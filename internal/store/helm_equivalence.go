package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"sort"
)

var ErrHelmEquivalenceChanged = errors.New("Helm comparison inputs changed")

const equivalenceAppSelect = `SELECT id,project_id,server_id,name,source_repo,branch,source_auth_type,source_credential_id,build_type,context_path,dockerfile_path,compose_path,compose_content,helm_chart,helm_version,helm_repository,helm_values,helm_namespace,helm_release,pre_deploy_hook,post_deploy_hook,container_port,domain,state,created_at,helm_group_values,hook_environment,generated,template,helm_provenance FROM apps WHERE id=?`
const equivalenceServerSelect = `SELECT id,name,address,runtime,state,agent_mode,kubeconfig_path,kube_context,kube_namespace,kubeconfig_data,kube_ca_data,openshift_service_account,openshift_service_account_namespace,openshift_token_secret,openshift_connected_at,relay_access_token,relay_pending_events,relay_oldest_pending_at,relay_last_connected_at,relay_last_error,created_at FROM servers WHERE id=?`
const equivalenceSelect = `SELECT app_id,project_id,app_name,app_spec_digest,candidate_spec_digest,chart_commit,server_id,target_digest,deployment_id,checked_at FROM application_helm_equivalence WHERE app_id=?`

func scanHelmEquivalence(row scanner) (core.HelmEquivalence, error) {
	var proof core.HelmEquivalence
	var checked string
	err := row.Scan(&proof.AppID, &proof.ProjectID, &proof.AppName, &proof.AppSpecDigest, &proof.CandidateSpecDigest, &proof.ChartCommit, &proof.ServerID, &proof.TargetDigest, &proof.DeploymentID, &checked)
	proof.CheckedAt = parseTime(checked)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return proof, err
}

