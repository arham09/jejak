// Package impact turns a generation-pinned graph into bounded, explainable
// task guidance. It contains no storage or Git implementation details.
package impact

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

const (
	// SchemaVersion is the stable JSON schema version for impact reports.
	SchemaVersion = 1
	defaultSeeds  = 8
	defaultDepth  = 3
	defaultFanout = 32
	defaultItems  = 24
	maxSeeds      = 64
	maxDepth      = 12
	maxFanout     = 512
	maxItems      = 2048
)

// SeedStatus describes how task seeds were selected.
type SeedStatus string

const (
	SeedMatched   SeedStatus = "matched"
	SeedAmbiguous SeedStatus = "ambiguous"
	SeedNoMatch   SeedStatus = "no-match"
)

// Label describes the purpose or confidence of a selected report item.
type Label string

const (
	LabelMustRead     Label = "must-read"
	LabelSupporting   Label = "supporting"
	LabelTest         Label = "test"
	LabelHigh         Label = "high"
	LabelMedium       Label = "medium"
	LabelLow          Label = "low"
	LabelInspectOnly  Label = "inspect-only"
	LabelDownstream   Label = "downstream"
	LabelDirectTest   Label = "direct-test"
	LabelPackageTests Label = "package-tests"
	LabelPackageRisk  Label = "package-risk"
	LabelExternal     Label = "external"
	LabelUnavailable  Label = "unavailable"
	LabelHistorical   Label = "historical"
)

// Request controls one bounded impact analysis.
type Request struct {
	Task                string
	SeedKeys            []string
	MaxSeeds            int
	MaxDepth            int
	MaxFanout           int
	ContextLimit        int
	ImplementationLimit int
	ValidationLimit     int
	// SeedOnly skips lexical search and uses only SeedKeys. Overlay actual
	// change analysis uses this mode so unrelated symbols cannot become seeds.
	SeedOnly bool
}

// Normalize validates limits and returns an independent request copy.
func (r Request) Normalize() (Request, error) {
	r.Task = strings.TrimSpace(r.Task)
	if r.Task == "" {
		return Request{}, errors.New("impact task is empty")
	}
	r.SeedKeys = cleanStrings(r.SeedKeys)
	if r.MaxSeeds == 0 {
		r.MaxSeeds = defaultSeeds
	}
	if r.MaxDepth == 0 {
		r.MaxDepth = defaultDepth
	}
	if r.MaxFanout == 0 {
		r.MaxFanout = defaultFanout
	}
	if r.ContextLimit == 0 {
		r.ContextLimit = defaultItems
	}
	if r.ImplementationLimit == 0 {
		r.ImplementationLimit = defaultItems
	}
	if r.ValidationLimit == 0 {
		r.ValidationLimit = defaultItems
	}
	if r.MaxSeeds < 1 || r.MaxSeeds > maxSeeds {
		return Request{}, fmt.Errorf("impact max seeds must be between 1 and %d", maxSeeds)
	}
	if r.MaxDepth < 1 || r.MaxDepth > maxDepth {
		return Request{}, fmt.Errorf("impact max depth must be between 1 and %d", maxDepth)
	}
	if r.MaxFanout < 1 || r.MaxFanout > maxFanout {
		return Request{}, fmt.Errorf("impact max fanout must be between 1 and %d", maxFanout)
	}
	for name, value := range map[string]int{
		"context limit":        r.ContextLimit,
		"implementation limit": r.ImplementationLimit,
		"validation limit":     r.ValidationLimit,
	} {
		if value < 1 || value > maxItems {
			return Request{}, fmt.Errorf("impact %s must be between 1 and %d", name, maxItems)
		}
	}
	return r, nil
}

// Metadata identifies the immutable graph used for a report.
type Metadata struct {
	RepositoryID              string   `json:"repository_id"`
	WorktreeID                string   `json:"worktree_id"`
	Commit                    string   `json:"commit"`
	GenerationID              int64    `json:"generation_id"`
	BuildFingerprint          string   `json:"build_fingerprint"`
	AnalyzerVersion           string   `json:"analyzer_version"`
	SchemaVersion             int      `json:"graph_schema_version"`
	SourceMode                string   `json:"source_mode,omitempty"`
	BaseCommit                string   `json:"base_commit,omitempty"`
	BaseGenerationID          int64    `json:"base_generation_id,omitempty"`
	OverlayID                 string   `json:"overlay_id,omitempty"`
	SourceManifest            string   `json:"source_manifest,omitempty"`
	EffectiveBuildFingerprint string   `json:"effective_build_fingerprint,omitempty"`
	Incomplete                bool     `json:"incomplete,omitempty"`
	ChangedFiles              int      `json:"changed_files,omitempty"`
	ChangedPaths              []string `json:"changed_paths,omitempty"`
}

