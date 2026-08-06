package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrAlreadyExists      = errors.New("already exists")
	ErrEventTriggerActive = errors.New("event trigger has active previews")
	ErrAppActive          = errors.New("application has an active deployment")
	ErrAppPreviewGroup    = errors.New("application belongs to a preview group")
)

type SQLStore struct {
	db       *sql.DB
	postgres bool
}

func Open(ctx context.Context, databaseURL string) (*SQLStore, error) {
	postgres := strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://")
	driver, dsn := "sqlite", databaseURL
	if postgres {
		driver = "pgx"
	} else {
		if databaseURL == "" {
			databaseURL = "dispatch.db"
		}
		dsn = "file:" + databaseURL + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if !postgres {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLStore{db: db, postgres: postgres}, nil
}

func (s *SQLStore) Close() error { return s.db.Close() }

func (s *SQLStore) q(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	index := 1
	for _, r := range query {
		if r == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(index))
			index++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) GetAdminCredential(ctx context.Context) (AdminCredential, error) {
	var credential AdminCredential
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT username,password_hash,created_at FROM admin_credentials WHERE id=1`).
		Scan(&credential.Username, &credential.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return credential, ErrNotFound
	}
	credential.CreatedAt = parseTime(created)
	return credential, err
}

func (s *SQLStore) CreateAdminCredential(ctx context.Context, credential AdminCredential) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO admin_credentials(id,username,password_hash,created_at) VALUES(1,?,?,?)`),
		credential.Username, credential.PasswordHash, stamp(credential.CreatedAt))
	if err == nil {
		return nil
	}
	if _, lookupErr := s.GetAdminCredential(ctx); lookupErr == nil {
		return ErrAlreadyExists
	}
	return err
}

func (s *SQLStore) CreateProject(ctx context.Context, project core.Project) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO projects(id,name,description,created_at) VALUES(?,?,?,?)`),
		project.ID, project.Name, project.Description, stamp(project.CreatedAt))
	return err
}

func (s *SQLStore) UpdateProject(ctx context.Context, project core.Project) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE projects SET name=?,description=? WHERE id=?`),
		project.Name, project.Description, project.ID)
	return changed(result, err)
}

func (s *SQLStore) DeleteProject(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM projects WHERE id=?`), id)
	return changed(result, err)
}

func (s *SQLStore) ListProjects(ctx context.Context) ([]core.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Project{}
	for rows.Next() {
		var item core.Project
		var created string
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) GetProject(ctx context.Context, id string) (core.Project, error) {
	var item core.Project
	var created string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id,name,description,created_at FROM projects WHERE id=?`), id).
		Scan(&item.ID, &item.Name, &item.Description, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	item.CreatedAt = parseTime(created)
	return item, err
}

func (s *SQLStore) CreateServer(ctx context.Context, server core.Server) error {
	kubernetes := kubernetesServerColumns(server)
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO servers(
        id,name,address,runtime,state,agent_mode,kubeconfig_path,kube_context,kube_namespace,kubeconfig_data,kube_ca_data,
        openshift_service_account,openshift_service_account_namespace,openshift_token_secret,openshift_connected_at,created_at)
        VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), server.ID, server.Name, server.Address, server.Runtime, server.State, server.AgentMode,
		kubernetes.KubeconfigPath, kubernetes.Context, kubernetes.Namespace, kubernetes.KubeconfigData,
		kubernetes.CertificateAuthorityData, openShiftServiceAccount(kubernetes), openShiftServiceAccountNamespace(kubernetes),
		openShiftTokenSecret(kubernetes), openShiftConnectedAt(kubernetes), stamp(server.CreatedAt))
	return err
}

func (s *SQLStore) UpdateServer(ctx context.Context, server core.Server) error {
	kubernetes := kubernetesServerColumns(server)
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE servers SET name=?,address=?,runtime=?,state=?,agent_mode=?,
        kubeconfig_path=?,kube_context=?,kube_namespace=?,kubeconfig_data=?,kube_ca_data=?,openshift_service_account=?,
        openshift_service_account_namespace=?,openshift_token_secret=?,openshift_connected_at=? WHERE id=?`), server.Name, server.Address, server.Runtime,
		server.State, server.AgentMode, kubernetes.KubeconfigPath, kubernetes.Context, kubernetes.Namespace,
		kubernetes.KubeconfigData, kubernetes.CertificateAuthorityData, openShiftServiceAccount(kubernetes),
		openShiftServiceAccountNamespace(kubernetes), openShiftTokenSecret(kubernetes), openShiftConnectedAt(kubernetes), server.ID)
	return changed(result, err)
}

func (s *SQLStore) DeleteServer(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM servers WHERE id=?`), id)
	return changed(result, err)
}