func (s *SQLStore) validateHelmEquivalence(ctx context.Context, tx *changeTx, proof core.HelmEquivalence, lock bool) error {
	if proof.AppID == "" || proof.ProjectID == "" || proof.AppName == "" || proof.AppSpecDigest == "" || proof.CandidateSpecDigest == "" || proof.ChartCommit == "" || proof.TargetDigest == "" || proof.DeploymentID == "" || proof.CheckedAt.IsZero() {
		return ErrHelmEquivalenceChanged
	}
	aq, sq := equivalenceAppSelect, equivalenceServerSelect
	if lock && s.postgres {
		aq += ` FOR UPDATE`
		sq += ` FOR SHARE`
	}
	app, err := scanApp(tx.QueryRowContext(ctx, s.q(aq), proof.AppID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrHelmEquivalenceChanged
	}
	if err != nil {
		return err
	}
	if app.ProjectID != proof.ProjectID || app.Name != proof.AppName || app.ServerID != proof.ServerID || app.SpecDigest() != proof.AppSpecDigest || app.BuildType != core.BuildTypeHelm || app.Template || app.State == "closed" || app.PreDeployHook != "" || app.PostDeployHook != "" || len(app.HookEnvironment) != 0 {
		return ErrHelmEquivalenceChanged
	}
	server, err := scanServer(tx.QueryRowContext(ctx, s.q(sq), app.ServerID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrHelmEquivalenceChanged
	}
	if err != nil {
		return err
	}
	if server.State != "ready" || server.Kubernetes == nil || core.HelmTargetDigest(server) != proof.TargetDigest {
		return ErrHelmEquivalenceChanged
	}
	var id, state string
	err = tx.QueryRowContext(ctx, s.q(`SELECT id,state FROM deployments WHERE app_id=? ORDER BY created_at DESC,id DESC LIMIT 1`), app.ID).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (id != proof.DeploymentID || state != string(core.DeploymentSucceeded)) {
		return ErrHelmEquivalenceChanged
	}
	if err != nil {
		return err
	}
	var excluded int
	err = tx.QueryRowContext(ctx, s.q(`SELECT (SELECT count(*) FROM app_service_bindings WHERE app_id=?)+(SELECT count(*) FROM deployment_service_bindings WHERE deployment_id=?)+(SELECT count(*) FROM deployments WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled'))`), app.ID, id, app.ID).Scan(&excluded)
	if err != nil {
		return err
	}
	if excluded != 0 {
		return ErrHelmEquivalenceChanged
	}
	return nil
}

func (s *SQLStore) SaveHelmEquivalence(ctx context.Context, proof core.HelmEquivalence) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.validateHelmEquivalence(ctx, tx, proof, true); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO application_helm_equivalence(app_id,project_id,app_name,app_spec_digest,candidate_spec_digest,chart_commit,server_id,target_digest,deployment_id,checked_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(app_id) DO UPDATE SET project_id=excluded.project_id,app_name=excluded.app_name,app_spec_digest=excluded.app_spec_digest,candidate_spec_digest=excluded.candidate_spec_digest,chart_commit=excluded.chart_commit,server_id=excluded.server_id,target_digest=excluded.target_digest,deployment_id=excluded.deployment_id,checked_at=excluded.checked_at`), proof.AppID, proof.ProjectID, proof.AppName, proof.AppSpecDigest, proof.CandidateSpecDigest, proof.ChartCommit, proof.ServerID, proof.TargetDigest, proof.DeploymentID, stamp(proof.CheckedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) equivalenceReadOptions() *sql.TxOptions {
	options := &sql.TxOptions{ReadOnly: true}
	if s.postgres {
		options.Isolation = sql.LevelRepeatableRead
	}
	return options
}

// GetHelmEquivalence returns only evidence whose anchors are still current.
func (s *SQLStore) GetHelmEquivalence(ctx context.Context, appID string) (core.HelmEquivalence, error) {
	tx, err := s.db.BeginTx(ctx, s.equivalenceReadOptions())
	if err != nil {
		return core.HelmEquivalence{}, err
	}
	defer tx.Rollback()
	proof, err := scanHelmEquivalence(tx.QueryRowContext(ctx, s.q(equivalenceSelect), appID))
	if err == nil {
		err = s.validateHelmEquivalence(ctx, tx, proof, false)
	}
	if errors.Is(err, ErrHelmEquivalenceChanged) {
		err = ErrNotFound
	}
	return proof, err
}

func (s *SQLStore) validateWorkflowEquivalence(ctx context.Context, tx *changeTx, observed core.WorkflowEquivalence, lock bool) error {
	if observed.ResourceID == "" || observed.ProjectID == "" || observed.SpecDigest == "" || observed.BaselineRevisionID == "" || observed.CheckedAt.IsZero() || len(observed.Sources) == 0 || len(observed.Results) == 0 || len(observed.AppProofs) != len(observed.Results) {
		return ErrHelmEquivalenceChanged
	}
	query := `SELECT r.spec_digest,c.project_id,r.active,c.active,r.state FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=?`
	if lock && s.postgres {
		query += ` FOR UPDATE OF r,c`
	}
	var spec, project, resourceState string
	var ra, sa bool
	err := tx.QueryRowContext(ctx, s.q(query), observed.ResourceID).Scan(&spec, &project, &ra, &sa, &resourceState)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (spec != observed.SpecDigest || project != observed.ProjectID || !ra || !sa || resourceState == "invalid") {
		return ErrHelmEquivalenceChanged
	}
	if err != nil {
		return err
	}
	var id, state, revisionSpec string
	err = tx.QueryRowContext(ctx, s.q(`SELECT id,state,spec_digest FROM workflow_revisions WHERE resource_id=? ORDER BY created_at DESC,id DESC LIMIT 1`), observed.ResourceID).Scan(&id, &state, &revisionSpec)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (id != observed.BaselineRevisionID || state != "succeeded" || revisionSpec != observed.SpecDigest) {
		return ErrHelmEquivalenceChanged
	}
	if err != nil {
		return err
	}
	var active int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT count(*) FROM workflow_revisions WHERE resource_id=? AND state NOT IN ('succeeded','failed','cancelled','canceled','skipped')`), observed.ResourceID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrHelmEquivalenceChanged
	}
	results := append([]core.WorkflowDeploymentResult{}, observed.Results...)
	sort.Slice(results, func(i, j int) bool { return results[i].AppID < results[j].AppID })
	seen := map[string]bool{}
	for _, result := range results {
		if result.Outcome != "unchanged" || seen[result.AppID] || result.DeploymentName == "" {
			return ErrHelmEquivalenceChanged
		}
		seen[result.AppID] = true
		proof, err := scanHelmEquivalence(tx.QueryRowContext(ctx, s.q(equivalenceSelect), result.AppID))
		if errors.Is(err, ErrNotFound) {
			return ErrHelmEquivalenceChanged
		}
		if err != nil {
			return err
		}
		if proof.ProjectID != observed.ProjectID || proof.DeploymentID != result.DeploymentID || core.HelmEquivalenceFingerprint(proof) != observed.AppProofs[result.AppID] {
			return ErrHelmEquivalenceChanged
		}
		if err = s.validateHelmEquivalence(ctx, tx, proof, lock); err != nil {
			return err
		}
		// Filesystem kubeconfig contents can change independently of stored metadata.
		// Never use a saved observation as a same-source cache for those targets.
		if !lock {
			var path, data string
			if err = tx.QueryRowContext(ctx, s.q(`SELECT kubeconfig_path,kubeconfig_data FROM servers WHERE id=?`), proof.ServerID).Scan(&path, &data); err != nil {
				return err
			}
			if path != "" && data == "" {
				return ErrHelmEquivalenceChanged
			}
		}
	}
	return nil
}

