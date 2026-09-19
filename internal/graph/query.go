package graph

import "context"

// SymbolReference is one pinned incoming reference to a symbol. Effective
// working-tree views may mark baseline-only records as historical.
type SymbolReference struct {
	SourceKey     string
	TargetKey     string
	SourcePackage string
	SourceFile    string
	Kind          EdgeKind
	Confidence    Confidence
	Position      Position
	Historical    bool
}

// SymbolRelationship is one generation-pinned semantic relationship. A
// relationship may appear more than once when the same semantic edge has
// multiple source-evidence locations; callers can group by SourceKey,
// TargetKey, and Kind when they need semantic edge counts.
type SymbolRelationship struct {
	SourceKey     string
	TargetKey     string
	SourcePackage string
	TargetPackage string
	SourceFile    string
	TargetFile    string
	SourceName    string
	TargetName    string
	SourceKind    NodeKind
	TargetKind    NodeKind
	Kind          EdgeKind
	Confidence    Confidence
	Position      Position
	Details       string
	// Historical indicates that the relationship comes from the committed
	// baseline and is shown only to explain a working-tree deletion. It is
	// never an effective current edge.
	Historical bool
}

// SymbolResult contains a symbol declaration and its known incoming
// references. All records belong to one generation.
type SymbolResult struct {
	Symbol          Symbol
	References      []SymbolReference
	Calls           []SymbolRelationship
	CalledBy        []SymbolRelationship
	Implementations []SymbolRelationship
	ImplementedBy   []SymbolRelationship
	Tests           []SymbolRelationship
	// Historical marks a baseline declaration retained for deleted-symbol
	// impact. ChangeKind is an overlay-only descriptor such as "deleted" or
	// "modified" and is empty for committed/effective records.
	Historical bool
	ChangeKind string
}

// FileResult contains one source file and declarations/imports associated with
// its package variant.
type FileResult struct {
	File          File
	Symbols       []Symbol
	SymbolResults []SymbolResult
	Imports       []string
}

// QueryView is the consumer-owned read contract implemented by graphdb.View.
type QueryView interface {
	Generation() Generation
	FindSymbols(context.Context, string) ([]SymbolResult, error)
	FindFile(context.Context, string) (FileResult, error)
	Close() error
}
