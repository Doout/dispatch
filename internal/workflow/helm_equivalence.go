package workflow

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type workflowEquivalenceStore interface {
	SaveWorkflowEquivalence(context.Context, core.WorkflowEquivalence) error
	GetWorkflowEquivalence(context.Context, string) (core.WorkflowEquivalence, error)
	GetHelmEquivalence(context.Context, string) (core.HelmEquivalence, error)
}

// An observation can replace automatic execution only when there are no checks,
// approvals, or finally jobs to run, and prior successful job results still match
// all declared inputs including resolved secrets. Manual Start never uses this.
func (s *Service) reuseEquivalentWorkflow(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, snapshot map[string]core.WorkflowSourceRevision) bool {
	data, ok := s.Store.(workflowEquivalenceStore)
	if !ok || s.Deployments == nil {
		return false
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return false
	}
	spec := documents[0].Spec
	if len(spec.Stages) == 0 || len(spec.Finally) != 0 {
		return false
	}
	for _, stage := range spec.Stages {
		if stage.Approval == "required" || len(stage.Checks) != 0 || len(stage.Deploy) == 0 {
			return false
		}
	}
	// Do not wait behind an executing workflow while holding the scheduling lock.
	// The ordinary path will queue the observed inputs as it did before.
	unlock, locked := s.tryResourceExecutionLock(resource.ID)
	if !locked {
		return false
	}
	defer unlock()
	revisions, err := s.Store.ListWorkflowRevisions(ctx, resource.ID, 1)
	if err != nil || len(revisions) != 1 || revisions[0].State != "succeeded" || revisions[0].SpecDigest != resource.SpecDigest {
		return false
	}
	previous := revisions[0]
	if !onlyHelmInputsChanged(*spec, previous.Sources, snapshot) {
		return false
	}
	outputs, reusable := s.reusableWorkflowOutputs(ctx, resource, source, previous, snapshot, spec.Jobs)
	if !reusable {
		return false
	}
	candidate := core.WorkflowRevision{ResourceID: resource.ID, Sources: snapshot, Outputs: outputs, Trigger: "poll"}
	type preparedCandidate struct {
		name string
		preparedHelmDeployment
	}
	preparedCandidates := []preparedCandidate{}
	for _, stage := range spec.Stages {
		server, err := s.resolveTarget(ctx, stage.TargetRef)
		if err != nil {
			return false
		}
		for _, name := range stage.Deploy {
			prepared, err := s.prepareHelmDeployment(ctx, resource, source, candidate, stage, name, spec.Deployments[name], server)
			if err != nil || len(prepared.bindings) != 0 {
				return false
			}
			preparedCandidates = append(preparedCandidates, preparedCandidate{name, prepared})
		}
	}
	// Resolve names and effective values before considering a cached observation:
	// a target name may now point at a different server without changing the YAML.
	if observed, err := data.GetWorkflowEquivalence(ctx, resource.ID); err == nil && observed.BaselineRevisionID == previous.ID && snapshotDigest(observed.Sources) == snapshotDigest(snapshot) && len(observed.Results) == len(preparedCandidates) {
		matches := true
		for _, prepared := range preparedCandidates {
			proof, err := data.GetHelmEquivalence(ctx, prepared.app.ID)
			if err != nil || proof.ProjectID != source.ProjectID || proof.ServerID != prepared.app.ServerID || proof.AppName != prepared.app.Name || proof.AppSpecDigest != prepared.expectedAppSpecDigest || proof.CandidateSpecDigest != prepared.app.SpecDigest() || proof.ChartCommit != prepared.chart.CommitSHA || observed.AppProofs[prepared.app.ID] != core.HelmEquivalenceFingerprint(proof) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	observation := core.WorkflowEquivalence{ResourceID: resource.ID, ProjectID: source.ProjectID, SpecDigest: resource.SpecDigest, BaselineRevisionID: previous.ID, Sources: snapshot, Results: []core.WorkflowDeploymentResult{}, AppProofs: map[string]string{}}
	for _, prepared := range preparedCandidates {
		deployment, unchanged, err := s.Deployments.ReuseCandidateIfUnchanged(ctx, prepared.app, prepared.chart.CommitSHA, prepared.expectedAppSpecDigest)
		if err != nil || !unchanged {
			return false
		}
		proof, err := data.GetHelmEquivalence(ctx, prepared.app.ID)
		if err != nil || proof.DeploymentID != deployment.ID || proof.AppSpecDigest != prepared.expectedAppSpecDigest || proof.CandidateSpecDigest != prepared.app.SpecDigest() || proof.ChartCommit != prepared.chart.CommitSHA {
			return false
		}
		observation.AppProofs[prepared.app.ID] = core.HelmEquivalenceFingerprint(proof)
		observation.Results = append(observation.Results, core.WorkflowDeploymentResult{DeploymentName: prepared.name, AppID: prepared.app.ID, DeploymentID: deployment.ID, Outcome: "unchanged", Reason: "The effective Helm release is unchanged.", CheckedAt: proof.CheckedAt})
	}
	observation.CheckedAt = time.Now().UTC()
	// The save rechecks every anchor together. A concurrent manual run, app edit,
	// source edit, or deployment invalidates the proof and follows normal execution.
	return len(observation.Results) != 0 && data.SaveWorkflowEquivalence(ctx, observation) == nil
}

func onlyHelmInputsChanged(spec ApplicationSpec, before, after map[string]core.WorkflowSourceRevision) bool {
	if len(before) != len(after) {
		return false
	}
	helmSources := map[string]bool{}
	for _, deployment := range spec.Deployments {
		helmSources[deployment.Helm.SourceRef] = true
		for _, file := range deployment.Helm.ValuesFiles {
			helmSources[file.SourceRef] = true
		}
	}
	for alias, current := range after {
		prior, ok := before[alias]
		if !ok || (!sameSourceInput(prior, current) && !helmSources[alias]) {
			return false
		}
	}
	return true
}

func (s *Service) reusableWorkflowOutputs(ctx context.Context, resource core.WorkflowResource, source core.ConfigSource, previous core.WorkflowRevision, snapshot map[string]core.WorkflowSourceRevision, jobs map[string]JobSpec) (map[string]map[string]string, bool) {
	results, err := s.Store.ListWorkflowJobResults(ctx, previous.ID)
	if err != nil {
		return nil, false
	}
	byName := map[string]core.WorkflowJobResult{}
	for _, result := range results {
		byName[result.JobName] = result
	}
	runtime := jobRuntime{service: s, source: source}
	outputs := map[string]map[string]string{}
	for name, job := range jobs {
		if job.Reuse != "onInputMatch" {
			return nil, false
		}
		jobSources := map[string]core.WorkflowSourceRevision{}
		for _, alias := range jobSourceAliases(job) {
			item, ok := snapshot[alias]
			if !ok {
				return nil, false
			}
			jobSources[alias] = item
		}
		secrets, err := runtime.resolveSecrets(ctx, job.Secrets)
		if err != nil {
			return nil, false
		}
		result, ok := byName[name]
		if !ok || result.ResourceID != resource.ID || result.State != "succeeded" || !completeOutputs(result.Outputs, job.Outputs) || result.Fingerprint != jobExecutionFingerprint(name, job, jobSources, nil, secrets) || !reflect.DeepEqual(result.Outputs, previous.Outputs[name]) {
			return nil, false
		}
		outputs[name] = result.Outputs
	}
	return outputs, true
}

func (s *Service) tryResourceExecutionLock(resourceID string) (func(), bool) {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	key := "resource:" + resourceID
	lock := s.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[key] = lock
	}
	s.mu.Unlock()
	if !lock.TryLock() {
		return nil, false
	}
	return lock.Unlock, true
}
