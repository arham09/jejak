package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/repository"
)

// Analyzer is the language-neutral analysis boundary consumed by the graph
// manager. Implementations return graph records rather than language-specific
// syntax or type objects.
type Analyzer interface {
	Supports(path string) bool
	Analyze(context.Context, AnalyzeInput) (AnalysisResult, error)
}

// Fingerprinter is an optional extension implemented by analyzers that can
// calculate the build identity before doing a full analysis. It lets the
// manager reuse an active generation when the source and build inputs still
// match.
type Fingerprinter interface {
	BuildFingerprint(context.Context, AnalyzeInput) (string, error)
}

// Manifest lists one committed tree without materializing its bytes. It holds
// exactly what a build-identity decision needs, so the manager can reuse an
// active generation without writing a temporary checkout it would discard.
type Manifest struct {
	Commit       CommitSHA
	ObjectFormat string
	Files        []SnapshotFile

	// ReadFile returns the committed contents of one repository-relative path
	// from the same tree. It reads immutable Git objects and never the working
	// tree. A fingerprinter uses it for the few build files it must parse, so
	// listing a tree stays independent of any one language's build layout.
	ReadFile func(path string) ([]byte, error)
}

// ManifestInput is the build identity question asked against a listed tree.
type ManifestInput struct {
	Repository repository.RepoID
	Worktree   repository.WorktreeID
	Commit     CommitSHA
	Manifest   *Manifest
	Build      BuildConfig
}

// ManifestProvider lists a committed tree without materializing it.
type ManifestProvider interface {
	Manifest(context.Context, string, string) (*Manifest, error)
}

// ManifestFingerprinter is an optional extension implemented by analyzers whose
// build identity can be derived from a tree listing. Git blob identifiers are
// content hashes, so a listing determines the source exactly and the manager
// can decide to reuse a generation without reading any file body.
type ManifestFingerprinter interface {
	ManifestFingerprint(context.Context, ManifestInput) (string, error)
}

// DiffProvider supplies normalized committed-tree changes without coupling the
// graph package to a Git implementation.
type DiffProvider interface {
	Diff(context.Context, string, string, string) ([]FileChange, error)
}

// SyntaxCacheProvider optionally emits serializable syntax summaries. The
// summaries are an optimization boundary; semantic type resolution remains
// owned by the analyzer and is never reused by blob identity alone.
type SyntaxCacheProvider interface {
	SyntaxCacheEntries(context.Context, AnalyzeInput) ([]SyntaxCacheEntry, error)
}

// SyntaxParserVersioner identifies the syntax-summary schema before parsing,
// allowing the manager to look up cached blobs without doing duplicate work.
type SyntaxParserVersioner interface {
	SyntaxParserVersion() string
}

// SyntaxCache persists versioned syntax summaries independently from graph
// generations. Implementations must scope entries by repository, blob, object
// format, and parser version.
type SyntaxCache interface {
	ReadParseCache(context.Context, repository.RepoID, string, string, string) (SyntaxCacheEntry, bool, error)
	WriteParseCache(context.Context, repository.RepoID, SyntaxCacheEntry) error
}

// SyntaxCacheEntry is one serializable file syntax summary.
type SyntaxCacheEntry struct {
	BlobSHA       string
	ObjectFormat  string
	ParserVersion string
	SyntaxJSON    []byte
}

// SyntaxCacheStats reports best-effort optimization accounting for one graph
// request. A cache error is counted as a miss because reparsing is safe.
type SyntaxCacheStats struct {
	Hits   int
	Misses int
	Errors int
}

// SnapshotFile identifies one file in an immutable committed source snapshot.
// Paths are repository-relative slash-separated paths.
type SnapshotFile struct {
	Path         string
	BlobSHA      string
	ObjectFormat string
	Mode         string
	Size         int64
}

// Snapshot is the minimal source view needed by an analyzer. The owner must
// close it after Analyze returns; closing removes temporary materialized data.
type Snapshot struct {
	Root   string
	Commit CommitSHA
	Files  []SnapshotFile
	close  func() error
}

