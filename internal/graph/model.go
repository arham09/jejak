// Package graph contains language-neutral graph and generation domain types.
package graph

// NodeKind identifies the kind of graph node. The vocabulary is shared by the
// foundation schema and the source-derived records emitted by analyzers.
type NodeKind string

const (
	NodeRepository NodeKind = "repository"
	NodePackage    NodeKind = "package"
	NodeFile       NodeKind = "file"
	NodeSymbol     NodeKind = "symbol"
	NodeFunction   NodeKind = "function"
	NodeMethod     NodeKind = "method"
	NodeType       NodeKind = "type"
	NodeStruct     NodeKind = "struct"
	NodeInterface  NodeKind = "interface"
	NodeTest       NodeKind = "test"
)

// EdgeKind identifies a typed graph relationship.
type EdgeKind string

const (
	EdgeContains       EdgeKind = "contains"
	EdgeDefines        EdgeKind = "defines"
	EdgeImports        EdgeKind = "imports"
	EdgeCalls          EdgeKind = "calls"
	EdgeReferences     EdgeKind = "references"
	EdgeImplements     EdgeKind = "implements"
	EdgeDependsOn      EdgeKind = "depends_on"
	EdgeTests          EdgeKind = "tests"
	EdgePossibleCall   EdgeKind = "possible_call"
	EdgeUnresolvedCall EdgeKind = "unresolved_call"
)

// Confidence describes how strongly static analysis supports a relationship.
type Confidence string

const (
	ConfidenceExact    Confidence = "exact"
	ConfidenceInferred Confidence = "inferred"
	ConfidencePossible Confidence = "possible"
)
