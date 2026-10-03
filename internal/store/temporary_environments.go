package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/oklog/ulid/v2"
)

var ErrTemporaryEnvironmentChanged = errors.New("temporary environment review, ownership, deadline or revision changed")

type frozenServiceBindingsKey struct{}

func (s *SQLStore) CreateTemporaryEnvironmentReview(ctx context.Context, r core.TemporaryEnvironmentReview) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO temporary_environment_reviews(id,project_id,state,payload) VALUES(?,?,'prepared',?)`), r.ID, r.Input.ProjectID, jsonText(r))
	return err
}
func (s *SQLStore) GetTemporaryEnvironmentReview(ctx context.Context, id string) (core.TemporaryEnvironmentReview, error) {
	var r core.TemporaryEnvironmentReview
	var raw, state string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload,state FROM temporary_environment_reviews WHERE id=?`), id).Scan(&raw, &state)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(raw), &r)
	r.State = state
	return r, err
}

const temporaryColumns = `payload,state,revision,expires_at,updated_at,lease_token,lease_until`

func scanTemporary(row scanner) (core.TemporaryEnvironment, error) {
	var e core.TemporaryEnvironment
	var raw, state, expiry, updated, token, lease string
	var revision int64
	err := row.Scan(&raw, &state, &revision, &expiry, &updated, &token, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return e, err
	}
	if err = json.Unmarshal([]byte(raw), &e); err != nil {
		return e, err
	}
	e.State, e.Revision, e.ExpiresAt, e.UpdatedAt, e.LeaseToken, e.LeaseUntil = state, revision, parseTime(expiry), parseTime(updated), token, parseTime(lease)
	return e, nil
}
func (s *SQLStore) GetTemporaryEnvironment(ctx context.Context, id string) (core.TemporaryEnvironment, error) {
	return scanTemporary(s.db.QueryRowContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE id=?`), id))
}
func (s *SQLStore) TemporaryEnvironmentForApp(ctx context.Context, id string) (core.TemporaryEnvironment, error) {
	return scanTemporary(s.db.QueryRowContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE app_id=?`), id))
}
func (s *SQLStore) ListTemporaryEnvironments(ctx context.Context, project string) ([]core.TemporaryEnvironment, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE (?='' OR project_id=?) ORDER BY created_at DESC`), project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.TemporaryEnvironment{}
	for rows.Next() {
		e, err := scanTemporary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *SQLStore) AcceptTemporaryEnvironment(ctx context.Context, review core.TemporaryEnvironmentReview, actor core.Identity, deploymentID string, now time.Time) (core.TemporaryEnvironment, error) {
	var e core.TemporaryEnvironment
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environment_reviews SET state='accepted' WHERE id=? AND state='prepared' AND payload=?`), review.ID, jsonText(review))
	if err != nil {
		return e, err
	}
	n, _ := result.RowsAffected()
	if n != 1 || !review.ExpiresAt.After(now) {
		return e, ErrTemporaryEnvironmentChanged
	}
	// Lock policy before counting accepted/uncertain environments. Reducing limits never evicts existing ownership.
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE project_infrastructure_policies SET revision=revision WHERE project_id=?`), review.Input.ProjectID); err != nil {
		return e, err
	}
	p, err := scanQuotaPolicy(tx.QueryRowContext(ctx, s.q(`SELECT `+quotaPolicyColumns+` FROM project_infrastructure_policies WHERE project_id=?`), review.Input.ProjectID))
	if errors.Is(err, ErrNotFound) {
		return e, &core.InfrastructureQuotaViolation{Code: "policy_required", Limit: "maxTemporaryEnvironments", Requested: 1}
	}
	if err != nil {
		return e, err
	}
	if review.Input.LifetimeSeconds <= 0 || review.Input.LifetimeSeconds > p.MaxTemporaryLifetimeSeconds {
		return e, &core.InfrastructureQuotaViolation{Code: "lifetime_exceeded", Limit: "maxTemporaryLifetimeSeconds", Maximum: p.MaxTemporaryLifetimeSeconds, Requested: review.Input.LifetimeSeconds}
	}
	var used int64
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM temporary_environments WHERE project_id=? AND state<>'closed'`), review.Input.ProjectID).Scan(&used); err != nil {
		return e, err
	}
	if p.MaxTemporaryEnvironments >= 0 && used >= p.MaxTemporaryEnvironments {
		return e, &core.InfrastructureQuotaViolation{Code: "quota_exceeded", Limit: "maxTemporaryEnvironments", Maximum: p.MaxTemporaryEnvironments, Usage: used, Requested: 1}
	}
	// Compare the actual template under its row lock, preventing edits between review and insertion.
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE apps SET state=state WHERE id=?`), review.Input.TemplateID); err != nil {
		return e, err
	}
	current, err := s.temporaryTemplate(ctx, tx, review.Input.TemplateID)
	if err != nil {
		return e, err
	}
	if !current.Template || current.ProjectID != review.Input.ProjectID || current.SpecDigest() != review.TemplateDigest {
		return e, ErrTemporaryEnvironmentChanged
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE servers SET state=state WHERE id=?`), review.Input.ServerID); err != nil {
		return e, err
	}
	var runtime, node, state, routingRaw string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT runtime,agent_node_id,state,routing_config FROM servers WHERE id=?`), review.Input.ServerID).Scan(&runtime, &node, &state, &routingRaw); err != nil {
		return e, err
	}
	if runtime != "docker" || node == "" || node != review.TargetNodeID || state != "ready" {
		return e, ErrTemporaryEnvironmentChanged
	}
	var currentRouting *core.RoutingConfig
	if json.Unmarshal([]byte(routingRaw), &currentRouting) != nil || jsonText(currentRouting) != jsonText(review.Routing) {
		return e, ErrTemporaryEnvironmentChanged
	}
	var generation int64
	var revoked bool
	var public string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT generation,revoked,public_key FROM edge_node_credentials WHERE network_id=?`), node).Scan(&generation, &revoked, &public); err != nil {
		return e, err
	}
	if generation != review.TargetGeneration || revoked || public == "" {
		return e, ErrTemporaryEnvironmentChanged
	}
	app := review.Clone
	app.CreatedAt = now
	if err = s.insertApp(ctx, tx, app); err != nil {
		return e, err
	}
	// Match ordinary binding acceptance: lock services before inserting consumers
	// so a concurrently reviewed deletion observes the accepted environment.
	refs := []string{}
	selectedRefs := map[string]bool{}
	for _, binding := range review.Input.ServiceBindings {
		if !selectedRefs[binding.ServiceRef] {
			refs = append(refs, binding.ServiceRef)
			selectedRefs[binding.ServiceRef] = true
		}
	}
	if len(selectedRefs) != len(review.ServiceRevisions) {
		return e, ErrDeploymentReviewChanged
	}
	sort.Strings(refs)
	for _, ref := range refs {
		var raw string
		if err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), ref).Scan(&raw); err != nil {
			return e, ErrDeploymentReviewChanged
		}
		service, err := decodeService(raw)
		if err != nil || service.ProjectID != app.ProjectID || service.Revision != review.ServiceRevisions[ref] {
			return e, ErrDeploymentReviewChanged
		}
	}
	for _, binding := range review.Input.ServiceBindings {
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO app_service_bindings(app_id,alias,service_id,payload) VALUES(?,?,?,?)`), app.ID, binding.Alias, binding.ServiceRef, jsonText(binding)); err != nil {
			return e, err
		}
	}
	policy, err := core.NormalizeHealthPolicy(app.HealthPolicy)
	if err != nil {
		return e, err
	}
	d := core.Deployment{ID: deploymentID, AppID: app.ID, CommitSHA: review.Input.SourceSHA, SpecDigest: app.SpecDigest(), State: core.DeploymentQueued, Message: "Temporary environment accepted", CreatedAt: now, Health: core.DeploymentHealth{Policy: policy, State: "pending", Checks: []core.HealthCheckResult{}}, Acceptance: &core.DeploymentReview{ExpectedAppName: app.Name, ProjectID: app.ProjectID, AppSpecDigest: app.SpecDigest(), BindingsDigest: core.ServiceBindingConfigurationDigest(review.Input.ServiceBindings), ServiceRevisions: review.ServiceRevisions}, ExecutionAppName: app.Name, ExecutionGenerated: true}
	if err = s.insertDeployment(context.WithValue(ctx, frozenServiceBindingsKey{}, true), tx, d); err != nil {
		return e, err
	}
	plan, err := routing.Plan(d, app, core.Server{ID: app.ServerID, Routing: currentRouting})
	if err != nil {
		return e, ErrTemporaryEnvironmentChanged
	}
	if plan != nil {
		if review.Route == nil || plan.Hostname != review.Route.Hostname || app.Domain != plan.Hostname {
			return e, ErrTemporaryEnvironmentChanged
		}
		if _, err = s.reserveApplicationRoute(ctx, tx, *plan); err != nil {
			return e, err
		}
	} else if review.Route != nil {
		return e, ErrTemporaryEnvironmentChanged
	}
	e = core.TemporaryEnvironment{AppSpecDigest: app.SpecDigest(), Hostname: app.Domain, Routing: currentRouting, ID: review.EnvironmentID, ProjectID: app.ProjectID, Name: app.Name, TemplateID: review.Input.TemplateID, TemplateDigest: review.TemplateDigest, SourceSHA: d.CommitSHA, AppID: app.ID, DeploymentID: d.ID, ServerID: app.ServerID, TargetNodeID: review.TargetNodeID, TargetGeneration: review.TargetGeneration, Actor: actor, State: "accepted", Revision: 1, ExpiresAt: now.Add(time.Duration(review.Input.LifetimeSeconds) * time.Second), CreatedAt: now, UpdatedAt: now, CleanupOperationID: review.EnvironmentID, CleanupJobID: "cleanup-environment-" + review.EnvironmentID, Resources: []core.TemporaryResource{{Kind: "application", ID: app.ID, Ownership: "owned"}, {Kind: "deployment", ID: d.ID, Ownership: "owned"}, {Kind: "server", ID: app.ServerID, Ownership: "shared"}}}
	seenServices := map[string]bool{}
	for _, binding := range review.Input.ServiceBindings {
		if !seenServices[binding.ServiceRef] {
			e.Resources = append(e.Resources, core.TemporaryResource{Kind: "service", ID: binding.ServiceRef, Ownership: "shared"})
			seenServices[binding.ServiceRef] = true
		}
	}
	if plan != nil {
		e.Resources = append(e.Resources, core.TemporaryResource{Kind: "route", ID: plan.Hostname, Ownership: "owned"})
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO temporary_environments(id,project_id,app_id,deployment_id,state,revision,expires_at,created_at,updated_at,cleanup_operation_id,cleanup_job_id,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`), e.ID, e.ProjectID, e.AppID, e.DeploymentID, e.State, e.Revision, stamp(e.ExpiresAt), stamp(now), stamp(now), e.CleanupOperationID, e.CleanupJobID, jsonText(e))
	if err != nil {
		return e, err
	}
	return e, tx.Commit()
}
func (s *SQLStore) temporaryTemplate(ctx context.Context, tx *changeTx, id string) (core.App, error) {
	return scanApp(tx.QueryRowContext(ctx, s.q(`SELECT id,project_id,server_id,name,source_repo,branch,source_auth_type,source_credential_id,build_type,context_path,dockerfile_path,compose_path,compose_content,helm_chart,helm_version,helm_repository,helm_values,helm_namespace,helm_release,pre_deploy_hook,post_deploy_hook,container_port,domain,state,created_at,helm_group_values,hook_environment,generated,template,helm_provenance,health_policy FROM apps WHERE id=?`), id))
}
func (s *SQLStore) checkTemporaryDeployment(ctx context.Context, tx *changeTx, app string, now time.Time) error {
	// All mutations to an environment serialize on its row. No direct deployment API can bypass expiry.
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET revision=revision WHERE app_id=?`), app); err != nil {
		return err
	}
	var count int
	err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM temporary_environments WHERE app_id=?`), app).Scan(&count)
	if err == nil && count > 0 {
		return ErrTemporaryEnvironmentChanged
	}
	return err
}
func (s *SQLStore) ClaimTemporaryEnvironment(ctx context.Context, id string, now time.Time) (core.TemporaryEnvironment, error) {
	token := ulid.Make().String()
	r, err := s.db.ExecContext(ctx, s.q(`UPDATE temporary_environments SET lease_token=?,lease_until=? WHERE id=? AND state<>'closed' AND lease_until<=?`), token, stamp(now.Add(2*time.Minute)), id, stamp(now))
	if err = changed(r, err); err != nil {
		return core.TemporaryEnvironment{}, err
	}
	return s.GetTemporaryEnvironment(ctx, id)
}
func (s *SQLStore) SaveTemporaryEnvironment(ctx context.Context, e core.TemporaryEnvironment, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, err := scanTemporary(tx.QueryRowContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE id=?`), e.ID))
	if err != nil {
		return err
	}
	if previous.State == e.State && previous.Message == e.Message && jsonText(previous.Resources) == jsonText(e.Resources) {
		result, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET lease_token='',lease_until='' WHERE id=? AND revision=? AND lease_token=? AND lease_until>?`), e.ID, e.Revision, e.LeaseToken, stamp(now))
		if err = changed(result, err); err != nil {
			return ErrTemporaryEnvironmentChanged
		}
		return tx.Commit()
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET state=?,revision=revision+1,payload=?,updated_at=?,lease_token='',lease_until='' WHERE id=? AND revision=? AND lease_token=? AND lease_until>? AND (state IN ('closing','cleanup_blocked') OR expires_at>? OR ? IN ('closing','cleanup_blocked','closed'))`), e.State, jsonText(e), stamp(now), e.ID, e.Revision, e.LeaseToken, stamp(now), stamp(now), e.State)
	if err = changed(result, err); err != nil {
		return ErrTemporaryEnvironmentChanged
	}
	if e.State == "closed" {
		var count int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM runtime_jobs WHERE app_id=? AND operation NOT IN ('inspect','logs','storage_inspect','service_inspect','retention_inspect') AND state IN ('pending','running','unknown')`), e.AppID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrTemporaryEnvironmentChanged
		}
		var jobState, appState string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT state FROM runtime_jobs WHERE id=? AND app_id=? AND server_id=? AND operation='destroy'`), e.CleanupJobID, e.AppID, e.ServerID).Scan(&jobState); err != nil {
			return ErrTemporaryEnvironmentChanged
		}
		if err = tx.QueryRowContext(ctx, s.q(`SELECT state FROM apps WHERE id=?`), e.AppID).Scan(&appState); err != nil {
			return err
		}
		if e.Hostname != "" {
			var routes int
			if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM application_routes WHERE app_id=?`), e.AppID).Scan(&routes); err != nil {
				return err
			}
			if routes != 0 {
				return ErrTemporaryEnvironmentChanged
			}
		}
		if jobState != "succeeded" || appState != "closed" {
			return ErrTemporaryEnvironmentChanged
		}
		// The stopped workload no longer consumes shared services. Frozen deployment
		// captures and the environment's shared-resource history remain retained.
		if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM app_service_bindings WHERE app_id=?`), e.AppID); err != nil {
			return err
		}
	}
	outcome := e.State
	if outcome == "closed" {
		outcome = "succeeded"
	}
	if outcome == "cleanup_blocked" {
		outcome = "unknown"
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "temporary_environment", e.CleanupOperationID, outcome); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) StopTemporaryEnvironment(ctx context.Context, id string, revision int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET revision=revision WHERE id=?`), id); err != nil {
		return err
	}
	e, err := scanTemporary(tx.QueryRowContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE id=?`), id))
	if err != nil {
		return err
	}
	if e.Revision != revision || e.State == "closed" {
		return ErrTemporaryEnvironmentChanged
	}
	closing := e.State == "closing" || e.State == "cleanup_blocked"
	claim, keyed := core.MutationAcceptanceFromContext(ctx)
	if closing {
		if !keyed {
			return ErrTemporaryEnvironmentChanged
		}
		job, jobErr := scanRuntimeJob(tx.QueryRowContext(ctx, s.q(`SELECT `+runtimeJobColumns+` FROM runtime_jobs WHERE id=?`), e.CleanupJobID))
		if jobErr != nil || job.State != "failed" && job.State != "cancelled" && job.State != "acknowledged" {
			return ErrTemporaryEnvironmentChanged
		}
	}
	if keyed {
		e.CleanupOperationID = claim.OperationID
		e.CleanupJobID = "cleanup-environment-" + claim.OperationID
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET state='closing',revision=revision+1,updated_at=?,lease_token='',lease_until='',cleanup_operation_id=?,cleanup_job_id=?,payload=? WHERE id=? AND revision=?`), stamp(now), e.CleanupOperationID, e.CleanupJobID, jsonText(e), id, revision)
	if err = changed(result, err); err != nil {
		return ErrTemporaryEnvironmentChanged
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "temporary_environment", e.CleanupOperationID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET cancel_requested=TRUE,state=CASE WHEN state='pending' AND attempt=0 THEN 'cancelled' ELSE 'unknown' END,encrypted_request='',lease_token='',message='Temporary environment stopped; inspect any interrupted runtime mutation.',updated_at=? WHERE app_id=? AND state IN ('pending','running') AND operation NOT IN ('inspect','logs','retention_inspect')`), stamp(now), e.AppID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`UPDATE deployments SET state='cancelled',message='Temporary environment stopped',finished_at=?,lease_until=NULL WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled')`), stamp(now), e.AppID)
	if err != nil {
		return err
	}
	var deploymentState string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT state FROM deployments WHERE id=?`), e.DeploymentID).Scan(&deploymentState); err != nil {
		return err
	}
	if err = s.UpdateMutationOutcome(ctx, tx.Tx, "deployment", e.DeploymentID, deploymentState); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) ExtendTemporaryEnvironment(ctx context.Context, id string, revision int64, expires, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e, err := scanTemporary(tx.QueryRowContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE id=?`), id))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.q(`UPDATE project_infrastructure_policies SET revision=revision WHERE project_id=?`), e.ProjectID); err != nil {
		return err
	}
	var max int64
	if err = tx.QueryRowContext(ctx, s.q(`SELECT max_temporary_lifetime_seconds FROM project_infrastructure_policies WHERE project_id=?`), e.ProjectID).Scan(&max); err != nil {
		return err
	}
	if !expires.After(e.ExpiresAt) || expires.After(e.CreatedAt.Add(time.Duration(max)*time.Second)) {
		return ErrTemporaryEnvironmentChanged
	}
	r, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET expires_at=?,revision=revision+1,updated_at=?,lease_token='',lease_until='' WHERE id=? AND revision=? AND state NOT IN ('closing','cleanup_blocked','closed') AND expires_at>?`), stamp(expires), stamp(now), id, revision, stamp(now))
	if err = changed(r, err); err != nil {
		return ErrTemporaryEnvironmentChanged
	}
	return tx.Commit()
}

// A restart waits for the previous controller execution lease before dispatching
// an accepted job whose remote request has not been persisted yet.
func (s *SQLStore) ClaimTemporaryDeployment(ctx context.Context, environment, deployment string, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE deployments SET lease_until=? WHERE id=? AND state NOT IN ('succeeded','failed','cancelled') AND (lease_until IS NULL OR lease_until<=?) AND EXISTS(SELECT 1 FROM temporary_environments e WHERE e.id=? AND e.deployment_id=deployments.id AND e.state NOT IN ('closing','cleanup_blocked','closed') AND e.expires_at>?)`), stamp(now.Add(3*time.Minute)), deployment, stamp(now), environment, stamp(now))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
