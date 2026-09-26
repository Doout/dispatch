package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
)

var ErrServiceInUse = errors.New("service is in use")
var ErrServiceConflict = errors.New("service was changed; reload and retry")
var ErrDeploymentReviewChanged = errors.New("deployment inputs changed after review; refresh before deploying")

type storedService struct {
	CapturedSecrets map[string]capturedSecret `json:"capturedSecrets,omitempty"`
	Service         core.Service              `json:"service"`
	Credentials     map[string]string         `json:"credentials"`
}

type capturedSecret struct {
	ID     string `json:"id"`
	Cipher string `json:"cipher"`
}

func encodeService(s core.Service) storedService {
	result := storedService{Service: s, Credentials: map[string]string{}, CapturedSecrets: map[string]capturedSecret{}}
	for key, f := range s.Fields {
		if f.CapturedSecretID != "" {
			result.CapturedSecrets[key] = capturedSecret{ID: f.CapturedSecretID, Cipher: f.CapturedSecretValue}
		}
		if f.EncryptedValue != "" {
			result.Credentials[key] = f.EncryptedValue
		}
	}
	return result
}
func decodeService(data string) (core.Service, error) {
	var value storedService
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return core.Service{}, err
	}
	for key, cipher := range value.Credentials {
		f := value.Service.Fields[key]
		f.EncryptedValue = cipher
		value.Service.Fields[key] = f
	}
	for key, secret := range value.CapturedSecrets {
		f := value.Service.Fields[key]
		f.CapturedSecretID = secret.ID
		f.CapturedSecretValue = secret.Cipher
		value.Service.Fields[key] = f
	}
	return value.Service, nil
}
func (s *SQLStore) CreateService(ctx context.Context, item core.Service) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO services(id,project_id,name,revision,payload) VALUES(?,?,?,?,?)`), item.ID, item.ProjectID, item.Name, item.Revision, jsonText(encodeService(item)))
	if err != nil {
		var n int
		if e := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM services WHERE project_id=? AND name=?`), item.ProjectID, item.Name).Scan(&n); e == nil && n > 0 {
			return ErrAlreadyExists
		}
	}
	return err
}
func (s *SQLStore) UpdateService(ctx context.Context, item core.Service, expected int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE services SET revision=?,payload=? WHERE id=? AND project_id=? AND name=? AND revision=?`), item.Revision, jsonText(encodeService(item)), item.ID, item.ProjectID, item.Name, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrServiceConflict
	}
	return err
}
func (s *SQLStore) GetService(ctx context.Context, id string) (core.Service, error) {
	var data string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM services WHERE id=?`), id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Service{}, ErrNotFound
	}
	if err != nil {
		return core.Service{}, err
	}
	return decodeService(data)
}
func (s *SQLStore) ListServices(ctx context.Context, project string) ([]core.Service, error) {
	query := `SELECT payload FROM services`
	args := []any{}
	if project != "" {
		query += ` WHERE project_id=?`
		args = append(args, project)
	}
	query += ` ORDER BY name,id`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Service{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		item, err := decodeService(data)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *SQLStore) serviceLockQuery() string {
	if s.postgres {
		return `SELECT payload FROM services WHERE id=? FOR UPDATE`
	}
	return `SELECT payload FROM services WHERE id=?`
}
func (s *SQLStore) DeleteService(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data string
	if err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), id).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_service_references WHERE service_id=?`), id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrServiceInUse
	}
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM app_service_bindings WHERE service_id=?`), id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrServiceInUse
	}
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployment_service_bindings b JOIN deployments d ON d.id=b.deployment_id WHERE b.service_id=? AND d.state NOT IN ('succeeded','failed','cancelled')`), id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrServiceInUse
	}
	_, err = tx.ExecContext(ctx, s.q(`DELETE FROM services WHERE id=?`), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) GetAppServiceBindings(ctx context.Context, appID string) ([]core.ServiceBinding, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM app_service_bindings WHERE app_id=? ORDER BY alias`), appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ServiceBinding{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var b core.ServiceBinding
		if err = json.Unmarshal([]byte(data), &b); err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}
