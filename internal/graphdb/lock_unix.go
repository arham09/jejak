//go:build darwin || linux

package graphdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func acquireWriter(ctx context.Context, path string, wait time.Duration) (*WriterLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create writer lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open writer lock %q: %w", path, err)
	}
	lock := &WriterLock{file: file, path: path}
	try := func() error { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
	if err := try(); err == nil {
		return lock, nil
	} else if !isWouldBlock(err) {
		_ = file.Close()
		return nil, fmt.Errorf("lock writer file %q: %w", path, err)
	}
	if wait <= 0 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s", ErrWriterLockUnavailable, path)
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
			_ = file.Close()
			return nil, fmt.Errorf("%w: %s", ErrWriterLockUnavailable, path)
		case <-ticker.C:
			if err := try(); err == nil {
				return lock, nil
			} else if !isWouldBlock(err) {
				_ = file.Close()
				return nil, fmt.Errorf("lock writer file %q: %w", path, err)
			}
		}
	}
}

func releaseWriter(lock *WriterLock) error {
	var result error
	if lock.file != nil {
		if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
			result = err
		}
		if err := lock.file.Close(); err != nil {
			result = errors.Join(result, err)
		}
		lock.file = nil
	}
	return result
}

func isWouldBlock(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