func (s *SQLStore) ListServers(ctx context.Context) ([]core.Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,address,runtime,state,agent_mode,
        kubeconfig_path,kube_context,kube_namespace,kubeconfig_data,kube_ca_data,openshift_service_account,
        openshift_service_account_namespace,openshift_token_secret,openshift_connected_at,created_at FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Server{}
	for rows.Next() {
		item, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) GetServer(ctx context.Context, id string) (core.Server, error) {
	item, err := scanServer(s.db.QueryRowContext(ctx, s.q(`SELECT id,name,address,runtime,state,agent_mode,
        kubeconfig_path,kube_context,kube_namespace,kubeconfig_data,kube_ca_data,openshift_service_account,
        openshift_service_account_namespace,openshift_token_secret,openshift_connected_at,created_at FROM servers WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func kubernetesServerColumns(server core.Server) core.KubernetesServerConfig {
	if server.Kubernetes == nil {
		return core.KubernetesServerConfig{}
	}
	return *server.Kubernetes
}

func openShiftServiceAccount(config core.KubernetesServerConfig) string {
	if config.OpenShift == nil {
		return ""
	}
	return config.OpenShift.ServiceAccount
}

func openShiftServiceAccountNamespace(config core.KubernetesServerConfig) string {
	if config.OpenShift == nil {
		return ""
	}
	return config.OpenShift.ServiceAccountNamespace
}

func openShiftTokenSecret(config core.KubernetesServerConfig) string {
	if config.OpenShift == nil {
		return ""
	}
	return config.OpenShift.TokenSecret
}

func openShiftConnectedAt(config core.KubernetesServerConfig) any {
	if config.OpenShift == nil || config.OpenShift.ConnectedAt == nil {
		return nil
	}
	return stamp(*config.OpenShift.ConnectedAt)
}

func scanServer(row scanner) (core.Server, error) {
	var item core.Server
	var created string
	var kubernetes core.KubernetesServerConfig
	var serviceAccount, serviceAccountNamespace, tokenSecret string
	var connected sql.NullString
	err := row.Scan(&item.ID, &item.Name, &item.Address, &item.Runtime, &item.State, &item.AgentMode,
		&kubernetes.KubeconfigPath, &kubernetes.Context, &kubernetes.Namespace, &kubernetes.KubeconfigData,
		&kubernetes.CertificateAuthorityData, &serviceAccount, &serviceAccountNamespace, &tokenSecret, &connected, &created)
	kubernetes.KubeconfigStored = kubernetes.KubeconfigData != ""
	kubernetes.CertificateAuthorityStored = kubernetes.CertificateAuthorityData != ""
	item.CreatedAt = parseTime(created)
	if serviceAccount != "" {
		kubernetes.OpenShift = &core.OpenShiftServerConfig{Managed: true, ServiceAccount: serviceAccount,
			ServiceAccountNamespace: serviceAccountNamespace, TokenSecret: tokenSecret, ConnectedAt: parseNullTime(connected)}
	}
	if core.IsKubernetesRuntime(item.Runtime) || kubernetes.KubeconfigPath != "" || kubernetes.KubeconfigStored {
		item.Kubernetes = &kubernetes
	}
	return item, err
}

func (s *SQLStore) EnsureLocalDockerServer(ctx context.Context) (core.Server, error) {
	servers, err := s.ListServers(ctx)
	if err != nil {
		return core.Server{}, err
	}
	name := "local-docker"
	taken := make(map[string]bool, len(servers))
	for _, server := range servers {
		if !strings.EqualFold(server.Address, "local") {
			taken[strings.ToLower(server.Name)] = true
		}
	}
	if taken[name] {
		const base = "controller-docker"
		name = base
		for suffix := 2; taken[strings.ToLower(name)]; suffix++ {
			name = base + "-" + strconv.Itoa(suffix)
		}
	}
	for _, server := range servers {
		if !strings.EqualFold(server.Address, "local") {
			continue
		}
		server.Name = name
		server.Address = "local"
		server.Runtime = core.ServerRuntimeDocker
		server.State = "ready"
		server.AgentMode = "local"
		server.Kubernetes = nil
		if err := s.UpdateServer(ctx, server); err != nil {
			return core.Server{}, err
		}
		return server, nil
	}
	server := core.Server{ID: newID(), Name: name, Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", AgentMode: "local", CreatedAt: time.Now().UTC()}
	if err := s.CreateServer(ctx, server); err != nil {
		return core.Server{}, err
	}
	return server, nil
}

func (s *SQLStore) ReconcileLocalDockerServer(ctx context.Context, available bool) (*core.Server, error) {
	if available {
		server, err := s.EnsureLocalDockerServer(ctx)
		if err != nil {
			return nil, err
		}
		return &server, nil
	}
	servers, err := s.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		if !strings.EqualFold(server.Address, "local") {
			continue
		}
		server.Runtime = core.ServerRuntimeDocker
		server.State = "unavailable"
		server.AgentMode = "local"
		server.Kubernetes = nil
		if err := s.UpdateServer(ctx, server); err != nil {
			return nil, err
		}
		return &server, nil
	}
	return nil, nil
}

func (s *SQLStore) CreateApp(ctx context.Context, app core.App) error {
	hookEnvironment, _ := json.Marshal(app.HookEnvironment)
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO apps(
        id,project_id,server_id,name,source_repo,branch,build_type,context_path,dockerfile_path,compose_path,
        compose_content,helm_chart,helm_version,helm_repository,helm_values,helm_namespace,helm_release,
        pre_deploy_hook,post_deploy_hook,container_port,domain,state,created_at,helm_group_values,hook_environment,generated,template)
        VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		app.ID, app.ProjectID, app.ServerID, app.Name, app.SourceRepo, app.Branch, string(app.BuildType),
		app.ContextPath, app.DockerfilePath, app.ComposePath, app.ComposeContent, app.HelmChart, app.HelmVersion,
		app.HelmRepository, app.HelmValues, app.HelmNamespace, app.HelmRelease, app.PreDeployHook, app.PostDeployHook,
		app.ContainerPort, app.Domain, app.State, stamp(app.CreatedAt), app.HelmGroupValues, string(hookEnvironment), app.Generated, app.Template)
	return err
}

func (s *SQLStore) UpdateApp(ctx context.Context, app core.App) error {
	hookEnvironment, _ := json.Marshal(app.HookEnvironment)
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE apps SET project_id=?,server_id=?,name=?,source_repo=?,branch=?,build_type=?,
        context_path=?,dockerfile_path=?,compose_path=?,compose_content=?,helm_chart=?,helm_version=?,helm_repository=?,
        helm_values=?,helm_namespace=?,helm_release=?,pre_deploy_hook=?,post_deploy_hook=?,container_port=?,domain=?,state=?,
        helm_group_values=?,hook_environment=?,generated=?,template=? WHERE id=?`),
		app.ProjectID, app.ServerID, app.Name, app.SourceRepo, app.Branch, string(app.BuildType), app.ContextPath, app.DockerfilePath,
		app.ComposePath, app.ComposeContent, app.HelmChart, app.HelmVersion, app.HelmRepository, app.HelmValues, app.HelmNamespace,
		app.HelmRelease, app.PreDeployHook, app.PostDeployHook, app.ContainerPort, app.Domain, app.State,
		app.HelmGroupValues, string(hookEnvironment), app.Generated, app.Template, app.ID)
	return changed(result, err)
}

func (s *SQLStore) AppHasDeployments(ctx context.Context, appID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployments WHERE app_id=?`), appID).Scan(&count)
	return count > 0, err
}

func (s *SQLStore) DeleteApp(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var groupReferences int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM preview_group_components WHERE app_id=?`), id).Scan(&groupReferences); err != nil {
		return err
	}
	if groupReferences > 0 {
		return ErrAppPreviewGroup
	}
	var activePreviews int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM preview_environments
        WHERE template_app_id=? AND state<>'closed'`), id).Scan(&activePreviews); err != nil {
		return err
	}
	if activePreviews > 0 {
		return ErrAppActive
	}
	var active int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployments WHERE app_id=?
        AND state NOT IN ('succeeded','failed','cancelled')`), id).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return ErrAppActive
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM deployment_logs WHERE deployment_id IN
        (SELECT id FROM deployments WHERE app_id=?)`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM deployments WHERE app_id=?`), id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`DELETE FROM apps WHERE id=?`), id)
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) ListApps(ctx context.Context) ([]core.App, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,project_id,server_id,name,source_repo,branch,build_type,context_path,
        dockerfile_path,compose_path,compose_content,helm_chart,helm_version,helm_repository,helm_values,helm_namespace,
        helm_release,pre_deploy_hook,post_deploy_hook,container_port,domain,state,created_at,helm_group_values,hook_environment,generated,template FROM apps
        WHERE state <> 'closed' AND generated=? ORDER BY name`), false)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.App{}
	for rows.Next() {
		item, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) GetApp(ctx context.Context, id string) (core.App, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,project_id,server_id,name,source_repo,branch,build_type,context_path,
        dockerfile_path,compose_path,compose_content,helm_chart,helm_version,helm_repository,helm_values,helm_namespace,
        helm_release,pre_deploy_hook,post_deploy_hook,container_port,domain,state,created_at,helm_group_values,hook_environment,generated,template FROM apps WHERE id=?`), id)
	app, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return app, ErrNotFound
	}
	return app, err
}