func (s *SQLStore) ReplaceAppServiceBindings(ctx context.Context, appID string, bindings []core.ServiceBinding) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, build string
	appQuery := `SELECT project_id,build_type FROM apps WHERE id=?`
	if s.postgres {
		appQuery += ` FOR UPDATE`
	}
	if err = tx.QueryRowContext(ctx, s.q(appQuery), appID).Scan(&project, &build); err != nil {
		return err
	}
	services := map[string]core.Service{}
	refs := []string{}
	for _, b := range bindings {
		refs = append(refs, b.ServiceRef)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		var data string
		if err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), ref).Scan(&data); err != nil {
			return fmt.Errorf("service is unavailable")
		}
		item, e := decodeService(data)
		if e != nil {
			return e
		}
		if item.ProjectID != project {
			return errors.New("service is outside the application's project")
		}
		services[ref] = item
	}
	if err = serviceconn.ValidateBindings(bindings, core.BuildType(build), services); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM app_service_bindings WHERE app_id=?`), appID); err != nil {
		return err
	}
	for _, b := range bindings {
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO app_service_bindings(app_id,alias,service_id,payload) VALUES(?,?,?,?)`), appID, b.Alias, b.ServiceRef, jsonText(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type storedCapture struct {
	Binding core.ServiceBinding `json:"binding"`
	Service storedService       `json:"service"`
}

func (s *SQLStore) captureServiceBindings(ctx context.Context, tx *changeTx, d core.Deployment) error {
	// Lock the app before reading its bindings, matching ReplaceAppServiceBindings.
	// Validating the prefetched execution inputs inside this lock prevents a
	// concurrent application edit from mixing old runtime inputs and new bindings.
	if d.Acceptance != nil {
		q := `SELECT id,project_id,server_id,name,source_repo,branch,source_auth_type,source_credential_id,build_type,context_path,
   dockerfile_path,compose_path,compose_content,helm_chart,helm_version,helm_repository,helm_values,helm_namespace,
   helm_release,pre_deploy_hook,post_deploy_hook,container_port,domain,state,created_at,helm_group_values,hook_environment,generated,template,helm_provenance FROM apps WHERE id=?`
		if s.postgres {
			q += ` FOR UPDATE`
		}
		current, err := scanApp(tx.QueryRowContext(ctx, s.q(q), d.AppID))
		if err != nil {
			return err
		}
		if current.Name != d.Acceptance.ExpectedAppName || current.ProjectID != d.Acceptance.ProjectID || current.SpecDigest() != d.Acceptance.AppSpecDigest || current.Name != d.ExecutionAppName || current.Template != d.ExecutionTemplate || current.Generated != d.ExecutionGenerated {
			return ErrDeploymentReviewChanged
		}
	}
	var project, build string
	query := `SELECT project_id,build_type FROM apps WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	if err := tx.QueryRowContext(ctx, s.q(query), d.AppID).Scan(&project, &build); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT payload FROM app_service_bindings WHERE app_id=? ORDER BY service_id,alias`), d.AppID)
	if err != nil {
		return err
	}
	bindings := []core.ServiceBinding{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return err
		}
		var b core.ServiceBinding
		if err = json.Unmarshal([]byte(data), &b); err != nil {
			rows.Close()
			return err
		}
		bindings = append(bindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if d.Acceptance != nil && d.Acceptance.BindingsDigest != "" && core.ServiceBindingConfigurationDigest(bindings) != d.Acceptance.BindingsDigest {
		return ErrDeploymentReviewChanged
	}
	services := map[string]core.Service{}
	for _, b := range bindings {
		var data string
		if err = tx.QueryRowContext(ctx, s.q(s.serviceLockQuery()), b.ServiceRef).Scan(&data); err != nil {
			return errors.New("bound service is unavailable")
		}
		item, e := decodeService(data)
		if e != nil {
			return e
		}
		if item.ProjectID != project {
			return errors.New("service is outside the application's project")
		}
		for key, field := range item.Fields {
			if field.SecretRef == "" {
				continue
			}
			var source, cipher string
			if err = tx.QueryRowContext(ctx, s.q(`SELECT secret_source,encrypted_value FROM secrets WHERE id=?`), field.SecretRef).Scan(&source, &cipher); err != nil {
				return errors.New("service credential is unavailable")
			}
			if source == "" || source == string(core.SecretSourceLocal) {
				field.CapturedSecretID = field.SecretRef
				field.CapturedSecretValue = cipher
				item.Fields[key] = field
			}
		}
		if d.Acceptance != nil && d.Acceptance.ServiceRevisions != nil {
			if expected, ok := d.Acceptance.ServiceRevisions[item.ID]; !ok || expected != item.Revision {
				return ErrDeploymentReviewChanged
			}
		}
		services[item.ID] = item
	}
	if d.Acceptance != nil && d.Acceptance.ServiceRevisions != nil && len(services) != len(d.Acceptance.ServiceRevisions) {
		return ErrDeploymentReviewChanged
	}
	if err = serviceconn.ValidateBindings(bindings, core.BuildType(build), services); err != nil {
		return err
	}
	evidence := []core.AppliedServiceBinding{}
	for _, b := range bindings {
		item := services[b.ServiceRef]
		evidence = append(evidence, core.AppliedServiceBinding{Alias: b.Alias, ServiceID: item.ID, ServiceName: item.Name, Revision: item.Revision})
		if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO deployment_service_bindings(deployment_id,alias,service_id,payload) VALUES(?,?,?,?)`), d.ID, b.Alias, item.ID, jsonText(storedCapture{b, encodeService(item)})); err != nil {
			return err
		}
	}
	if len(bindings) > 0 {
		d.Snapshot.ServiceBindings = evidence
		digest := core.BoundDeploymentSpecDigest(d.SpecDigest, bindings, evidence)
		_, err = tx.ExecContext(ctx, s.q(`UPDATE deployments SET spec_digest=?,spec_snapshot=? WHERE id=?`), digest, jsonText(d.Snapshot), d.ID)
		return err
	}
	return nil
}
func (s *SQLStore) GetDeploymentServiceBindings(ctx context.Context, id string) ([]core.CapturedServiceBinding, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM deployment_service_bindings WHERE deployment_id=? ORDER BY alias`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.CapturedServiceBinding{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var item storedCapture
		if err = json.Unmarshal([]byte(data), &item); err != nil {
			return nil, err
		}
		service, err := decodeService(jsonText(item.Service))
		if err != nil {
			return nil, err
		}
		result = append(result, core.CapturedServiceBinding{Binding: item.Binding, Service: service})
	}
	return result, rows.Err()
}
func (s *SQLStore) ListServiceConsumers(ctx context.Context, id string) ([]core.ServiceConsumer, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT a.id,a.name,b.alias FROM app_service_bindings b JOIN apps a ON a.id=b.app_id WHERE b.service_id=? ORDER BY a.name,b.alias`), id)
	if err != nil {
		return nil, err
	}
	items := []core.ServiceConsumer{}
	for rows.Next() {
		var c core.ServiceConsumer
		if err = rows.Scan(&c.AppID, &c.AppName, &c.Alias); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	service, err := s.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	for i, c := range items {
		items[i].RedeploymentRequired = true
		var deployment string
		err = s.db.QueryRowContext(ctx, s.q(`SELECT id FROM deployments WHERE app_id=? AND state='succeeded' ORDER BY created_at DESC,id DESC LIMIT 1`), c.AppID).Scan(&deployment)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		snapshots, err := s.GetDeploymentServiceBindings(ctx, deployment)
		if err != nil {
			return nil, err
		}
		current, err := s.GetAppServiceBindings(ctx, c.AppID)
		if err != nil {
			return nil, err
		}
		for _, snap := range snapshots {
			if snap.Binding.Alias == c.Alias && snap.Service.ID == id {
				items[i].AppliedRevision = snap.Service.Revision
				for _, binding := range current {
					if binding.Alias == c.Alias {
						items[i].RedeploymentRequired = snap.Service.Revision != service.Revision || !reflect.DeepEqual(binding, snap.Binding)
					}
				}
			}
		}
	}
	return items, nil
}

func (s *SQLStore) updateServiceSecretRevisions(ctx context.Context, tx *changeTx, secretID string) error {
	query := `SELECT payload FROM services ORDER BY id`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	items := []core.Service{}
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		item, e := decodeService(payload)
		if e != nil {
			rows.Close()
			return e
		}
		for _, f := range item.Fields {
			if f.SecretRef == secretID {
				items = append(items, item)
				break
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		item.Revision++
		item.Check = nil
		item.UpdatedAt = time.Now().UTC()
		if _, err = tx.ExecContext(ctx, s.q(`UPDATE services SET revision=?,payload=? WHERE id=?`), item.Revision, jsonText(encodeService(item)), item.ID); err != nil {
			return err
		}
	}
	return nil
}
