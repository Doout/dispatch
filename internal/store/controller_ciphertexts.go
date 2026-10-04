package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
)

// These scopes match the records' encryption boundaries. In particular, copied
// runtime artifacts retain scope_id, and captured secret values retain secret ID.
type controllerCipherSource struct {
	table, column, prefix, suffix string
	identity                      []string
}

var controllerCipherSources = []controllerCipherSource{
	{"secrets", "encrypted_value", "secret:", "", []string{"id"}},
	{"secret_stores", "encrypted_credentials", "secret-store:", "", []string{"id"}},
	{"servers", "relay_access_token", "relay-server:", ":access-token", []string{"id"}},
	{"github_apps", "encrypted_private_key", "github-app:", ":private-key", []string{"id"}},
	{"github_apps", "encrypted_webhook_secret", "github-app:", ":webhook-secret", []string{"id"}},
	{"auth_providers", "encrypted_client_secret", "auth-provider:", "", []string{"id"}},
	{"private_networks", "encrypted_credentials", "laneway-network:", "", []string{"id"}},
	{"laneway_applications", "encrypted_client_secret", "laneway-application:", "", []string{"id"}},
	{"laneway_authorization_transactions", "encrypted_code_verifier", "laneway-authorization:", "", []string{"state_hash"}},
	{"edge_jobs", "encrypted_request", "edge-job:", ":request", []string{"id"}},
	{"edge_jobs", "encrypted_response", "edge-job:", ":response", []string{"id"}},
	{"deployment_drift_baselines", "ciphertext", "deployment-drift:", "", []string{"deployment_id"}},
	{"application_observation_configs", "webhook_ciphertext", "observation-webhook:", "", []string{"app_id"}},
	{"deployment_runtime_artifacts", "ciphertext", "deployment-runtime:", "", []string{"scope_id", "app_id", "server_id"}},
	{"runtime_jobs", "encrypted_request", "runtime-job:", ":request", []string{"id"}},
	{"runtime_jobs", "encrypted_result", "runtime-job:", ":result", []string{"id"}},
	{"infrastructure_reviews", "encrypted_request", "infrastructure-review:", "", []string{"id"}},
	{"infrastructure_snapshot_reviews", "encrypted_request", "snapshot-review:", "", []string{"id"}},
	{"infrastructure_action_requests", "encrypted_request", "infrastructure-action:", "", []string{"operation_id"}},
	{"target_bootstraps", "encrypted_input", "target-bootstrap:", "", []string{"id"}},
	{"service_resources", "request_cipher", "service-resource:", ":request", []string{"run_id"}},
	{"service_resources", "outputs_cipher", "service-resource:", ":outputs", []string{"run_id"}},
	{"workload_backups", "input_cipher", "workload-backup:", ":accepted", []string{"id"}},
	{"workload_backup_operations", "input_cipher", "workload-backup:", ":operation", []string{"id"}},
	{"workload_backup_policies", "input_cipher", "workload-backup:", ":policy", []string{"id"}},
	{"webhook_deliveries", "ciphertext", "webhook-delivery:", "", []string{"id"}},
}

// VisitControllerCiphertexts streams controller ciphertext with its associated
// data. Restore verification uses this on an isolated database, without API list
// limits or active-state filters that would omit retained recovery material.
// Empty values were never configured or have been deliberately retired.
func (s *SQLStore) VisitControllerCiphertexts(ctx context.Context, visit func(scope, ciphertext string) error) error {
	for _, source := range controllerCipherSources {
		query := `SELECT ` + strings.Join(source.identity, ",") + `,` + source.column + ` FROM ` + source.table + ` WHERE ` + source.column + `<>''`
		if err := s.visitControllerCipherQuery(ctx, query, func(values []string) error {
			scope := source.prefix + strings.Join(values[:len(values)-1], ":") + source.suffix
			return visit(scope, values[len(values)-1])
		}, len(source.identity)+1); err != nil {
			return err
		}
	}
	if err := s.visitControllerCipherQuery(ctx, `SELECT id,payload FROM services`, func(values []string) error {
		var item storedService
		if err := json.Unmarshal([]byte(values[1]), &item); err != nil {
			return errors.New("saved service credentials cannot be read")
		}
		return visitStoredServiceCiphertexts(values[0], item, visit)
	}, 2); err != nil {
		return err
	}
	if err := s.visitControllerCipherQuery(ctx, `SELECT service_id,payload FROM deployment_service_bindings`, func(values []string) error {
		var item storedCapture
		if err := json.Unmarshal([]byte(values[1]), &item); err != nil {
			return errors.New("captured service credentials cannot be read")
		}
		return visitStoredServiceCiphertexts(values[0], item.Service, visit)
	}, 2); err != nil {
		return err
	}
	for _, table := range []string{"apps", "preview_environments", "preview_group_runs"} {
		if err := s.visitControllerCipherQuery(ctx, `SELECT hook_environment FROM `+table, func(values []string) error {
			var environment map[string]string
			if err := json.Unmarshal([]byte(values[0]), &environment); err != nil {
				return errors.New("saved hook environment cannot be read")
			}
			for key, cipher := range environment {
				if id, _, secret := core.ParseSecretEnvironmentKey(key); secret && cipher != "" {
					if err := visit("secret:"+id, cipher); err != nil {
						return err
					}
				}
			}
			return nil
		}, 1); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) visitControllerCipherQuery(ctx context.Context, query string, visit func([]string) error, columns int) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	values, targets := make([]string, columns), make([]any, columns)
	for i := range values {
		targets[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		if err := visit(values); err != nil {
			return err
		}
	}
	return rows.Err()
}

func visitStoredServiceCiphertexts(id string, item storedService, visit func(string, string) error) error {
	for field, cipher := range item.Credentials {
		if cipher != "" {
			if err := visit(serviceconn.FieldAAD(id, field), cipher); err != nil {
				return err
			}
		}
	}
	for _, secret := range item.CapturedSecrets {
		if secret.Cipher != "" {
			if err := visit("secret:"+secret.ID, secret.Cipher); err != nil {
				return err
			}
		}
	}
	return nil
}
