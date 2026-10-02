package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

var ErrPreviewSourceTrust = errors.New("preview source trust denied")

func NormalizeSourceTrustPolicy(policy string) (string, error) {
	if policy == "" {
		policy = "same_repository"
	}
	if policy != "same_repository" && policy != "approval_required" {
		return "", errors.New("sourceTrustPolicy must be same_repository or approval_required")
	}
	return policy, nil
}

// SourceTrust evaluates the full revision before any workload runs. Untrusted
// previews are blocked entirely: current job runners are not secretless sandboxes.
// Provider tokens used here remain inside controller-owned metadata requests.
func (s *Service) SourceTrust(ctx context.Context, resource core.WorkflowResource, revision core.WorkflowRevision) (*core.PreviewSourceTrustDecision, error) {
	if !resource.Temporary {
		return nil, nil
	}
	triggers, err := s.Store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, err
	}
	var trigger *core.WorkflowPreviewTrigger
	for i := range triggers {
		if triggers[i].ResourceID == resource.ID {
			if trigger != nil && triggers[i].ClosedAt == nil && trigger.ClosedAt == nil {
				return nil, errors.New("preview has multiple active source policies")
			}
			if trigger == nil || triggers[i].ClosedAt == nil {
				trigger = &triggers[i]
			}
		}
	}
	if trigger == nil {
		return nil, nil
	} // Operator-authored temporary applications have no PR inputs.
	decision := &core.PreviewSourceTrustDecision{Policy: "same_repository", CheckedAt: time.Now().UTC(), Sources: []core.PreviewSourceOrigin{}, CredentialScope: []string{}, Environments: []string{}}
	deny := func(reason string) (*core.PreviewSourceTrustDecision, error) {
		decision.Reason = reason
		return decision, fmt.Errorf("%w: %s", ErrPreviewSourceTrust, reason)
	}
	if trigger.ClosedAt != nil || !resource.Active {
		return deny("Preview is closed or inactive. Create a new preview before running code.")
	}
	if revision.SpecDigest != "" && revision.SpecDigest != resource.SpecDigest {
		return deny("Preview configuration changed. Start a new revision and review its sources.")
	}
	decision.Policy, err = NormalizeSourceTrustPolicy(trigger.SourceTrustPolicy)
	if err != nil {
		return deny(err.Error())
	}
	if trigger.TemplateID != "" {
		template, e := s.Store.GetWorkflowPreviewTemplate(ctx, trigger.TemplateID)
		if e != nil {
			return deny("Preview template is unavailable. Restore its policy before running code.")
		}
		decision.Policy, e = NormalizeSourceTrustPolicy(template.SourceTrustPolicy)
		if e != nil {
			return deny(e.Error())
		}
	}
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return decision, err
	}
	if !source.Active {
		return deny("Repository access configuration is inactive.")
	}
	docs, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(docs) != 1 || docs[0].Spec == nil {
		return deny("Preview configuration is invalid.")
	}
	if len(revision.Sources) != len(docs[0].Spec.Sources) {
		return deny("Preview source snapshot is incomplete. Redeploy before running checks.")
	}
	untrusted := decision.Policy == "approval_required"
	matched := false
	aliases := make([]string, 0, len(revision.Sources))
	for alias := range revision.Sources {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	for _, alias := range aliases {
		pinned := revision.Sources[alias]
		configured, ok := docs[0].Spec.Sources[alias]
		if !ok || normalizeRepository(configured.Repository) != normalizeRepository(pinned.Repository) || pinned.CommitSHA == "" {
			return deny("Preview source " + alias + " no longer matches the saved configuration.")
		}
		number := trigger.LinkedPullRequests[alias]
		if normalizeRepository(pinned.Repository) == normalizeRepository(trigger.Repository) {
			number = trigger.PullRequestNumber
			matched = true
		}
		if number < 1 {
			continue
		}
		if s.GitHub == nil && s.ResolvePreviewSource == nil {
			return deny("Cannot verify PR source origins. Restore GitHub access before running code.")
		}
		head, e := s.resolvePreviewSource(ctx, trigger.GitHubAppID, pinned.Repository, number)
		if e != nil {
			return deny("Cannot verify PR source " + alias + ". Restore repository access and retry.")
		}
		if head.Head.Repo == nil || head.Base.Repo == nil || head.Head.Repo.ID < 1 || head.Base.Repo.ID < 1 || head.Head.Repo.FullName == "" || normalizeRepository(head.Base.Repo.FullName) != normalizeRepository(pinned.Repository) {
			return deny("PR source " + alias + " has missing repository identity. Restore the source and retry.")
		}
		origin := core.PreviewSourceOrigin{Alias: alias, Repository: normalizeRepository(head.Base.Repo.FullName), RepositoryID: head.Base.Repo.ID, HeadRepository: normalizeRepository(head.Head.Repo.FullName), HeadRepositoryID: head.Head.Repo.ID, Fork: head.Head.Repo.ID != head.Base.Repo.ID, PullRequest: number, CommitSHA: pinned.CommitSHA, GitHubAppID: trigger.GitHubAppID}
		decision.Sources = append(decision.Sources, origin)
		if head.Head.SHA != pinned.CommitSHA {
			return deny("PR source " + alias + " advanced after this revision. Deploy and approve the current exact commits.")
		}
		if origin.Fork || origin.HeadRepository != origin.Repository {
			untrusted = true
		}
	}
	if !matched {
		return deny("Primary PR repository is missing from the source snapshot.")
	}
	for alias := range trigger.LinkedPullRequests {
		if _, ok := revision.Sources[alias]; !ok {
			return deny("Linked PR source " + alias + " is missing from the source snapshot.")
		}
	}
	scopes, environments, err := s.previewCredentialScope(ctx, source, docs[0])
	if err != nil {
		return deny("Cannot resolve the preview credential scope. Repair its bindings before approval.")
	}
	if trigger.GitHubAppID != "" {
		connection, e := s.Store.GetGitHubApp(ctx, trigger.GitHubAppID)
		if e != nil {
			return deny("Source verification connection is unavailable.")
		}
		scopes = append(scopes, "source-verifier:"+connection.ID+":"+connection.UpdatedAt.UTC().Format(time.RFC3339Nano))
		slices.Sort(scopes)
	}
	decision.CredentialScope, decision.Environments = scopes, environments
	// Content hash caches are not part of a trust identity; the exact commit is.
	exact := map[string]core.WorkflowSourceRevision{}
	for alias, item := range revision.Sources {
		item.ContentHashes = nil
		exact[alias] = item
	}
	input := struct {
		ResourceID, ProjectID, SpecDigest, ConfigSHA, Policy, TriggerID, TemplateID, URL string
		Sources                                                                          map[string]core.WorkflowSourceRevision
		Origins                                                                          []core.PreviewSourceOrigin
		Scope, Environments                                                              []string
	}{resource.ID, source.ProjectID, resource.SpecDigest, resource.ConfigSHA, decision.Policy, trigger.ID, trigger.TemplateID, trigger.PreviewURL, exact, decision.Sources, scopes, environments}
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	decision.Digest = "sha256:" + hex.EncodeToString(sum[:])
	if !untrusted {
		decision.Allowed = true
		decision.Reason = "All PR source repositories match their target repository identities."
		return decision, nil
	}
	data, ok := s.Store.(store.PreviewSourceTrustStore)
	if !ok {
		return deny("Source approval storage is unavailable.")
	}
	approvals, err := data.ListPreviewSourceTrustApprovals(ctx, resource.ID)
	if err != nil {
		return decision, err
	}
	for _, approval := range approvals {
		if approval.Digest != decision.Digest || approval.RevokedAt != nil || !approval.ExpiresAt.After(decision.CheckedAt) {
			continue
		}
		if approval.ActorID != "controller-owner" {
			user, e := s.Store.GetUser(ctx, approval.ActorID)
			if e != nil || user.State != core.UserStateActive || user.SystemRole != core.UserRoleOwner {
				continue
			}
		}
		decision.Allowed, decision.ApprovalID, decision.Reason = true, approval.ID, "An unexpired owner approval covers these exact sources, credentials and environments."
		return decision, nil
	}
	return deny("PR code requires owner approval for these exact source commits, credential scope and environments. Review sourceTrust on this run, approve its digest with an expiry, then retry.")
}

