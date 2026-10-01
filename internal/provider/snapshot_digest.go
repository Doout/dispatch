package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func SnapshotDigest(snapshot Snapshot) string {
	// Provider-added inventory labels do not change captured content. Dispatch
	// verifies its ownership labels separately before trusting this digest.
	snapshot.Labels = nil
	raw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