func (s *SQLStore) SaveWorkflowEquivalence(ctx context.Context, observed core.WorkflowEquivalence) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.validateWorkflowEquivalence(ctx, tx, observed, true); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO workflow_helm_equivalence(resource_id,project_id,spec_digest,baseline_revision_id,sources,results,app_proofs,checked_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(resource_id) DO UPDATE SET project_id=excluded.project_id,spec_digest=excluded.spec_digest,baseline_revision_id=excluded.baseline_revision_id,sources=excluded.sources,results=excluded.results,app_proofs=excluded.app_proofs,checked_at=excluded.checked_at`), observed.ResourceID, observed.ProjectID, observed.SpecDigest, observed.BaselineRevisionID, jsonText(observed.Sources), jsonText(observed.Results), jsonText(observed.AppProofs), stamp(observed.CheckedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) GetWorkflowEquivalence(ctx context.Context, resourceID string) (core.WorkflowEquivalence, error) {
	var observed core.WorkflowEquivalence
	tx, err := s.db.BeginTx(ctx, s.equivalenceReadOptions())
	if err != nil {
		return observed, err
	}
	defer tx.Rollback()
	var sources, results, proofs, checked string
	err = tx.QueryRowContext(ctx, s.q(`SELECT resource_id,project_id,spec_digest,baseline_revision_id,sources,results,app_proofs,checked_at FROM workflow_helm_equivalence WHERE resource_id=?`), resourceID).Scan(&observed.ResourceID, &observed.ProjectID, &observed.SpecDigest, &observed.BaselineRevisionID, &sources, &results, &proofs, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return observed, ErrNotFound
	}
	if err != nil {
		return observed, err
	}
	for _, item := range []struct {
		raw    string
		target any
	}{{sources, &observed.Sources}, {results, &observed.Results}, {proofs, &observed.AppProofs}} {
		if err = json.Unmarshal([]byte(item.raw), item.target); err != nil {
			return observed, err
		}
	}
	observed.CheckedAt = parseTime(checked)
	err = s.validateWorkflowEquivalence(ctx, tx, observed, false)
	if errors.Is(err, ErrHelmEquivalenceChanged) {
		err = ErrNotFound
	}
	return observed, err
}
