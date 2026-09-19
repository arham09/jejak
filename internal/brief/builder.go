package brief

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/impact"
)

const (
	defaultMaxLines = 60
	defaultMaxBytes = 12 * 1024
	maxExcerptLines = 512
	maxExcerptBytes = 256 * 1024
)

// Options bounds each source excerpt.
type Options struct {
	MaxLines int
	MaxBytes int
}

func (o Options) normalize() (Options, error) {
	if o.MaxLines == 0 {
		o.MaxLines = defaultMaxLines
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = defaultMaxBytes
	}
	if o.MaxLines < 1 || o.MaxLines > maxExcerptLines {
		return Options{}, fmt.Errorf("brief max lines must be between 1 and %d", maxExcerptLines)
	}
	if o.MaxBytes < 1 || o.MaxBytes > maxExcerptBytes {
		return Options{}, fmt.Errorf("brief max bytes must be between 1 and %d", maxExcerptBytes)
	}
	return o, nil
}

// Builder selects source excerpts from an impact report and reads only the
// report's pinned blob identities. The reader may be a committed Git source
// or a command-scoped working-tree overlay.
type Builder struct {
	source SourceReader
	root   string
	opts   Options
}

// NewBuilder returns a source brief builder.
func NewBuilder(source SourceReader, root string, options Options) (*Builder, error) {
	if source == nil {
		return nil, errors.New("brief builder requires a source reader")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("brief builder requires a repository root")
	}
	normalized, err := options.normalize()
	if err != nil {
		return nil, err
	}
	return &Builder{source: source, root: root, opts: normalized}, nil
}

// Build creates bounded excerpts for the context radius without consulting
// mutable checkout bytes directly.
func (b *Builder) Build(ctx context.Context, report impact.Report) (Report, error) {
	if b == nil || b.source == nil {
		return Report{}, errors.New("brief builder is nil")
	}
	result := Report{SchemaVersion: SchemaVersion, Impact: report, Excerpts: []Excerpt{}, Diagnostics: []Diagnostic{}}
	seen := make(map[string]struct{})
	for _, item := range report.Context.Items {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if item.Kind == graph.NodePackage || item.Source.BlobSHA == "" {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "info", Code: "source-unavailable", Message: fmt.Sprintf("no pinned source blob is available for %s", itemDisplay(item))})
			continue
		}
		path := item.Source.Path
		if path == "" {
			path = item.Path
		}
		if path == "" {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "warning", Code: "source-unavailable", Message: fmt.Sprintf("source path is unavailable for %s", itemDisplay(item))})
			continue
		}
		key := item.Key + "\x00" + item.Source.BlobSHA
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		contents, err := b.source.ReadBlob(ctx, b.root, item.Source.BlobSHA)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "warning", Code: "source-unavailable", Message: fmt.Sprintf("read pinned blob %s for %s: %v", item.Source.BlobSHA, itemDisplay(item), err)})
			continue
		}
		start, end := item.Source.Position.StartLine, item.Source.Position.EndLine
		if start <= 0 {
			start = item.Symbol.Position.StartLine
		}
		if end <= 0 {
			end = item.Symbol.Position.EndLine
		}
		lines := strings.Split(string(contents), "\n")
		if start <= 0 {
			start = 1
		}
		if start > len(lines) {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "warning", Code: "source-position-unavailable", Message: fmt.Sprintf("source position for %s is outside pinned blob %s", itemDisplay(item), item.Source.BlobSHA)})
			continue
		}
		if end < start {
			end = start
		}
		if end > len(lines) {
			end = len(lines)
		}
		if end-start+1 > b.opts.MaxLines {
			end = start + b.opts.MaxLines - 1
		}
		text := strings.Join(lines[start-1:end], "\n")
		truncated := end < item.Source.Position.EndLine
		text, byteTruncated := boundText(text, b.opts.MaxBytes)
		truncated = truncated || byteTruncated
		result.Excerpts = append(result.Excerpts, Excerpt{Key: item.Key, Path: path, BlobSHA: item.Source.BlobSHA, Commit: report.Metadata.Commit, GenerationID: report.Metadata.GenerationID, StartLine: start, EndLine: end, Text: text, Truncated: truncated, SourceMode: report.Metadata.SourceMode, OverlayID: report.Metadata.OverlayID, SourceManifest: report.Metadata.SourceManifest, Historical: item.Historical})
	}
	if result.Excerpts == nil {
		result.Excerpts = []Excerpt{}
	}
	return result, nil
}

func boundText(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	cut := value[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func itemDisplay(item impact.Item) string {
	if item.Name != "" {
		return item.Name
	}
	return item.Key
}
