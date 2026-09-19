package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

const graphExportSchemaVersion = 1

type graphOutputFormat string

const (
	graphOutputText graphOutputFormat = "text"
	graphOutputJSON graphOutputFormat = "json"
	graphOutputDOT  graphOutputFormat = "dot"
)

func parseGraphOutputFormat(value string) (graphOutputFormat, error) {
	switch graphOutputFormat(strings.ToLower(strings.TrimSpace(value))) {
	case graphOutputText:
		return graphOutputText, nil
	case graphOutputJSON:
		return graphOutputJSON, nil
	case graphOutputDOT:
		return graphOutputDOT, nil
	default:
		return "", fmt.Errorf("%w: unsupported graph format %q (want text, json, or dot)", errUsage, value)
	}
}

// graphExport is the canonical query-scoped projection used by the JSON and
// DOT renderers. It deliberately contains no SQL or analyzer values.
type graphExport struct {
	SchemaVersion int                   `json:"schema_version"`
	Query         graphExportQuery      `json:"query"`
	Repository    graphExportRepository `json:"repository"`
	Worktree      graphExportWorktree   `json:"worktree"`
	Generation    graphExportGeneration `json:"generation"`
	Source        graphExportSource     `json:"source"`
	Nodes         []graphExportNode     `json:"nodes"`
	Edges         []graphExportEdge     `json:"edges"`
}

type graphExportQuery struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type graphExportRepository struct {
	ID       string `json:"id"`
	Identity string `json:"identity"`
}

type graphExportWorktree struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

type graphExportGeneration struct {
	ID               int64  `json:"id"`
	Commit           string `json:"commit"`
	BuildFingerprint string `json:"build_fingerprint"`
	AnalyzerVersion  string `json:"analyzer_version"`
	SchemaVersion    int    `json:"schema_version"`
}

type graphExportSource struct {
	Mode             string `json:"mode"`
	BaseCommit       string `json:"base_commit"`
	BaseGenerationID int64  `json:"base_generation_id"`
	OverlayID        string `json:"overlay_id"`
	ManifestID       string `json:"manifest_id"`
}

type graphExportNode struct {
	ID         string              `json:"id"`
	Kind       string              `json:"kind"`
	Name       string              `json:"name"`
	Package    string              `json:"package"`
	File       string              `json:"file"`
	BlobSHA    string              `json:"blob_sha"`
	Signature  string              `json:"signature"`
	Receiver   string              `json:"receiver"`
	Position   graphExportPosition `json:"position"`
	Exported   bool                `json:"exported"`
	IsTest     bool                `json:"is_test"`
	Historical bool                `json:"historical"`
	ChangeKind string              `json:"change_kind"`
}

type graphExportEdge struct {
	Source     string                `json:"source"`
	Target     string                `json:"target"`
	Kind       string                `json:"kind"`
	Confidence string                `json:"confidence"`
	Locations  []graphExportPosition `json:"locations"`
	Details    []string              `json:"details"`
	Historical bool                  `json:"historical"`
}

type graphExportPosition struct {
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
}

type graphExportBuilder struct {
	export graphExport
	nodes  map[string]int
	edges  map[string]int
}

func newSymbolGraphExport(target repository.Target, generation graph.Generation, source graphSource, query string, results []graph.SymbolResult) graphExport {
	builder := newGraphExportBuilder(target, generation, source, "symbol", query)
	for _, result := range results {
		builder.addSymbolResult(result)
	}
	return builder.finish()
}

func newFileGraphExport(target repository.Target, generation graph.Generation, source graphSource, query string, result graph.FileResult) graphExport {
	builder := newGraphExportBuilder(target, generation, source, "file", query)
	fileKey := result.File.Key
	if fileKey == "" {
		fileKey = "file:" + filepath.ToSlash(result.File.Path)
	}
	fileNode := graphExportNode{
		ID:       fileKey,
		Kind:     string(graph.NodeFile),
		Name:     result.File.Path,
		File:     result.File.Path,
		Package:  result.File.PackageKey,
		BlobSHA:  result.File.BlobSHA,
		IsTest:   result.File.IsTest,
		Position: graphExportPosition{Path: result.File.Path},
	}
	builder.addNode(fileNode)

	for _, symbol := range result.Symbols {
		builder.addFileSymbol(fileKey, result.File.Path, symbol)
	}
	for _, symbolResult := range result.SymbolResults {
		builder.addFileSymbol(fileKey, result.File.Path, symbolResult.Symbol)
		builder.addSymbolResult(symbolResult)
	}
	for _, importPath := range result.Imports {
		importPath = strings.TrimSpace(importPath)
		if importPath == "" {
			continue
		}
		importKey := "import:" + importPath
		builder.addNode(graphExportNode{ID: importKey, Kind: string(graph.NodePackage), Name: importPath, Package: importPath})
		builder.addEdge(graphExportEdge{
			Source:     fileKey,
			Target:     importKey,
			Kind:       string(graph.EdgeImports),
			Confidence: graphConfidenceLabel(graph.ConfidenceExact),
		})
	}
	return builder.finish()
}