type scanner interface{ Scan(...any) error }

func scanApp(row scanner) (core.App, error) {
	var item core.App
	var buildType, created, hookEnvironment string
	err := row.Scan(&item.ID, &item.ProjectID, &item.ServerID, &item.Name, &item.SourceRepo, &item.Branch,
		&buildType, &item.ContextPath, &item.DockerfilePath, &item.ComposePath, &item.ComposeContent, &item.HelmChart,
		&item.HelmVersion, &item.HelmRepository, &item.HelmValues, &item.HelmNamespace, &item.HelmRelease,
		&item.PreDeployHook, &item.PostDeployHook, &item.ContainerPort,
		&item.Domain, &item.State, &created, &item.HelmGroupValues, &hookEnvironment, &item.Generated, &item.Template)
	item.BuildType = core.BuildType(buildType)
	item.CreatedAt = parseTime(created)
	_ = json.Unmarshal([]byte(hookEnvironment), &item.HookEnvironment)
	return item, err
}

func (s *SQLStore) CreateDeployment(ctx context.Context, deployment core.Deployment) error {
	outputs, _ := json.Marshal(deployment.Outputs)
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO deployments(
        id,app_id,commit_sha,spec_digest,state,message,created_at,started_at,finished_at,lease_until,outputs)
        VALUES(?,?,?,?,?,?,?,?,?,?,?)`), deployment.ID, deployment.AppID, deployment.CommitSHA,
		deployment.SpecDigest, string(deployment.State), deployment.Message, stamp(deployment.CreatedAt),
		nullTime(deployment.StartedAt), nullTime(deployment.FinishedAt), nullTime(deployment.LeaseUntil), string(outputs))
	return err
}

func (s *SQLStore) UpdateDeployment(ctx context.Context, deployment core.Deployment) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE deployments SET state=?,message=?,started_at=?,finished_at=?,lease_until=? WHERE id=?`),
		string(deployment.State), deployment.Message, nullTime(deployment.StartedAt), nullTime(deployment.FinishedAt),
		nullTime(deployment.LeaseUntil), deployment.ID)
	return err
}

