package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type PreviewSourceTrustStore interface {
	UpdatePreviewSourceTrustDecision(context.Context, string, *core.PreviewSourceTrustDecision) error
	CreatePreviewSourceTrustApproval(context.Context, core.PreviewSourceTrustApproval) error
	ListPreviewSourceTrustApprovals(context.Context, string) ([]core.PreviewSourceTrustApproval, error)
	RevokePreviewSourceTrustApproval(context.Context, string, string) error
}

func (s *SQLStore) CreatePreviewSourceTrustApproval(ctx context.Context, a core.PreviewSourceTrustApproval) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO preview_source_trust_approvals(id,resource_id,digest,actor_id,expires_at,created_at) VALUES(?,?,?,?,?,?)`), a.ID, a.ResourceID, a.Digest, a.ActorID, stamp(a.ExpiresAt), stamp(a.CreatedAt))
	return err
}
func (s *SQLStore) ListPreviewSourceTrustApprovals(ctx context.Context, resourceID string) ([]core.PreviewSourceTrustApproval, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,resource_id,digest,actor_id,expires_at,created_at,revoked_at FROM preview_source_trust_approvals WHERE resource_id=? ORDER BY created_at DESC`), resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.PreviewSourceTrustApproval{}
	for rows.Next() {
		var a core.PreviewSourceTrustApproval
		var expires, created string
		var revoked sql.NullString
		if err := rows.Scan(&a.ID, &a.ResourceID, &a.Digest, &a.ActorID, &expires, &created, &revoked); err != nil {
			return nil, err
		}
		a.ExpiresAt, a.CreatedAt, a.RevokedAt = parseTime(expires), parseTime(created), parseNullTime(revoked)
		result = append(result, a)
	}
	return result, rows.Err()
}
func (s *SQLStore) RevokePreviewSourceTrustApproval(ctx context.Context, resourceID, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE preview_source_trust_approvals SET revoked_at=? WHERE resource_id=? AND id=?`), stamp(time.Now().UTC()), resourceID, id)
	return changed(result, err)
}

func (s *SQLStore) UpdatePreviewSourceTrustDecision(ctx context.Context, id string, decision *core.PreviewSourceTrustDecision) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_revisions SET source_trust=? WHERE id=?`), jsonText(decision), id)
	return changed(result, err)
}