// Report is the versioned, presentation-independent impact result.
type Report struct {
	SchemaVersion  int          `json:"schema_version"`
	Task           string       `json:"task"`
	Metadata       Metadata     `json:"metadata"`
	Seeds          SeedReport   `json:"seeds"`
	Context        Radius       `json:"context_radius"`
	Implementation Radius       `json:"implementation_radius"`
	Validation     Radius       `json:"validation_radius"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	Truncated      bool         `json:"truncated"`
}

// SeedReport records candidate and selected task seeds.
type SeedReport struct {
	Status     SeedStatus      `json:"status"`
	Terms      []string        `json:"terms"`
	Candidates []SeedCandidate `json:"candidates"`
	Selected   []SeedCandidate `json:"selected"`
	Truncated  bool            `json:"truncated"`
}

// SeedCandidate is a lexical or qualified symbol match.
type SeedCandidate struct {
	Key        string         `json:"key"`
	Name       string         `json:"name"`
	Kind       graph.NodeKind `json:"kind"`
	Package    string         `json:"package"`
	Path       string         `json:"path"`
	Signature  string         `json:"signature,omitempty"`
	Score      int            `json:"score"`
	Match      string         `json:"match"`
	Reason     string         `json:"reason"`
	Symbol     graph.Symbol   `json:"symbol"`
	Historical bool           `json:"historical,omitempty"`
	ChangeKind string         `json:"change_kind,omitempty"`
}

// Radius is one independently bounded selection.
type Radius struct {
	Items     []Item `json:"items"`
	Limit     int    `json:"limit"`
	Truncated bool   `json:"truncated"`
}

// Item is a selected symbol or package with its source and evidence.
type Item struct {
	Key           string              `json:"key"`
	Kind          graph.NodeKind      `json:"kind"`
	Name          string              `json:"name"`
	Package       string              `json:"package"`
	Path          string              `json:"path,omitempty"`
	Signature     string              `json:"signature,omitempty"`
	Score         int                 `json:"score"`
	Label         Label               `json:"label"`
	Reason        string              `json:"reason"`
	Confidence    graph.Confidence    `json:"confidence,omitempty"`
	Symbol        graph.Symbol        `json:"symbol,omitempty"`
	Source        SourceRef           `json:"source,omitempty"`
	Evidence      []Evidence          `json:"evidence"`
	Contributions []ScoreContribution `json:"score_contributions"`
	External      bool                `json:"external,omitempty"`
	Historical    bool                `json:"historical,omitempty"`
	ChangeKind    string              `json:"change_kind,omitempty"`
}

// SourceRef identifies the immutable source represented by an item.
type SourceRef struct {
	Path     string         `json:"path,omitempty"`
	BlobSHA  string         `json:"blob_sha,omitempty"`
	Position graph.Position `json:"position,omitempty"`
}

// Evidence explains why an item was selected.
type Evidence struct {
	Reason        string              `json:"reason"`
	Confidence    graph.Confidence    `json:"confidence,omitempty"`
	Path          []PathStep          `json:"path"`
	Contributions []ScoreContribution `json:"score_contributions"`
}

// PathStep is one graph edge in a shortest deterministic explanation path.
type PathStep struct {
	FromKey    string           `json:"from_key"`
	ToKey      string           `json:"to_key"`
	Kind       graph.EdgeKind   `json:"kind"`
	Confidence graph.Confidence `json:"confidence,omitempty"`
	Position   graph.Position   `json:"position,omitempty"`
	Details    string           `json:"details,omitempty"`
	Historical bool             `json:"historical,omitempty"`
}

// ScoreContribution keeps relevance arithmetic inspectable.
type ScoreContribution struct {
	Signal string `json:"signal"`
	Value  int    `json:"value"`
}

// Diagnostic is a non-fatal report limitation or source issue.
type Diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