func newGraphExportBuilder(target repository.Target, generation graph.Generation, source graphSource, queryKind, query string) *graphExportBuilder {
	return &graphExportBuilder{
		export: graphExport{
			SchemaVersion: graphExportSchemaVersion,
			Query:         graphExportQuery{Kind: queryKind, Value: query},
			Repository: graphExportRepository{
				ID:       string(target.Repository.ID),
				Identity: target.Repository.CanonicalIdentity,
			},
			Worktree: graphExportWorktree{
				ID:     string(target.Worktree.ID),
				Path:   target.Worktree.Path,
				Branch: target.Worktree.Branch,
			},
			Generation: graphExportGeneration{
				ID:               int64(generation.ID),
				Commit:           string(generation.Commit),
				BuildFingerprint: generation.BuildFingerprint,
				AnalyzerVersion:  generation.AnalyzerVersion,
				SchemaVersion:    generation.SchemaVersion,
			},
			Source: graphExportSource{
				Mode:             source.mode,
				BaseCommit:       source.baseCommit,
				BaseGenerationID: source.baseGeneration,
				OverlayID:        source.overlayID,
				ManifestID:       source.manifest,
			},
			Nodes: make([]graphExportNode, 0),
			Edges: make([]graphExportEdge, 0),
		},
		nodes: make(map[string]int),
		edges: make(map[string]int),
	}
}

func (b *graphExportBuilder) addSymbolResult(result graph.SymbolResult) {
	b.addNode(symbolExportNode(result.Symbol, result.Historical, result.ChangeKind))
	for _, reference := range result.References {
		b.addReference(reference)
	}
	for _, relationships := range [][]graph.SymbolRelationship{
		result.Calls,
		result.CalledBy,
		result.Implementations,
		result.ImplementedBy,
		result.Tests,
	} {
		for _, relationship := range relationships {
			b.addRelationship(relationship)
		}
	}
}

func (b *graphExportBuilder) addFileSymbol(fileKey, filePath string, symbol graph.Symbol) {
	if symbol.Position.Path == "" {
		symbol.Position.Path = filePath
	}
	b.addNode(symbolExportNode(symbol, false, ""))
	if fileKey == "" || symbol.Key == "" {
		return
	}
	b.addEdge(graphExportEdge{
		Source:     fileKey,
		Target:     symbol.Key,
		Kind:       string(graph.EdgeContains),
		Confidence: graphConfidenceLabel(graph.ConfidenceExact),
	})
}

func (b *graphExportBuilder) addReference(reference graph.SymbolReference) {
	if reference.SourceKey == "" || reference.TargetKey == "" {
		return
	}
	position := exportPosition(reference.Position)
	if position.Path == "" {
		position.Path = reference.SourceFile
	}
	b.addNode(graphExportNode{
		ID:         reference.SourceKey,
		Kind:       string(inferNodeKind(reference.SourceKey)),
		Package:    reference.SourcePackage,
		File:       reference.SourceFile,
		Position:   position,
		Historical: reference.Historical,
	})
	b.addEdge(graphExportEdge{
		Source:     reference.SourceKey,
		Target:     reference.TargetKey,
		Kind:       string(nonEmptyEdgeKind(reference.Kind, graph.EdgeReferences)),
		Confidence: graphConfidenceLabel(reference.Confidence),
		Locations:  positionsOrEmpty(position),
		Historical: reference.Historical,
	})
}

