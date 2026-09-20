package core

import "time"

// EdgeCredential never crosses the controller API. Only its safe timestamps and
// public-key fingerprint may be exposed as network metadata.
type EdgeCredential struct {
	NetworkID           string
	Generation          int64
	EnrollmentHash      string
	EnrollmentExpiresAt time.Time
	PublicKey           string
	SessionHash         string
	SessionExpiresAt    time.Time
	Revoked             bool
	UpdatedAt           time.Time
}