func (s *SQLStore) UpdateDeploymentOutputs(ctx context.Context, id string, outputs map[string]string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE deployments SET outputs=? WHERE id=?`), jsonText(outputs), id)
	return changed(result, err)
}

func (s *SQLStore) GetDeployment(ctx context.Context, id string) (core.Deployment, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,app_id,commit_sha,spec_digest,state,message,created_at,
        started_at,finished_at,lease_until,outputs FROM deployments WHERE id=?`), id)
	item, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	return s.hydrateDeployment(ctx, item)
}

func (s *SQLStore) ListDeployments(ctx context.Context, limit int) ([]core.Deployment, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,app_id,commit_sha,spec_digest,state,message,created_at,
        started_at,finished_at,lease_until,outputs FROM deployments ORDER BY created_at DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Deployment{}
	for rows.Next() {
		item, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		item, err := s.hydrateDeployment(ctx, items[index])
		if err != nil {
			return nil, err
		}
		items[index] = item
	}
	return items, nil
}

func (s *SQLStore) ActiveDeploymentForApp(ctx context.Context, appID string) (*core.Deployment, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,app_id,commit_sha,spec_digest,state,message,created_at,
        started_at,finished_at,lease_until,outputs FROM deployments WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled')
        ORDER BY created_at DESC LIMIT 1`), appID)
	item, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *SQLStore) hydrateDeployment(ctx context.Context, item core.Deployment) (core.Deployment, error) {
	app, err := s.GetApp(ctx, item.AppID)
	if err != nil {
		return item, err
	}
	server, err := s.GetServer(ctx, app.ServerID)
	if err != nil {
		return item, err
	}
	item.App, item.Server = &app, &server
	return item, nil
}

func scanDeployment(row scanner) (core.Deployment, error) {
	var item core.Deployment
	var state, created string
	var started, finished, lease sql.NullString
	var outputs string
	err := row.Scan(&item.ID, &item.AppID, &item.CommitSHA, &item.SpecDigest, &state, &item.Message,
		&created, &started, &finished, &lease, &outputs)
	item.State = core.DeploymentState(state)
	item.CreatedAt = parseTime(created)
	item.StartedAt = parseNullTime(started)
	item.FinishedAt = parseNullTime(finished)
	item.LeaseUntil = parseNullTime(lease)
	_ = json.Unmarshal([]byte(outputs), &item.Outputs)
	return item, err
}

func (s *SQLStore) AppendDeploymentLog(ctx context.Context, entry core.DeploymentLog) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO deployment_logs(deployment_id,level,message,created_at) VALUES(?,?,?,?)`),
		entry.DeploymentID, entry.Level, entry.Message, stamp(entry.CreatedAt))
	return err
}

