package api

import (
	"context"

	"github.com/doout/dispatch/internal/core"
)

// Backup admission and execution need current enrollment reads, not credential
// rotation or session management.
type workloadBackupEnrollmentReader interface {
	GetEdgeCredential(context.Context, string) (core.EdgeCredential, error)
}
