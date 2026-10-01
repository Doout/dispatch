package core

import (
	"context"
	"sync"
	"time"
)

// MutationReceipt keeps request identity independently of workload or caller
// deletion. Key and request payloads are represented only by digests.
type MutationReceipt struct {
	ID            string    `json:"id"`
	CallerKind    string    `json:"-"`
	CallerID      string    `json:"-"`
	CredentialID  string    `json:"-"`
	ProjectID     string    `json:"projectId"`
	Action        string    `json:"action"`
	KeyDigest     string    `json:"-"`
	RequestDigest string    `json:"-"`
	OperationKind string    `json:"operationKind"`
	OperationID   string    `json:"operationId"`
	ResourceID    string    `json:"resourceId,omitempty"`
	State         string    `json:"state"`
	Message       string    `json:"message,omitempty"`
	FailureStatus int       `json:"-"`
	ClaimToken    string    `json:"-"`
	ClaimUntil    time.Time `json:"-"`
	RetryUntil    time.Time `json:"retryUntil"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type MutationAcceptance struct {
	ReceiptID     string
	ClaimToken    string
	OperationKind string
	OperationID   string
}
type mutationAcceptanceKey struct{}

func WithMutationAcceptance(ctx context.Context, claim MutationAcceptance) context.Context {
	return context.WithValue(ctx, mutationAcceptanceKey{}, claim)
}
func MutationAcceptanceFromContext(ctx context.Context) (MutationAcceptance, bool) {
	claim, ok := ctx.Value(mutationAcceptanceKey{}).(MutationAcceptance)
	return claim, ok
}

// OperationAudit links middleware audit records to the operation accepted by a
// handler, including a replay that returns the original accepted operation.
type OperationAudit struct {
	mu          sync.Mutex
	operationID string
}
type operationAuditKey struct{}

func WithOperationAudit(ctx context.Context) (context.Context, *OperationAudit) {
	item := &OperationAudit{}
	return context.WithValue(ctx, operationAuditKey{}, item), item
}
func RecordAcceptedOperation(ctx context.Context, id string) {
	if item, ok := ctx.Value(operationAuditKey{}).(*OperationAudit); ok {
		item.mu.Lock()
		item.operationID = id
		item.mu.Unlock()
	}
}
func (item *OperationAudit) OperationID() string {
	item.mu.Lock()
	defer item.mu.Unlock()
	return item.operationID
}
