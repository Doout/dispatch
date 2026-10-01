package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func SnapshotDigest(snapshot Snapshot) string {
	raw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