func (s *SQLStore) ListDeploymentLogs(ctx context.Context, deploymentID string, after int64) ([]core.DeploymentLog, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,deployment_id,level,message,created_at FROM deployment_logs
        WHERE deployment_id=? AND id>? ORDER BY id LIMIT 500`), deploymentID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.DeploymentLog{}
	for rows.Next() {
		var item core.DeploymentLog
		var created string
		if err := rows.Scan(&item.ID, &item.DeploymentID, &item.Level, &item.Message, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) CreateEventTrigger(ctx context.Context, trigger core.EventTrigger) (core.EventTrigger, bool, error) {
	existing, err := s.eventTriggerForApp(ctx, trigger.AppID, trigger.Provider, trigger.Repository)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return core.EventTrigger{}, false, err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO event_triggers(
        id,app_id,provider,repository,command,enabled,pre_deploy_hook,post_deploy_hook,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`),
		trigger.ID, trigger.AppID, string(trigger.Provider), trigger.Repository, trigger.Command, trigger.Enabled,
		trigger.PreDeployHook, trigger.PostDeployHook, stamp(trigger.CreatedAt), stamp(trigger.UpdatedAt))
	if err == nil {
		return trigger, true, nil
	}
	// A concurrent, identical request is idempotent.
	existing, lookupErr := s.eventTriggerForApp(ctx, trigger.AppID, trigger.Provider, trigger.Repository)
	if lookupErr == nil {
		return existing, false, nil
	}
	return core.EventTrigger{}, false, err
}

func (s *SQLStore) eventTriggerForApp(ctx context.Context, appID string, provider core.EventProvider, repository string) (core.EventTrigger, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,app_id,provider,repository,command,enabled,pre_deploy_hook,post_deploy_hook,created_at,updated_at
        FROM event_triggers WHERE app_id=? AND provider=? AND repository=?`), appID, string(provider), repository)
	item, err := scanEventTrigger(row)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListEventTriggers(ctx context.Context, appID string) ([]core.EventTrigger, error) {
	query := `SELECT id,app_id,provider,repository,command,enabled,pre_deploy_hook,post_deploy_hook,created_at,updated_at FROM event_triggers`
	args := []any{}
	if appID != "" {
		query += ` WHERE app_id=?`
		args = append(args, appID)
	}
	query += ` ORDER BY repository,command`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.EventTrigger{}
	for rows.Next() {
		item, err := scanEventTrigger(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanEventTrigger(row scanner) (core.EventTrigger, error) {
	var item core.EventTrigger
	var provider, created, updated string
	err := row.Scan(&item.ID, &item.AppID, &provider, &item.Repository, &item.Command, &item.Enabled,
		&item.PreDeployHook, &item.PostDeployHook, &created, &updated)
	item.Provider = core.EventProvider(provider)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, err
}

func (s *SQLStore) UpdateEventTrigger(ctx context.Context, trigger core.EventTrigger) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE event_triggers SET command=?,enabled=?,pre_deploy_hook=?,post_deploy_hook=?,updated_at=? WHERE id=?`),
		trigger.Command, trigger.Enabled, trigger.PreDeployHook, trigger.PostDeployHook, stamp(trigger.UpdatedAt), trigger.ID)
	return changed(result, err)
}

func (s *SQLStore) DeleteEventTrigger(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM event_triggers WHERE id=? AND NOT EXISTS (
		SELECT 1 FROM preview_environments WHERE trigger_id=? AND state<>?)`), id, id, string(core.PreviewClosed))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM event_triggers WHERE id=?`), id).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrEventTriggerActive
	}
	return ErrNotFound
}

func (s *SQLStore) IncomingEventExists(ctx context.Context, provider core.EventProvider, deliveryID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM incoming_events WHERE provider=? AND delivery_id=?`),
		string(provider), deliveryID).Scan(&count)
	return count > 0, err
}

func (s *SQLStore) HasEventTrigger(ctx context.Context, provider core.EventProvider, repository, command string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM event_triggers
        WHERE provider=? AND repository=? AND command=? AND enabled=?`), string(provider), repository, command, true).Scan(&count)
	return count > 0, err
}

