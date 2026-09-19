package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ReadBlob reads an immutable Git blob object from root. It never consults the
// working-tree file at a matching path.
func (c *Client) ReadBlob(ctx context.Context, root, objectID string) ([]byte, error) {
	if strings.TrimSpace(objectID) == "" {
		return nil, fmt.Errorf("git blob object ID is empty")
	}
	return c.object(ctx, root, objectID)
}

// CatFile is an explicit alias for callers that use Git's command vocabulary.
func (c *Client) CatFile(ctx context.Context, root, objectID string) ([]byte, error) {
	return c.ReadBlob(ctx, root, objectID)
}

// CommitExists reports whether commit resolves to an available commit object
// in the selected repository's local object database. It never fetches or
// changes Git state.
func (c *Client) CommitExists(ctx context.Context, root, commit string) (bool, error) {
	if strings.TrimSpace(commit) == "" {
		return false, errors.New("git commit ID is empty")
	}
	if _, err := c.run(ctx, root, "cat-file", "-e", commit+"^{commit}"); err != nil {
		if isMissingObjectError(err) {
			return false, nil
		}
		return false, fmt.Errorf("check Git commit %q: %w", commit, err)
	}
	return true, nil
}

// BlobExists reports whether objectID resolves to an available Git blob in
// the selected repository's local object database. It never fetches or
// changes Git state.
func (c *Client) BlobExists(ctx context.Context, root, objectID string) (bool, error) {
	if strings.TrimSpace(objectID) == "" {
		return false, errors.New("git blob object ID is empty")
	}
	if _, err := c.run(ctx, root, "cat-file", "-e", objectID+"^{blob}"); err != nil {
		if isMissingObjectError(err) {
			return false, nil
		}
		return false, fmt.Errorf("check Git blob %q: %w", objectID, err)
	}
	return true, nil
}

func isMissingObjectError(err error) bool {
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	message := strings.ToLower(commandErr.Stderr)
	for _, marker := range []string{
		"bad object",
		"unknown revision",
		"not a valid object",
		"ambiguous argument",
		"invalid object name",
		"does not exist in the local object database",
		"is a commit, not a blob",
		"is a tree, not a blob",
		"is a tag, not a blob",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