// NewSnapshot wraps a materialized source directory for use by an analyzer.
// closeFn owns removal of the temporary directory and may be nil for a caller
// that manages the directory separately.
func NewSnapshot(root string, commit CommitSHA, files []SnapshotFile, closeFn func() error) *Snapshot {
	return &Snapshot{Root: root, Commit: commit, Files: append([]SnapshotFile(nil), files...), close: closeFn}
}

// Close releases temporary snapshot storage. It is safe to call repeatedly.
func (s *Snapshot) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	err := s.close()
	s.close = nil
	return err
}

// AnalyzeInput describes one committed source analysis. The Root directory
// must be an immutable snapshot, never a developer's mutable checkout.
type AnalyzeInput struct {
	Repository     repository.RepoID
	Worktree       repository.WorktreeID
	Commit         CommitSHA
	PreviousCommit CommitSHA
	Changes        []FileChange
	CachedSyntax   []SyntaxCacheEntry
	Incremental    bool
	Root           string
	Files          []SnapshotFile
	Snapshot       *Snapshot
	Build          BuildConfig
}

// BuildConfig contains the build-selection and dependency policy that affect
// Go package resolution.
type BuildConfig struct {
	GOOS                 string
	GOARCH               string
	CGOEnabled           string
	Tags                 []string
	DownloadDependencies bool
	// IncludeTests loads _test.go package variants and records test symbols
	// and test relationships. Test code usually doubles the graph, so it is
	// excluded unless a caller asks for it.
	IncludeTests bool
}

// Normalize sorts tags and trims duplicate/empty entries. It returns a copy so
// callers can safely reuse their input configuration.
func (c BuildConfig) Normalize() BuildConfig {
	result := c
	result.GOOS = strings.TrimSpace(result.GOOS)
	result.GOARCH = strings.TrimSpace(result.GOARCH)
	result.CGOEnabled = strings.TrimSpace(result.CGOEnabled)
	seen := make(map[string]struct{}, len(c.Tags))
	result.Tags = nil
	for _, tag := range c.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result.Tags = append(result.Tags, tag)
	}
	sort.Strings(result.Tags)
	return result
}

// DiagnosticSeverity classifies an analyzer message.
type DiagnosticSeverity string

const (
	SeverityInfo    DiagnosticSeverity = "info"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityError   DiagnosticSeverity = "error"
)

// Diagnostic is an actionable analyzer or graph-build message.
type Diagnostic struct {
	Severity DiagnosticSeverity
	Package  string
	File     string
	Message  string
}

func (d Diagnostic) Validate() error {
	switch d.Severity {
	case SeverityInfo, SeverityWarning, SeverityError:
	default:
		return fmt.Errorf("unknown diagnostic severity %q", d.Severity)
	}
	if strings.TrimSpace(d.Message) == "" {
		return errors.New("diagnostic message is empty")
	}
	return nil
}

// AnalysisResult contains source-backed records for one candidate generation.
type AnalysisResult struct {
	AnalyzerVersion     string
	BuildFingerprint    string
	Packages            []Package
	Files               []File
	Symbols             []Symbol
	Nodes               []Node
	Edges               []Edge
	Evidence            []EdgeEvidence
	Blobs               []Blob
	PackageDependencies []PackageDependency
	TestRelationships   []TestRelationship
	Diagnostics         []Diagnostic
}

// HasErrors reports whether the result contains a diagnostic that prevents a
// complete semantic generation from becoming active.
func (r AnalysisResult) HasErrors() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Count returns the source-derived record counts used by CLI summaries.
func (r AnalysisResult) Count() Counts {
	return Counts{Packages: len(r.Packages), Files: len(r.Files), Symbols: len(r.Symbols), Edges: len(r.Edges), Tests: countTests(r)}
}

// Counts is a deterministic summary of an analysis result.
type Counts struct {
	Packages int
	Files    int
	Symbols  int
	Edges    int
	Tests    int
}

