package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) CreateServiceProvisionRun(ctx context.Context, run core.ServiceProvisionRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO service_provision_runs(id,template_id,project_id,service_name,state,payload) VALUES(?,?,?,?,?,?)`), run.ID, run.TemplateID, run.ProjectID, run.ServiceName, run.State, jsonText(run))
	if err != nil {
		return err
	}
	if err = s.BindMutationAcceptance(ctx, tx.Tx, "service_provision", run.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) UpdateServiceProvisionRun(ctx context.Context, run core.ServiceProvisionRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE service_provision_runs SET state=?,payload=? WHERE id=?`), run.State, jsonText(run), run.ID)
	if err = changed(result, err); err != nil {
		return err
	}
	if run.State == "failed" || run.State == "succeeded" {
		if err = s.UpdateMutationOutcome(ctx, tx.Tx, "service_provision", run.ID, run.State); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLStore) GetServiceProvisionRun(ctx context.Context, id string) (core.ServiceProvisionRun, error) {
	var data string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM service_provision_runs WHERE id=?`), id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ServiceProvisionRun{}, ErrNotFound
	}
	if err != nil {
		return core.ServiceProvisionRun{}, err
	}
	var run core.ServiceProvisionRun
	err = json.Unmarshal([]byte(data), &run)
	return run, err
}

func (s *SQLStore) ListServiceProvisionRuns(ctx context.Context, projectID string) ([]core.ServiceProvisionRun, error) {
	query := `SELECT payload FROM service_provision_runs`
	args := []any{}
	if projectID != "" {
		query += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.ServiceProvisionRun{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var run core.ServiceProvisionRun
		if err := json.Unmarshal([]byte(data), &run); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

// Provisioning may have side effects outside Dispatch. Never replay an
// interrupted run automatically; its operator can inspect the external system.
func (s *SQLStore) RecoverInterruptedServiceProvisionRuns(ctx context.Context) error {
	runs, err := s.ListServiceProvisionRuns(ctx, "")
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.State != "queued" && run.State != "running" {
			continue
		}
		now := time.Now().UTC()
		services, err := s.ListServices(ctx, run.ProjectID)
		if err != nil {
			return err
		}
		for _, service := range services {
			if service.ProvisionRunID == run.ID {
				run.State, run.Phase, run.ServiceID, run.FinishedAt = "succeeded", "Ready", service.ID, &now
				break
			}
		}
		if run.State == "succeeded" {
			if err := s.UpdateServiceProvisionRun(ctx, run); err != nil {
				return err
			}
			continue
		}
		run.State, run.Error, run.FinishedAt = "failed", "Controller stopped before provisioning finished; check the provider before retrying.", &now
		if _, e := s.GetServiceResource(ctx, run.ID); e == nil {
			run.Phase = "Recovery required"
			run.Error = "Controller stopped during an owned service operation. Inspect and reconcile this run; its original credentials are retained."
		}
		if err := s.UpdateServiceProvisionRun(ctx, run); err != nil {
			return err
		}
	}
	return nil
}