// ProcessIncomingEvent records a delivery and applies its preview lifecycle intent
// in one transaction. The provider delivery ID makes retries safe.
func (s *SQLStore) ProcessIncomingEvent(ctx context.Context, event core.IncomingEvent) (core.EventResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.EventResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	insertResult, err := tx.ExecContext(ctx, s.q(`INSERT INTO incoming_events(
		id,provider,delivery_id,kind,action,repository,pull_request_number,head_ref,head_sha,base_ref,actor,
		actor_association,trusted_actor,command,arguments,source_comment_id,received_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(provider,delivery_id) DO NOTHING`),
		event.ID, string(event.Provider), event.DeliveryID, string(event.Kind), event.Action, event.Repository,
		event.PullRequestNumber, event.HeadRef, event.HeadSHA, event.BaseRef, event.Actor, event.ActorAssociation, event.TrustedActor, event.Command,
		event.Arguments, event.SourceCommentID, stamp(event.ReceivedAt))
	if err != nil {
		return core.EventResult{}, err
	}
	inserted, err := insertResult.RowsAffected()
	if err != nil {
		return core.EventResult{}, err
	}
	if inserted == 0 {
		previews, lookupErr := listPreviewEnvironments(ctx, tx, s.q, "", event.Provider, event.Repository, event.PullRequestNumber)
		if lookupErr != nil {
			return core.EventResult{}, lookupErr
		}
		return core.EventResult{Event: event, Duplicate: true, Previews: previews}, nil
	}

	result := core.EventResult{Event: event, Previews: []core.PreviewEnvironment{}}
	switch {
	case event.Kind == core.EventKindPullRequestComment && event.Action == "created" && event.Command != "" && event.TrustedActor:
		var closed int
		if queryErr := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM incoming_events
			WHERE provider=? AND repository=? AND pull_request_number=? AND kind=? AND action=?`),
			string(event.Provider), event.Repository, event.PullRequestNumber, string(core.EventKindPullRequest), "closed").Scan(&closed); queryErr != nil {
			return core.EventResult{}, queryErr
		}
		if closed > 0 {
			result.Ignored = true
			break
		}
		rows, queryErr := tx.QueryContext(ctx, s.q(`SELECT id,app_id,provider,repository,command,enabled,pre_deploy_hook,post_deploy_hook,created_at,updated_at
            FROM event_triggers WHERE provider=? AND repository=? AND command=? AND enabled=? ORDER BY id`),
			string(event.Provider), event.Repository, event.Command, true)
		if queryErr != nil {
			return core.EventResult{}, queryErr
		}
		triggers := []core.EventTrigger{}
		for rows.Next() {
			trigger, scanErr := scanEventTrigger(rows)
			if scanErr != nil {
				_ = rows.Close()
				return core.EventResult{}, scanErr
			}
			triggers = append(triggers, trigger)
		}
		if err := rows.Close(); err != nil {
			return core.EventResult{}, err
		}
		for _, trigger := range triggers {
			preview := core.PreviewEnvironment{
				ID: newID(), TriggerID: trigger.ID, TemplateAppID: trigger.AppID, Provider: event.Provider,
				Repository: event.Repository, PullRequestNumber: event.PullRequestNumber, HeadRef: event.HeadRef,
				HeadSHA: event.HeadSHA, BaseRef: event.BaseRef, DeliveryID: event.DeliveryID,
				SourceCommentID: event.SourceCommentID, TriggeredBy: event.Actor, State: core.PreviewRequested,
				PreDeployHook: trigger.PreDeployHook, PostDeployHook: trigger.PostDeployHook, HookEnvironment: event.HookEnvironment(),
				Message: "Preview requested", CreatedAt: event.ReceivedAt, UpdatedAt: event.ReceivedAt,
			}
			_, insertErr := tx.ExecContext(ctx, s.q(`INSERT INTO preview_environments(
				id,trigger_id,template_app_id,app_id,provider,repository,pull_request_number,head_ref,head_sha,base_ref,delivery_id,
				source_comment_id,status_comment_id,url,deployment_id,triggered_by,state,message,pre_deploy_hook,post_deploy_hook,
				hook_environment,created_at,updated_at,closed_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(trigger_id,pull_request_number) DO NOTHING`),
				preview.ID, preview.TriggerID, preview.TemplateAppID, nil, string(preview.Provider), preview.Repository,
				preview.PullRequestNumber, preview.HeadRef, preview.HeadSHA, preview.BaseRef, preview.DeliveryID,
				preview.SourceCommentID, "", "", "", preview.TriggeredBy, string(preview.State), preview.Message,
				preview.PreDeployHook, preview.PostDeployHook, jsonText(preview.HookEnvironment), stamp(preview.CreatedAt), stamp(preview.UpdatedAt), nil)
			if insertErr != nil {
				return core.EventResult{}, insertErr
			}
			stored, lookupErr := previewForTrigger(ctx, tx, s.q, trigger.ID, event.PullRequestNumber)
			if lookupErr != nil {
				return core.EventResult{}, lookupErr
			}
			result.Previews = append(result.Previews, stored)
		}
	case event.Kind == core.EventKindPullRequest && event.Action == "closed":
		previews, queryErr := listPreviewEnvironments(ctx, tx, s.q, "", event.Provider, event.Repository, event.PullRequestNumber)
		if queryErr != nil {
			return core.EventResult{}, queryErr
		}
		for index := range previews {
			if previews[index].State != core.PreviewClosed {
				previews[index].State = core.PreviewCleanupRequested
				previews[index].Message = "Pull request closed; cleanup requested"
				previews[index].DeliveryID = event.DeliveryID
				previews[index].UpdatedAt = event.ReceivedAt
				previews[index].ClosedAt = &event.ReceivedAt
				if event.HeadRef != "" {
					previews[index].HeadRef = event.HeadRef
				}
				if event.HeadSHA != "" {
					previews[index].HeadSHA = event.HeadSHA
				}
				if updateErr := updatePreviewEnvironment(ctx, tx, s.q, previews[index]); updateErr != nil {
					return core.EventResult{}, updateErr
				}
			}
		}
		result.Previews = previews
	default:
		result.Ignored = true
	}
	if len(result.Previews) == 0 {
		result.Ignored = true
	}
	if err := tx.Commit(); err != nil {
		return core.EventResult{}, err
	}
	return result, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func previewForTrigger(ctx context.Context, queryer queryer, q func(string) string, triggerID string, number int) (core.PreviewEnvironment, error) {
	row := queryer.QueryRowContext(ctx, q(previewSelect+` WHERE trigger_id=? AND pull_request_number=?`), triggerID, number)
	return scanPreviewEnvironment(row)
}

const previewSelect = `SELECT id,trigger_id,template_app_id,COALESCE(app_id,''),provider,repository,pull_request_number,head_ref,head_sha,base_ref,
    delivery_id,source_comment_id,status_comment_id,url,deployment_id,triggered_by,state,message,pre_deploy_hook,post_deploy_hook,
    hook_environment,created_at,updated_at,closed_at
    FROM preview_environments`

func listPreviewEnvironments(ctx context.Context, queryer queryer, q func(string) string, appID string, provider core.EventProvider, repository string, number int) ([]core.PreviewEnvironment, error) {
	query := previewSelect + ` WHERE 1=1`
	args := []any{}
	if appID != "" {
		query += ` AND (template_app_id=? OR app_id=?)`
		args = append(args, appID, appID)
	}
	if provider != "" {
		query += ` AND provider=?`
		args = append(args, string(provider))
	}
	if repository != "" {
		query += ` AND repository=?`
		args = append(args, repository)
	}
	if number > 0 {
		query += ` AND pull_request_number=?`
		args = append(args, number)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := queryer.QueryContext(ctx, q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PreviewEnvironment{}
	for rows.Next() {
		item, err := scanPreviewEnvironment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanPreviewEnvironment(row scanner) (core.PreviewEnvironment, error) {
	var item core.PreviewEnvironment
	var provider, state, environment, created, updated string
	var closed sql.NullString
	err := row.Scan(&item.ID, &item.TriggerID, &item.TemplateAppID, &item.AppID, &provider, &item.Repository, &item.PullRequestNumber,
		&item.HeadRef, &item.HeadSHA, &item.BaseRef, &item.DeliveryID, &item.SourceCommentID, &item.StatusCommentID,
		&item.URL, &item.DeploymentID, &item.TriggeredBy, &state, &item.Message, &item.PreDeployHook, &item.PostDeployHook,
		&environment, &created, &updated, &closed)
	item.Provider = core.EventProvider(provider)
	item.State = core.PreviewState(state)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	item.ClosedAt = parseNullTime(closed)
	_ = json.Unmarshal([]byte(environment), &item.HookEnvironment)
	return item, err
}

func (s *SQLStore) ListPreviewEnvironments(ctx context.Context, appID string) ([]core.PreviewEnvironment, error) {
	return listPreviewEnvironments(ctx, s.db, s.q, appID, "", "", 0)
}

func (s *SQLStore) GetPreviewEnvironment(ctx context.Context, id string) (core.PreviewEnvironment, error) {
	item, err := scanPreviewEnvironment(s.db.QueryRowContext(ctx, s.q(previewSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func updatePreviewEnvironment(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, q func(string) string, item core.PreviewEnvironment) error {
	result, err := executor.ExecContext(ctx, q(`UPDATE preview_environments SET app_id=NULLIF(?,''),head_ref=?,head_sha=?,base_ref=?,delivery_id=?,
        source_comment_id=?,status_comment_id=?,url=?,deployment_id=?,triggered_by=?,state=?,message=?,updated_at=?,closed_at=? WHERE id=?`),
		item.AppID, item.HeadRef, item.HeadSHA, item.BaseRef, item.DeliveryID, item.SourceCommentID, item.StatusCommentID,
		item.URL, item.DeploymentID, item.TriggeredBy, string(item.State), item.Message, stamp(item.UpdatedAt),
		nullTime(item.ClosedAt), item.ID)
	return changed(result, err)
}

func (s *SQLStore) UpdatePreviewEnvironment(ctx context.Context, item core.PreviewEnvironment) error {
	return updatePreviewEnvironment(ctx, s.db, s.q, item)
}

func (s *SQLStore) TransitionPreviewEnvironment(ctx context.Context, item core.PreviewEnvironment, expected ...core.PreviewState) (bool, error) {
	if len(expected) == 0 {
		return false, errors.New("preview transition requires an expected state")
	}
	query := `UPDATE preview_environments SET app_id=NULLIF(?,''),head_ref=?,head_sha=?,base_ref=?,delivery_id=?,
        source_comment_id=?,status_comment_id=?,url=?,deployment_id=?,triggered_by=?,state=?,message=?,updated_at=?,closed_at=?
        WHERE id=? AND state IN (`
	args := []any{item.AppID, item.HeadRef, item.HeadSHA, item.BaseRef, item.DeliveryID, item.SourceCommentID,
		item.StatusCommentID, item.URL, item.DeploymentID, item.TriggeredBy, string(item.State), item.Message,
		stamp(item.UpdatedAt), nullTime(item.ClosedAt), item.ID}
	for index, state := range expected {
		if index > 0 {
			query += `,`
		}
		query += `?`
		args = append(args, string(state))
	}
	query += `)`
	result, err := s.db.ExecContext(ctx, s.q(query), args...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *SQLStore) SeedDemo(ctx context.Context) error {
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) > 0 {
		return err
	}
	now := time.Now().UTC()
	project := core.Project{ID: newID(), Name: "Dispatch demo", Description: "Local demonstration data", CreatedAt: now.Add(-24 * time.Hour)}
	server := core.Server{ID: newID(), Name: "local-docker", Address: "local", Runtime: "docker", State: "ready", AgentMode: "local", CreatedAt: now.Add(-23 * time.Hour)}
	if err := s.CreateProject(ctx, project); err != nil {
		return err
	}
	if err := s.CreateServer(ctx, server); err != nil {
		return err
	}
	apps := []core.App{
		{ID: newID(), ProjectID: project.ID, ServerID: server.ID, Name: "checkout-api", SourceRepo: "github.com/doout/checkout-api", Branch: "main", BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile", ContainerPort: 8080, Domain: "checkout.demo.internal", State: "attention", CreatedAt: now.Add(-22 * time.Hour)},
		{ID: newID(), ProjectID: project.ID, ServerID: server.ID, Name: "catalog-web", SourceRepo: "github.com/doout/catalog-web", Branch: "main", BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile", ContainerPort: 3000, Domain: "catalog.demo.internal", State: "deploying", CreatedAt: now.Add(-20 * time.Hour)},
		{ID: newID(), ProjectID: project.ID, ServerID: server.ID, Name: "payments-worker", SourceRepo: "github.com/doout/payments-worker", Branch: "main", BuildType: core.BuildTypeCompose, ContextPath: ".", ComposePath: "compose.yml", State: "live", CreatedAt: now.Add(-18 * time.Hour)},
	}
	for _, app := range apps {
		if err := s.CreateApp(ctx, app); err != nil {
			return err
		}
	}
	states := []core.DeploymentState{core.DeploymentFailed, core.DeploymentChecking, core.DeploymentSucceeded}
	messages := []string{"Health check returned 503", "Readiness check 2 of 3", "Deployment is live"}
	for index, app := range apps {
		started := now.Add(time.Duration(-8+index*3) * time.Minute)
		finished := started.Add(2 * time.Minute)
		deployment := core.Deployment{ID: newID(), AppID: app.ID, CommitSHA: []string{"a1b2c3d4", "f7e9d1a0", "9c8b7a6d"}[index], SpecDigest: app.SpecDigest(), State: states[index], Message: messages[index], CreatedAt: started, StartedAt: &started}
		if states[index].Terminal() {
			deployment.FinishedAt = &finished
		}
		if err := s.CreateDeployment(ctx, deployment); err != nil {
			return err
		}
		for _, message := range []string{"Source revision resolved", "Application specification verified", messages[index]} {
			if err := s.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: deployment.ID, Level: "info", Message: message, CreatedAt: started}); err != nil {
				return err
			}
		}
	}
	return nil
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return stamp(*value)
}
func parseNullTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := parseTime(value.String)
	return &parsed
}
func newID() string { return ulid.Make().String() }
