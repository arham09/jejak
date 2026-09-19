// Package brief assembles bounded committed source context for an impact
// report. It deliberately uses a blob reader instead of the live checkout.
package brief

import "context"

// SourceReader reads one immutable source object for a repository root.
type SourceReader interface {
	ReadBlob(context.Context, string, string) ([]byte, error)
}
