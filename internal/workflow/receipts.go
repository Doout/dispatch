package workflow

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func (s *Service) receivedRevision(ctx context.Context, resourceID string) (core.WorkflowRevision, bool, error) {
	receipt := store.WorkflowReceiptFromContext(ctx)
	if receipt.Key == "" {
		return core.WorkflowRevision{}, false, nil
	}
	r, err := s.Store.WorkflowReceiptRevision(ctx, resourceID, receipt.Key)
	if errors.Is(err, store.ErrNotFound) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (s *Service) receivedNoChange(ctx context.Context, resourceID string) (core.WorkflowRevision, error) {
	if receipt := store.WorkflowReceiptFromContext(ctx); receipt.Key != "" {
		return core.WorkflowRevision{}, s.Store.RecordWorkflowNoChange(ctx, resourceID, receipt.Key)
	}
	return core.WorkflowRevision{}, nil
}
