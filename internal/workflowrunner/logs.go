package workflowrunner

import (
	"context"
	"errors"
)

type logChunkKey struct{}

func WithLogChunks(ctx context.Context, emit func(string) error) context.Context {
	return context.WithValue(ctx, logChunkKey{}, emit)
}
func PublishLogChunk(ctx context.Context, text string) error {
	emit, ok := ctx.Value(logChunkKey{}).(func(string) error)
	if !ok {
		return errors.New("full log transport is unavailable")
	}
	return emit(text)
}
