package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

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

func (s *SQLStore) CreateProject(ctx context.Context, project core.Project) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO projects(id,name,description,created_at) VALUES(?,?,?,?)`),
		project.ID, project.Name, project.Description, stamp(project.CreatedAt))
	return err
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
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO servers(id,name,address,runtime,state,agent_mode,created_at) VALUES(?,?,?,?,?,?,?)`),
		server.ID, server.Name, server.Address, server.Runtime, server.State, server.AgentMode, stamp(server.CreatedAt))
	return err
}

func (s *SQLStore) ListServers(ctx context.Context) ([]core.Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,address,runtime,state,agent_mode,created_at FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Server{}
	for rows.Next() {
		var item core.Server
		var created string
		if err := rows.Scan(&item.ID, &item.Name, &item.Address, &item.Runtime, &item.State, &item.AgentMode, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) GetServer(ctx context.Context, id string) (core.Server, error) {
	var item core.Server
	var created string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id,name,address,runtime,state,agent_mode,created_at FROM servers WHERE id=?`), id).
		Scan(&item.ID, &item.Name, &item.Address, &item.Runtime, &item.State, &item.AgentMode, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	item.CreatedAt = parseTime(created)
	return item, err
}

func (s *SQLStore) CreateApp(ctx context.Context, app core.App) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO apps(
        id,project_id,server_id,name,source_repo,branch,build_type,context_path,dockerfile_path,compose_path,
        container_port,domain,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		app.ID, app.ProjectID, app.ServerID, app.Name, app.SourceRepo, app.Branch, string(app.BuildType),
		app.ContextPath, app.DockerfilePath, app.ComposePath, app.ContainerPort, app.Domain, app.State, stamp(app.CreatedAt))
	return err
}

func (s *SQLStore) ListApps(ctx context.Context) ([]core.App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,server_id,name,source_repo,branch,build_type,context_path,
        dockerfile_path,compose_path,container_port,domain,state,created_at FROM apps ORDER BY name`)
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
        dockerfile_path,compose_path,container_port,domain,state,created_at FROM apps WHERE id=?`), id)
	app, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return app, ErrNotFound
	}
	return app, err
}

type scanner interface{ Scan(...any) error }

func scanApp(row scanner) (core.App, error) {
	var item core.App
	var buildType, created string
	err := row.Scan(&item.ID, &item.ProjectID, &item.ServerID, &item.Name, &item.SourceRepo, &item.Branch,
		&buildType, &item.ContextPath, &item.DockerfilePath, &item.ComposePath, &item.ContainerPort,
		&item.Domain, &item.State, &created)
	item.BuildType = core.BuildType(buildType)
	item.CreatedAt = parseTime(created)
	return item, err
}

func (s *SQLStore) CreateDeployment(ctx context.Context, deployment core.Deployment) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO deployments(
        id,app_id,commit_sha,spec_digest,state,message,created_at,started_at,finished_at,lease_until)
        VALUES(?,?,?,?,?,?,?,?,?,?)`), deployment.ID, deployment.AppID, deployment.CommitSHA,
		deployment.SpecDigest, string(deployment.State), deployment.Message, stamp(deployment.CreatedAt),
		nullTime(deployment.StartedAt), nullTime(deployment.FinishedAt), nullTime(deployment.LeaseUntil))
	return err
}

func (s *SQLStore) UpdateDeployment(ctx context.Context, deployment core.Deployment) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE deployments SET state=?,message=?,started_at=?,finished_at=?,lease_until=? WHERE id=?`),
		string(deployment.State), deployment.Message, nullTime(deployment.StartedAt), nullTime(deployment.FinishedAt),
		nullTime(deployment.LeaseUntil), deployment.ID)
	return err
}

func (s *SQLStore) GetDeployment(ctx context.Context, id string) (core.Deployment, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT id,app_id,commit_sha,spec_digest,state,message,created_at,
        started_at,finished_at,lease_until FROM deployments WHERE id=?`), id)
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
        started_at,finished_at,lease_until FROM deployments ORDER BY created_at DESC LIMIT ?`), limit)
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
        started_at,finished_at,lease_until FROM deployments WHERE app_id=? AND state NOT IN ('succeeded','failed','cancelled')
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
	err := row.Scan(&item.ID, &item.AppID, &item.CommitSHA, &item.SpecDigest, &state, &item.Message,
		&created, &started, &finished, &lease)
	item.State = core.DeploymentState(state)
	item.CreatedAt = parseTime(created)
	item.StartedAt = parseNullTime(started)
	item.FinishedAt = parseNullTime(finished)
	item.LeaseUntil = parseNullTime(lease)
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

func (s *SQLStore) SeedDemo(ctx context.Context) error {
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) > 0 {
		return err
	}
	now := time.Now().UTC()
	project := core.Project{ID: newID(), Name: "Dispatch demo", Description: "Local demonstration data", CreatedAt: now.Add(-24 * time.Hour)}
	server := core.Server{ID: newID(), Name: "local-docker", Address: "local", Runtime: "docker", State: "ready", AgentMode: "simulation", CreatedAt: now.Add(-23 * time.Hour)}
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