func (b *graphExportBuilder) addRelationship(relationship graph.SymbolRelationship) {
	if relationship.SourceKey == "" || relationship.TargetKey == "" {
		return
	}
	position := exportPosition(relationship.Position)
	if position.Path == "" {
		position.Path = relationship.SourceFile
	}
	b.addNode(graphExportNode{
		ID:         relationship.SourceKey,
		Kind:       string(nodeKindOrInferred(relationship.SourceKind, relationship.SourceKey)),
		Name:       relationship.SourceName,
		Package:    relationship.SourcePackage,
		File:       relationship.SourceFile,
		Position:   position,
		Historical: relationship.Historical,
	})
	b.addNode(graphExportNode{
		ID:         relationship.TargetKey,
		Kind:       string(nodeKindOrInferred(relationship.TargetKind, relationship.TargetKey)),
		Name:       relationship.TargetName,
		Package:    relationship.TargetPackage,
		File:       relationship.TargetFile,
		Historical: relationship.Historical,
	})
	b.addEdge(graphExportEdge{
		Source:     relationship.SourceKey,
		Target:     relationship.TargetKey,
		Kind:       string(relationship.Kind),
		Confidence: graphConfidenceLabel(relationship.Confidence),
		Locations:  positionsOrEmpty(position),
		Details:    nonEmptyDetails(relationship.Details),
		Historical: relationship.Historical,
	})
}

func (b *graphExportBuilder) addNode(node graphExportNode) {
	if node.ID == "" {
		return
	}
	if node.Kind == "" {
		node.Kind = string(inferNodeKind(node.ID))
	}
	if index, ok := b.nodes[node.ID]; ok {
		b.export.Nodes[index] = mergeGraphExportNodes(b.export.Nodes[index], node)
		return
	}
	if node.Position.Path == "" && node.File != "" {
		node.Position.Path = node.File
	}
	b.nodes[node.ID] = len(b.export.Nodes)
	b.export.Nodes = append(b.export.Nodes, node)
}

func (b *graphExportBuilder) addEdge(edge graphExportEdge) {
	if edge.Source == "" || edge.Target == "" || edge.Kind == "" {
		return
	}
	if edge.Confidence == "" {
		edge.Confidence = graphConfidenceLabel("")
	}
	if edge.Locations == nil {
		edge.Locations = make([]graphExportPosition, 0)
	}
	if edge.Details == nil {
		edge.Details = make([]string, 0)
	}
	key := edge.Source + "\x00" + edge.Target + "\x00" + edge.Kind + "\x00" + fmt.Sprint(edge.Historical)
	if index, ok := b.edges[key]; ok {
		existing := &b.export.Edges[index]
		existing.Confidence = weakerConfidence(existing.Confidence, edge.Confidence)
		existing.Locations = appendUniquePositions(existing.Locations, edge.Locations...)
		existing.Details = appendUniqueStrings(existing.Details, edge.Details...)
		return
	}
	b.edges[key] = len(b.export.Edges)
	b.export.Edges = append(b.export.Edges, edge)
}

func (b *graphExportBuilder) finish() graphExport {
	sort.Slice(b.export.Nodes, func(i, j int) bool {
		return b.export.Nodes[i].ID < b.export.Nodes[j].ID
	})
	for index := range b.export.Edges {
		b.export.Edges[index].Locations = appendUniquePositions(nil, b.export.Edges[index].Locations...)
		b.export.Edges[index].Details = appendUniqueStrings(nil, b.export.Edges[index].Details...)
	}
	sort.Slice(b.export.Edges, func(i, j int) bool {
		left, right := b.export.Edges[i], b.export.Edges[j]
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Historical != right.Historical {
			return !left.Historical
		}
		return left.Confidence < right.Confidence
	})
	return b.export
}

func symbolExportNode(symbol graph.Symbol, historical bool, changeKind string) graphExportNode {
	return graphExportNode{
		ID:         symbol.Key,
		Kind:       string(nodeKindOrInferred(symbol.Kind, symbol.Key)),
		Name:       symbol.Name,
		Package:    symbol.PackageKey,
		File:       symbol.Position.Path,
		Signature:  symbol.Signature,
		Receiver:   symbol.Receiver,
		Position:   exportPosition(symbol.Position),
		Exported:   symbol.Exported,
		Historical: historical,
		ChangeKind: changeKind,
	}
}

func exportPosition(position graph.Position) graphExportPosition {
	return graphExportPosition{
		Path:        position.Path,
		StartLine:   position.StartLine,
		StartColumn: position.StartColumn,
		EndLine:     position.EndLine,
		EndColumn:   position.EndColumn,
	}
}

func positionsOrEmpty(position graphExportPosition) []graphExportPosition {
	if position == (graphExportPosition{}) {
		return make([]graphExportPosition, 0)
	}
	return []graphExportPosition{position}
}

func nonEmptyEdgeKind(value, fallback graph.EdgeKind) graph.EdgeKind {
	if value == "" {
		return fallback
	}
	return value
}

func graphConfidenceLabel(value graph.Confidence) string {
	if value == "" {
		return "unspecified"
	}
	return string(value)
}

