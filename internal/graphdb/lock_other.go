//go:build !darwin && !linux

package graphdb

import (
	"context"
	"time"
)

func acquireWriter(context.Context, string, time.Duration) (*WriterLock, error) {
	return nil, ErrLockUnsupported
}

func releaseWriter(*WriterLock) error { return nil }
