package graph

import "strings"

// Position describes a source span in a repository-relative file.
type Position struct {
	Path        string
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
}

// Package is a Go package variant (production or a test variant).
type Package struct {
	Key        string
	ImportPath string
	ModulePath string
	Directory  string
	Name       string
	Variant    string
}

// File is a source file selected by the build configuration.
type File struct {
	Key        string
	Path       string
	BlobSHA    string
	PackageKey string
	IsTest     bool
	Position   Position
}

// Symbol is a declaration extracted from a source file.
type Symbol struct {
	Key        string
	Kind       NodeKind
	PackageKey string
	FileKey    string
	Name       string
	Signature  string
	Receiver   string
	Position   Position
	Exported   bool
}

// Blob records the immutable Git object associated with a source file.
type Blob struct {
	SHA          string
	ObjectFormat string
	ByteSize     int64
}

// Node is the normalized graph endpoint persisted in the generation.
type Node struct {
	Key         string
	Kind        NodeKind
	Owned       bool
	PackageKey  string
	FileKey     string
	SymbolKey   string
	SourceBlob  string
	SourceStart int
	SourceEnd   int
}

// Edge is a typed relationship between nodes in one generation.
type Edge struct {
	SourceKey    string
	TargetKey    string
	Kind         EdgeKind
	Confidence   Confidence
	OwnerPackage string
}

// EdgeEvidence points to the source location that supports an edge.
type EdgeEvidence struct {
	SourceKey      string
	TargetKey      string
	Kind           EdgeKind
	AnalyzerSource string
	SourceBlob     string
	StartLine      int
	StartColumn    int
	EndLine        int
	EndColumn      int
	Details        string
}

// PackageDependency records an import/dependency relationship in canonical
// package-key space.
type PackageDependency struct {
	SourcePackage string
	TargetPackage string
}

// TestRelationship associates a test symbol with a production symbol or
// package. Symbol targets are exact/inferred links; a package node is the
// conservative fallback when source traversal cannot identify one symbol.
type TestRelationship struct {
	TestKey    string
	TargetKey  string
	Confidence Confidence
}

// CanonicalKey trims user/analyzer whitespace. It is intentionally small; the
// Go analyzer adds kind-specific prefixes to avoid collisions.
func CanonicalKey(value string) string { return strings.TrimSpace(value) }
