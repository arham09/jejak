package impact

import (
	"context"
	"errors"
	"fmt"

	"github.com/arham09/jejak/internal/graph"
)

// Reader is the minimal committed or effective graph view consumed by the
// impact engine. Implementations must keep all records in one repository,
// worktree, and generation scope.
type Reader interface {
	Generation() graph.Generation
	SearchSymbols(context.Context, []string, int) ([]graph.SymbolResult, error)
	FindSymbols(context.Context, string) ([]graph.SymbolResult, error)
	FindFile(context.Context, string) (graph.FileResult, error)
}

type boundedReader interface {
	FindSymbolForImpact(context.Context, string, int) (graph.SymbolResult, error)
}

// fileMetadataReader is an optional fast path for readers that can return
// only immutable file provenance. The fallback uses Reader.FindFile, whose
// richer declaration payload remains useful for generic readers and tests.
type fileMetadataReader interface {
	FindFileMetadata(context.Context, string) (graph.File, error)
}

// historicalFileMetadataReader lets effective views provide the immutable
// baseline blob for a deleted declaration without exposing that file as a
// current effective file.
type historicalFileMetadataReader interface {
	FindHistoricalFileMetadata(context.Context, string) (graph.File, error)
}

// Engine computes a report from one already-scoped graph reader.
type Engine struct {
	reader Reader
}

// NewEngine returns an impact engine for a non-nil graph reader.
func NewEngine(reader Reader) (*Engine, error) {
	if reader == nil {
		return nil, errors.New("impact engine requires a graph reader")
	}
	return &Engine{reader: reader}, nil
}

// Analyze computes deterministic context, implementation, and validation
// radii for one task. It never writes graph state or reads the working tree.
func (e *Engine) Analyze(ctx context.Context, request Request) (Report, error) {
	if e == nil || e.reader == nil {
		return Report{}, errors.New("impact engine is nil")
	}
	normalized, err := request.Normalize()
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	generation := e.reader.Generation()
	report := Report{
		SchemaVersion:  SchemaVersion,
		Task:           normalized.Task,
		Metadata:       metadataFromGeneration(generation),
		Seeds:          SeedReport{Terms: tokenize(normalized.Task)},
		Context:        Radius{Limit: normalized.ContextLimit, Items: []Item{}},
		Implementation: Radius{Limit: normalized.ImplementationLimit, Items: []Item{}},
		Validation:     Radius{Limit: normalized.ValidationLimit, Items: []Item{}},
		Diagnostics:    []Diagnostic{},
	}
	discovery, err := discoverSeeds(ctx, e.reader, normalized)
	if err != nil {
		return Report{}, fmt.Errorf("discover impact seeds: %w", err)
	}
	report.Seeds = discovery.report
	report.Diagnostics = append(report.Diagnostics, discovery.diagnostics...)
	if len(discovery.selected) == 0 {
		report.Truncated = report.Seeds.Truncated
		normalizeReport(&report)
		return report, nil
	}
	traversal, err := traverse(ctx, e.reader, discovery.selected, normalized)
	if err != nil {
		return Report{}, fmt.Errorf("traverse impact graph: %w", err)
	}
	report.Diagnostics = append(report.Diagnostics, traversal.diagnostics...)
	report.Context = contextRadius(traversal, normalized.ContextLimit)
	report.Implementation = implementationRadius(traversal, normalized.ImplementationLimit)
	report.Validation = validationRadius(traversal, normalized.ValidationLimit)
	report.Truncated = report.Seeds.Truncated || report.Context.Truncated || report.Implementation.Truncated || report.Validation.Truncated || traversal.truncated
	normalizeReport(&report)
	return report, nil
}

// Run is a descriptive alias for Analyze.
func (e *Engine) Run(ctx context.Context, request Request) (Report, error) {
	return e.Analyze(ctx, request)
}

// Analyze is a convenience function for callers that do not need to retain an
// Engine value.
func Analyze(ctx context.Context, reader Reader, request Request) (Report, error) {
	engine, err := NewEngine(reader)
	if err != nil {
		return Report{}, err
	}
	return engine.Analyze(ctx, request)
}

func metadataFromGeneration(generation graph.Generation) Metadata {
	return Metadata{
		RepositoryID:     string(generation.RepoID),
		WorktreeID:       string(generation.WorktreeID),
		Commit:           string(generation.Commit),
		GenerationID:     int64(generation.ID),
		BuildFingerprint: generation.BuildFingerprint,
		AnalyzerVersion:  generation.AnalyzerVersion,
		SchemaVersion:    generation.SchemaVersion,
		SourceMode:       "committed",
	}
}

func normalizeReport(report *Report) {
	if report == nil {
		return
	}
	if report.SchemaVersion == 0 {
		report.SchemaVersion = SchemaVersion
	}
	if report.Seeds.Terms == nil {
		report.Seeds.Terms = []string{}
	}
	if report.Seeds.Candidates == nil {
		report.Seeds.Candidates = []SeedCandidate{}
	}
	if report.Seeds.Selected == nil {
		report.Seeds.Selected = []SeedCandidate{}
	}
	if report.Context.Items == nil {
		report.Context.Items = []Item{}
	}
	if report.Implementation.Items == nil {
		report.Implementation.Items = []Item{}
	}
	if report.Validation.Items == nil {
		report.Validation.Items = []Item{}
	}
	if report.Diagnostics == nil {
		report.Diagnostics = []Diagnostic{}
	}
	if report.Metadata.ChangedPaths == nil {
		report.Metadata.ChangedPaths = []string{}
	}
	for _, radius := range []*Radius{&report.Context, &report.Implementation, &report.Validation} {
		for index := range radius.Items {
			if radius.Items[index].Evidence == nil {
				radius.Items[index].Evidence = []Evidence{}
			}
			if radius.Items[index].Contributions == nil {
				radius.Items[index].Contributions = []ScoreContribution{}
			}
			for evidenceIndex := range radius.Items[index].Evidence {
				if radius.Items[index].Evidence[evidenceIndex].Path == nil {
					radius.Items[index].Evidence[evidenceIndex].Path = []PathStep{}
				}
				if radius.Items[index].Evidence[evidenceIndex].Contributions == nil {
					radius.Items[index].Evidence[evidenceIndex].Contributions = []ScoreContribution{}
				}
			}
		}
	}
}
