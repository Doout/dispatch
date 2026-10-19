package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// EdgeNodeHealthStore keeps agent observations separate from owner-controlled
// configuration and credentials. Older Store implementations may omit it.
type EdgeNodeHealthStore interface {
	UpdateEdgeNodeHealth(context.Context, string, string, map[string]string, time.Time) error
}

func (s *SQLStore) UpdateEdgeNodeHealth(ctx context.Context, id, credentialHash string, details map[string]string, now time.Time) error {
	if credentialHash == "" {
		return ErrEdgeCredential
	}
	patch := map[string]*string{}
	for key, value := range details {
		switch key {
		case "version", "workerDocker", "workerVersion", "workerCheckedAt", "agentArtifactSHA256", "runtimeVersion", "runtimeCheckedAt", "runtimeCapabilities", "credentialMode", "keyFingerprint", "sessionExpiresAt":
			patch[key] = &value
		default:
			return errors.New("unsupported edge health field")
		}
	}
	seen := now.UTC().Format(time.RFC3339Nano)
	patch["lastSeenAt"] = &seen
	if details["credentialMode"] == "short_session" {
		patch["enrollmentExpiresAt"] = nil
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	merge := `json_patch(COALESCE(NULLIF(details,'null'),'{}'),?)`
	if s.postgres {
		merge = `jsonb_strip_nulls(COALESCE(NULLIF(details,'null'),'{}')::jsonb || ?::jsonb)::text`
	}
	// The session check shares the statement with the health write. A heartbeat
	// authenticated before rotation or revocation cannot mark that node ready.
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE private_networks SET details=`+merge+`,state='ready',last_verified_at=?,updated_at=?
		WHERE id=? AND driver='dispatch_agent' AND (
			EXISTS(SELECT 1 FROM edge_node_credentials c WHERE c.network_id=private_networks.id AND c.revoked=FALSE AND c.public_key<>'' AND c.session_hash=? AND c.session_expires_at>?)
			OR (token_hash=? AND NOT EXISTS(SELECT 1 FROM edge_node_credentials c WHERE c.network_id=private_networks.id))
		)`), string(encoded), stamp(now), stamp(now), id, credentialHash, stamp(now), credentialHash)
	return changed(result, err)
}