func weakerConfidence(left, right string) string {
	if confidenceRank(right) < confidenceRank(left) {
		return right
	}
	if confidenceRank(right) == confidenceRank(left) && right < left {
		return right
	}
	return left
}

func confidenceRank(value string) int {
	switch value {
	case "unspecified":
		return 0
	case string(graph.ConfidencePossible):
		return 1
	case string(graph.ConfidenceInferred):
		return 2
	case string(graph.ConfidenceExact):
		return 3
	default:
		return 0
	}
}

func inferNodeKind(key string) graph.NodeKind {
	switch {
	case strings.HasPrefix(key, "symbol:"):
		return graph.NodeSymbol
	case strings.HasPrefix(key, "file:"):
		return graph.NodeFile
	case strings.HasPrefix(key, "go:package:"), strings.HasPrefix(key, "package:"), strings.HasPrefix(key, "external:package:"), strings.HasPrefix(key, "import:"):
		return graph.NodePackage
	case strings.HasPrefix(key, "repo:"):
		return graph.NodeRepository
	default:
		return ""
	}
}

func nodeKindOrInferred(kind graph.NodeKind, key string) graph.NodeKind {
	if kind != "" {
		return kind
	}
	return inferNodeKind(key)
}

func mergeGraphExportNodes(left, right graphExportNode) graphExportNode {
	left.Kind = mergeNodeKinds(left.Kind, right.Kind)
	left.Name = mergeStrings(left.Name, right.Name)
	left.Package = mergeStrings(left.Package, right.Package)
	left.File = mergeStrings(left.File, right.File)
	left.BlobSHA = mergeStrings(left.BlobSHA, right.BlobSHA)
	left.Signature = mergeStrings(left.Signature, right.Signature)
	left.Receiver = mergeStrings(left.Receiver, right.Receiver)
	left.Position = mergePositions(left.Position, right.Position)
	left.Exported = left.Exported || right.Exported
	left.IsTest = left.IsTest || right.IsTest
	left.Historical = left.Historical || right.Historical
	left.ChangeKind = mergeStrings(left.ChangeKind, right.ChangeKind)
	return left
}

func mergeNodeKinds(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	if left == string(graph.NodeSymbol) && right != left {
		return right
	}
	if right == string(graph.NodeSymbol) && left != right {
		return left
	}
	if right < left {
		return right
	}
	return left
}

func mergeStrings(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" || left == right || left < right {
		return left
	}
	return right
}

func mergePositions(left, right graphExportPosition) graphExportPosition {
	if left == (graphExportPosition{}) {
		return right
	}
	if right == (graphExportPosition{}) || positionLess(left, right) {
		return left
	}
	return right
}

func positionLess(left, right graphExportPosition) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.StartLine != right.StartLine {
		return left.StartLine < right.StartLine
	}
	if left.StartColumn != right.StartColumn {
		return left.StartColumn < right.StartColumn
	}
	if left.EndLine != right.EndLine {
		return left.EndLine < right.EndLine
	}
	return left.EndColumn < right.EndColumn
}

func appendUniquePositions(values []graphExportPosition, additions ...graphExportPosition) []graphExportPosition {
	if values == nil {
		values = make([]graphExportPosition, 0, len(additions))
	}
	for _, addition := range additions {
		seen := false
		for _, value := range values {
			if value == addition {
				seen = true
				break
			}
		}
		if !seen && addition != (graphExportPosition{}) {
			values = append(values, addition)
		}
	}
	sort.Slice(values, func(i, j int) bool { return positionLess(values[i], values[j]) })
	return values
}

func appendUniqueStrings(values []string, additions ...string) []string {
	if values == nil {
		values = make([]string, 0, len(additions))
	}
	for _, addition := range additions {
		if addition == "" {
			continue
		}
		seen := false
		for _, value := range values {
			if value == addition {
				seen = true
				break
			}
		}
		if !seen {
			values = append(values, addition)
		}
	}
	sort.Strings(values)
	return values
}

func nonEmptyDetails(value string) []string {
	if value == "" {
		return make([]string, 0)
	}
	return []string{value}
}

func renderGraphExport(output io.Writer, format graphOutputFormat, export graphExport) error {
	switch format {
	case graphOutputJSON:
		return writeGraphJSON(output, export)
	case graphOutputDOT:
		return writeGraphDOT(output, export)
	default:
		return fmt.Errorf("%w: unsupported graph output format %q", errUsage, format)
	}
}

func writeGraphJSON(output io.Writer, export graphExport) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(export); err != nil {
		return fmt.Errorf("encode graph JSON: %w", err)
	}
	return nil
}
