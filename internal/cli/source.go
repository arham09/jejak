package cli

import (
	"context"

	"github.com/arham09/jejak/internal/git"
)

// gitSourceReader keeps committed brief source reads anchored to immutable Git
// blobs. Working-tree commands use overlay.View, which implements the same
// reader contract for captured synthetic blobs.
type gitSourceReader struct {
	client *git.Client
}

func (r gitSourceReader) ReadBlob(ctx context.Context, root, objectID string) ([]byte, error) {
	return r.client.ReadBlob(ctx, root, objectID)
}
