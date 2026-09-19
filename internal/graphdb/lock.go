package graphdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ErrWriterLockUnavailable indicates that another process owns a repository
// writer lock and the requested wait elapsed.
var ErrWriterLockUnavailable = errors.New("repository writer lock unavailable")

// ErrLockUnsupported indicates that the current platform has no implemented
// process-level lock primitive.
var ErrLockUnsupported = errors.New("repository writer lock unsupported on this platform")

// WriterLock is an operating-system-backed repository writer lease. It must
// be closed by the caller; the type is intentionally pointer-only because it
// owns a file descriptor and synchronization state.
type WriterLock struct {
	file     *os.File
	path     string
	close    sync.Once
	closeErr error
}

// AcquireWriter obtains the store's repository writer lock, waiting at most
// wait. A non-positive wait performs one non-blocking attempt.
func (s *Store) AcquireWriter(ctx context.Context, wait time.Duration) (*WriterLock, error) {
	if s == nil {
		return nil, errors.New("acquire writer lock on nil store")
	}
	if _, err := s.database(); err != nil {
		return nil, err
	}
	return acquireWriter(ctx, s.lockPath, wait)
}

// Path returns the lock file path.
func (l *WriterLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close releases the operating-system lock and file descriptor. It is safe to
// call Close more than once; the first release error is returned each time.
func (l *WriterLock) Close() error {
	if l == nil {
		return nil
	}
	l.close.Do(func() { l.closeErr = releaseWriter(l) })
	if l.closeErr != nil {
		return fmt.Errorf("release repository writer lock %q: %w", l.path, l.closeErr)
	}
	return nil
}