func countTests(r AnalysisResult) int {
	count := 0
	for _, symbol := range r.Symbols {
		if symbol.Kind == NodeTest {
			count++
		}
	}
	return count
}

// Normalize canonicalizes and deterministically orders records before
// persistence. It returns a copy and does not mutate analyzer-owned slices.
func (r AnalysisResult) Normalize() AnalysisResult {
	result := r
	result.BuildFingerprint = strings.TrimSpace(result.BuildFingerprint)
	result.AnalyzerVersion = strings.TrimSpace(result.AnalyzerVersion)
	result.Packages = append([]Package(nil), result.Packages...)
	result.Files = append([]File(nil), result.Files...)
	result.Symbols = append([]Symbol(nil), result.Symbols...)
	result.Nodes = append([]Node(nil), result.Nodes...)
	result.Edges = append([]Edge(nil), result.Edges...)
	result.Evidence = append([]EdgeEvidence(nil), result.Evidence...)
	result.Blobs = append([]Blob(nil), result.Blobs...)
	result.PackageDependencies = append([]PackageDependency(nil), result.PackageDependencies...)
	result.TestRelationships = append([]TestRelationship(nil), result.TestRelationships...)
	result.Diagnostics = append([]Diagnostic(nil), result.Diagnostics...)
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Key < result.Packages[j].Key })
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Key < result.Files[j].Key })
	sort.Slice(result.Symbols, func(i, j int) bool { return result.Symbols[i].Key < result.Symbols[j].Key })
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Key < result.Nodes[j].Key })
	sort.Slice(result.Edges, func(i, j int) bool {
		if result.Edges[i].SourceKey != result.Edges[j].SourceKey {
			return result.Edges[i].SourceKey < result.Edges[j].SourceKey
		}
		if result.Edges[i].TargetKey != result.Edges[j].TargetKey {
			return result.Edges[i].TargetKey < result.Edges[j].TargetKey
		}
		return result.Edges[i].Kind < result.Edges[j].Kind
	})
	sort.Slice(result.Evidence, func(i, j int) bool { return evidenceKey(result.Evidence[i]) < evidenceKey(result.Evidence[j]) })
	sort.Slice(result.Blobs, func(i, j int) bool { return result.Blobs[i].SHA < result.Blobs[j].SHA })
	sort.Slice(result.PackageDependencies, func(i, j int) bool {
		if result.PackageDependencies[i].SourcePackage != result.PackageDependencies[j].SourcePackage {
			return result.PackageDependencies[i].SourcePackage < result.PackageDependencies[j].SourcePackage
		}
		return result.PackageDependencies[i].TargetPackage < result.PackageDependencies[j].TargetPackage
	})
	sort.Slice(result.TestRelationships, func(i, j int) bool {
		if result.TestRelationships[i].TestKey != result.TestRelationships[j].TestKey {
			return result.TestRelationships[i].TestKey < result.TestRelationships[j].TestKey
		}
		return result.TestRelationships[i].TargetKey < result.TestRelationships[j].TargetKey
	})
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		if result.Diagnostics[i].Severity != result.Diagnostics[j].Severity {
			return result.Diagnostics[i].Severity < result.Diagnostics[j].Severity
		}
		if result.Diagnostics[i].Package != result.Diagnostics[j].Package {
			return result.Diagnostics[i].Package < result.Diagnostics[j].Package
		}
		if result.Diagnostics[i].File != result.Diagnostics[j].File {
			return result.Diagnostics[i].File < result.Diagnostics[j].File
		}
		return result.Diagnostics[i].Message < result.Diagnostics[j].Message
	})
	return result
}

func evidenceKey(e EdgeEvidence) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s", e.SourceKey, e.TargetKey, e.Kind, e.AnalyzerSource, e.StartLine, e.StartColumn, e.EndLine, e.EndColumn, e.SourceBlob, e.Details)
}

// FingerprintBytes returns a stable short fingerprint for arbitrary analysis
// inputs. The Go analyzer uses this helper after hashing all build inputs.
func FingerprintBytes(parts ...[]byte) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write(part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