// Include metadata and references only. Approval does not decrypt credentials.
func (s *Service) previewCredentialScope(ctx context.Context, source core.ConfigSource, doc Document) ([]string, []string, error) {
	scopes := []string{"config-source:" + source.ID, "github-app:" + source.GitHubAppID, "repository-credential:" + source.CredentialSecretID}
	if source.GitHubAppID != "" {
		connection, err := s.Store.GetGitHubApp(ctx, source.GitHubAppID)
		if err != nil {
			return nil, nil, err
		}
		scopes = append(scopes, "repository-access:"+connection.ID+":"+connection.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	previewScopes, err := s.previewServiceScopes(ctx, source.ProjectID, *doc.Spec)
	if err != nil {
		return nil, nil, err
	}
	scopes = append(scopes, previewScopes...)
	environments := []string{}
	refs := map[string]bool{}
	if source.CredentialSecretID != "" {
		refs[source.CredentialSecretID] = true
	}
	usesDockerBuilder := false
	collect := func(jobs map[string]JobSpec) {
		for _, job := range jobs {
			for _, secret := range job.Secrets {
				if secret.SecretRef != "" {
					refs[secret.SecretRef] = true
				}
			}
			if job.Builder == "docker" {
				usesDockerBuilder = true
			}
			if job.Builder != "" {
				scopes = append(scopes, "builder:"+job.Builder)
			}
		}
	}
	collect(doc.Spec.Jobs)
	collect(doc.Spec.Finally)
	for _, stage := range doc.Spec.Stages {
		environments = append(environments, stage.Name+":"+stage.TargetRef+":"+stage.URL)
		if stage.TargetRef != "" {
			target, err := s.resolveTarget(ctx, stage.TargetRef)
			if err != nil {
				return nil, nil, err
			}
			scopes = append(scopes, previewServerScope("target", target))
		}
		for _, name := range stage.Deploy {
			bindings, err := s.effectiveServiceBindings(ctx, source.ProjectID, doc.Spec.Deployments[name], stage)
			if err != nil {
				return nil, nil, err
			}
			for _, binding := range bindings {
				if _, deferred := PreviewServiceAlias(binding.ServiceRef); deferred {
					continue
				}
				service, err := s.Store.GetService(ctx, binding.ServiceRef)
				if err != nil {
					return nil, nil, err
				}
				scopes = append(scopes, fmt.Sprintf("service:%s:%d", service.ID, service.Revision))
			}
		}
		for _, check := range stage.Checks {
			resources, err := s.Store.ListWorkflowResources(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			found := false
			for _, pipeline := range resources {
				if pipeline.Kind != KindPipeline || pipeline.Name != check.PipelineRef || !pipeline.Active {
					continue
				}
				if found {
					return nil, nil, errors.New("ambiguous check pipeline")
				}
				found = true
				pd, err := Parse(pipeline.Path, []byte(pipeline.Document))
				if err != nil || len(pd) != 1 || pd[0].Pipeline == nil {
					return nil, nil, errors.New("invalid check pipeline")
				}
				scopes = append(scopes, "pipeline:"+pipeline.ID+":"+pipeline.SpecDigest)
				pipelineSource, err := s.Store.GetConfigSource(ctx, pipeline.ConfigSourceID)
				if err != nil || !pipelineSource.Active {
					return nil, nil, errors.New("pipeline repository access unavailable")
				}
				scopes = append(scopes, "pipeline-config-source:"+pipelineSource.ID+":"+pipelineSource.GitHubAppID+":"+pipelineSource.CredentialSecretID)
				if pipelineSource.CredentialSecretID != "" {
					refs[pipelineSource.CredentialSecretID] = true
				}
				if pipelineSource.GitHubAppID != "" {
					connection, err := s.Store.GetGitHubApp(ctx, pipelineSource.GitHubAppID)
					if err != nil {
						return nil, nil, err
					}
					scopes = append(scopes, "repository-access:"+connection.ID+":"+connection.UpdatedAt.UTC().Format(time.RFC3339Nano))
				}
				collect(pd[0].Pipeline.Jobs)
				collect(pd[0].Pipeline.Finally)
			}
			if !found {
				return nil, nil, errors.New("check pipeline unavailable")
			}
		}
	}
	if usesDockerBuilder {
		servers, err := s.Store.ListServers(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, server := range servers {
			if server.Runtime == core.ServerRuntimeBuilder && server.State == "ready" && server.Builder != nil && server.Builder.MaxConcurrent > 0 {
				scopes = append(scopes, previewServerScope("builder", server))
				refs[server.Builder.SSHSecretID] = true
			}
		}
	}
	secrets, err := s.Store.ListSecrets(ctx)
	if err != nil {
		return nil, nil, err
	}
	for ref := range refs {
		found := false
		for _, secret := range secrets {
			if secret.ID == ref || secret.Name == ref {
				scopes = append(scopes, "secret:"+secret.ID+":"+secret.UpdatedAt.UTC().Format(time.RFC3339Nano))
				found = true
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("secret reference unavailable")
		}
	}
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	slices.Sort(environments)
	return scopes, environments, nil
}

// Scope target identity, connection settings and stored credentials without
// exposing connection credentials in persisted decisions or API responses.
func previewServerScope(kind string, server core.Server) string {
	private := ""
	if server.Kubernetes != nil {
		private = server.Kubernetes.KubeconfigData + "\x00" + server.Kubernetes.CertificateAuthorityData
	}
	credentialDigest := sha256.Sum256([]byte(private))
	public, _ := json.Marshal(server)
	sum := sha256.Sum256(append(public, credentialDigest[:]...))
	return kind + ":" + server.ID + ":" + hex.EncodeToString(sum[:])
}

type previewTrustParentKey struct{}

func (s *Service) checkRevisionTrust(ctx context.Context, revision core.WorkflowRevision) error {
	if revision.ResourceID == "" {
		return nil
	}
	if parent, ok := ctx.Value(previewTrustParentKey{}).(core.WorkflowRevision); ok && parent.ID != revision.ID {
		if err := s.checkRevisionTrust(context.WithValue(ctx, previewTrustParentKey{}, nil), parent); err != nil {
			return err
		}
	}
	resource, err := s.Store.GetWorkflowResource(ctx, revision.ResourceID)
	if err != nil {
		return err
	}
	decision, err := s.SourceTrust(ctx, resource, revision)
	if decision != nil {
		if data, ok := s.Store.(store.PreviewSourceTrustStore); ok {
			if saveErr := data.UpdatePreviewSourceTrustDecision(ctx, revision.ID, decision); saveErr != nil {
				return saveErr
			}
		}
	}
	return err
}

// CheckDeploymentTrust also protects direct API starts on generated preview apps.
func (s *Service) CheckDeploymentTrust(ctx context.Context, app core.App, commit string) error {
	if app.HelmProvenance.WorkflowResourceID == "" {
		for _, pr := range app.HelmProvenance.PullRequests {
			if s.GitHub == nil && s.ResolvePreviewSource == nil || pr.CommitSHA == "" {
				return fmt.Errorf("%w: recreate this legacy preview with verified GitHub source identities", ErrPreviewSourceTrust)
			}
			head, err := s.resolvePreviewSource(ctx, pr.GitHubAppID, pr.Repository, pr.Number)
			if err != nil || head.Head.Repo == nil || head.Base.Repo == nil || head.Head.Repo.ID < 1 || head.Head.Repo.ID != head.Base.Repo.ID || normalizeRepository(head.Head.Repo.FullName) != normalizeRepository(pr.Repository) || normalizeRepository(head.Base.Repo.FullName) != normalizeRepository(pr.Repository) || head.Head.SHA != pr.CommitSHA {
				return fmt.Errorf("%w: legacy previews require verified same-repository PRs at the accepted commit; use a WorkflowTemplate for scoped fork approval", ErrPreviewSourceTrust)
			}
			if normalizeRepository(pr.Repository) == normalizeRepository(app.SourceRepo) && commit != pr.CommitSHA {
				return ErrPreviewSourceTrust
			}
		}
		return nil
	}
	resource, err := s.Store.GetWorkflowResource(ctx, app.HelmProvenance.WorkflowResourceID)
	if err != nil {
		return err
	}
	if !resource.Temporary {
		return nil
	}
	revision, err := s.Store.GetWorkflowRevision(ctx, app.HelmProvenance.WorkflowRevisionID)
	if err != nil {
		return fmt.Errorf("%w: preview run evidence is missing", ErrPreviewSourceTrust)
	}
	if revision.ResourceID != resource.ID {
		return ErrPreviewSourceTrust
	}
	if err := s.checkRevisionTrust(ctx, revision); err != nil {
		return err
	}
	for _, pinned := range revision.Sources {
		if normalizeRepository(pinned.Repository) == normalizeRepository(app.SourceRepo) && pinned.CommitSHA == commit {
			return nil
		}
	}
	return fmt.Errorf("%w: run the preview workflow to select an approved chart revision", ErrPreviewSourceTrust)
}

func (s *Service) resolvePreviewSource(ctx context.Context, appID, repository string, number int) (githubapp.PullRequestHead, error) {
	if s.ResolvePreviewSource != nil {
		return s.ResolvePreviewSource(ctx, appID, repository, number)
	}
	return s.GitHub.PullRequestHead(ctx, appID, repository, number)
}
