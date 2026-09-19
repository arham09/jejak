package brief

import "github.com/arham09/jejak/internal/impact"

const SchemaVersion = 1

// Report contains the impact facts and bounded source excerpts used by a
// coding agent before implementation.
type Report struct {
	SchemaVersion int           `json:"schema_version"`
	Impact        impact.Report `json:"impact"`
	Excerpts      []Excerpt     `json:"excerpts"`
	Diagnostics   []Diagnostic  `json:"diagnostics"`
}

// Excerpt is a pinned source range associated with one impact item. Its source
// may be the durable generation or a command-scoped overlay.
type Excerpt struct {
	Key            string `json:"key"`
	Path           string `json:"path"`
	BlobSHA        string `json:"blob_sha"`
	Commit         string `json:"commit"`
	GenerationID   int64  `json:"generation_id"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Text           string `json:"text"`
	Truncated      bool   `json:"truncated"`
	SourceMode     string `json:"source_mode,omitempty"`
	OverlayID      string `json:"overlay_id,omitempty"`
	SourceManifest string `json:"source_manifest,omitempty"`
	Historical     bool   `json:"historical,omitempty"`
}

// Diagnostic describes an unavailable or bounded source context condition.
type Diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}
