package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

type callResolutionKind uint8

const (
	callUnresolved callResolutionKind = iota
	callExact
	callNotSemantic
)

type callResolution struct {
	kind   callResolutionKind
	key    string
	object types.Object
}

type callFact struct {
	sourceKey    string
	targetKey    string
	targetObject types.Object
	ownerPackage string
	position     graph.Position
	blob         graph.SnapshotFile
	details      string
}

// collectCalls resolves call expressions from go/types information. It keeps
// static interface method calls exact and lets interface analysis add separate
// possible dispatch edges later. Function values whose target cannot be
// proven are represented by explicit unresolved-call nodes.
func (e *extractor) collectCalls(ctx context.Context) {
	keys := make([]string, 0, len(e.packages))
	for key := range e.packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return
		}
		info := e.packages[key]
		if info == nil || info.pkg == nil || info.pkg.TypesInfo == nil {
			continue
		}
		for _, file := range info.files {
			modes := make(map[*ast.CallExpr]string)
			ast.Inspect(file.file, func(node ast.Node) bool {
				switch statement := node.(type) {
				case *ast.GoStmt:
					if statement.Call != nil {
						modes[statement.Call] = "go"
					}
				case *ast.DeferStmt:
					if statement.Call != nil {
						modes[statement.Call] = "defer"
					}
				}
				return true
			})
			ast.Inspect(file.file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || call == nil {
					return true
				}
				position := e.position(info.pkg.Fset, call, file.path)
				sourceKey := ownerAt(call.Pos(), e.positions[file.fileKey])
				if sourceKey == "" {
					sourceKey = file.fileKey
				}
				resolution := e.resolveCall(info, call)
				switch resolution.kind {
				case callNotSemantic:
					return true
				case callExact:
					details := modes[call]
					if details == "" {
						details = "direct call"
					}
					edge := graph.Edge{SourceKey: sourceKey, TargetKey: resolution.key, Kind: graph.EdgeCalls, Confidence: graph.ConfidenceExact, OwnerPackage: info.key}
					e.addEdge(edge)
					e.result.Evidence = append(e.result.Evidence, graph.EdgeEvidence{
						SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind,
						AnalyzerSource: analyzerVersion, SourceBlob: file.blob.BlobSHA,
						StartLine: position.StartLine, StartColumn: position.StartColumn,
						EndLine: position.EndLine, EndColumn: position.EndColumn, Details: details,
					})
					e.calls = append(e.calls, callFact{sourceKey: sourceKey, targetKey: resolution.key, targetObject: resolution.object, ownerPackage: info.key, position: position, blob: file.blob, details: details})
				case callUnresolved:
					targetKey := unresolvedCallKey(info.key, file.path, position)
					e.addNode(graph.Node{Key: targetKey, Kind: graph.NodeSymbol, Owned: false, PackageKey: info.key, FileKey: file.fileKey, SourceBlob: file.blob.BlobSHA, SourceStart: position.StartLine, SourceEnd: position.EndLine})
					edge := graph.Edge{SourceKey: sourceKey, TargetKey: targetKey, Kind: graph.EdgeUnresolvedCall, Confidence: graph.ConfidencePossible, OwnerPackage: info.key}
					e.addEdge(edge)
					details := modes[call]
					if details == "" {
						details = "unknown function value"
					}
					e.result.Evidence = append(e.result.Evidence, graph.EdgeEvidence{
						SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind,
						AnalyzerSource: analyzerVersion, SourceBlob: file.blob.BlobSHA,
						StartLine: position.StartLine, StartColumn: position.StartColumn,
						EndLine: position.EndLine, EndColumn: position.EndColumn, Details: details,
					})
				}
				return true
			})
		}
	}
}

func (e *extractor) resolveCall(info *packageInfo, call *ast.CallExpr) callResolution {
	if info == nil || info.pkg == nil || info.pkg.TypesInfo == nil || call == nil {
		return callResolution{kind: callUnresolved}
	}
	fun := unwrapCallExpression(call.Fun)
	switch expression := fun.(type) {
	case *ast.FuncLit:
		if key := e.literals[expression]; key != "" {
			return callResolution{kind: callExact, key: key}
		}
		return callResolution{kind: callUnresolved}
	case *ast.Ident:
		return e.resolveCallObject(info.pkg.TypesInfo.Uses[expression])
	case *ast.SelectorExpr:
		if selection := info.pkg.TypesInfo.Selections[expression]; selection != nil {
			return e.resolveCallObject(selection.Obj())
		}
		return e.resolveCallObject(info.pkg.TypesInfo.Uses[expression.Sel])
	default:
		return callResolution{kind: callUnresolved}
	}
}

func (e *extractor) resolveCallObject(object types.Object) callResolution {
	switch object := object.(type) {
	case *types.Func:
		return callResolution{kind: callExact, key: e.objectTargetKey(object), object: object}
	case *types.Builtin, *types.TypeName:
		// Built-ins and conversions are useful identifier references, but they
		// are not ordinary function-call graph edges.
		return callResolution{kind: callNotSemantic}
	case *types.Var:
		if isFunctionType(object.Type()) {
			return callResolution{kind: callUnresolved}
		}
		return callResolution{kind: callNotSemantic}
	case nil:
		return callResolution{kind: callUnresolved}
	default:
		if isFunctionType(object.Type()) {
			return callResolution{kind: callUnresolved}
		}
		return callResolution{kind: callUnresolved}
	}
}

func (e *extractor) objectTargetKey(object types.Object) string {
	if object == nil {
		return ""
	}
	if key := e.ownedObjectTargetKey(object); key != "" {
		return key
	}
	packagePath := objectPackagePath(object)
	key := externalSymbolKey(packagePath, object)
	e.addNode(graph.Node{Key: packageNodeKey(externalPackageKey(packagePath)), Kind: graph.NodePackage, Owned: false, PackageKey: externalPackageKey(packagePath)})
	e.addNode(graph.Node{Key: key, Kind: graph.NodeSymbol, Owned: false, PackageKey: externalPackageKey(packagePath), SymbolKey: key})
	return key
}

// ownedObjectTargetKey resolves objects declared by source packages. go/types
// may produce distinct object pointers for an external test package and its
// production package, so pointer identity alone is not sufficient here.
func (e *extractor) ownedObjectTargetKey(object types.Object) string {
	if object == nil {
		return ""
	}
	if key := e.objects[object]; key != "" {
		return key
	}
	if canonical := objectCanonicalKey(object); canonical != "" {
		if key := e.objectsByCanonical[canonical]; key != "" {
			return key
		}
	}
	if canonical := objectLooseCanonicalKey(object); canonical != "" {
		return e.objectsByCanonical[canonical]
	}
	return ""
}

func isFunctionType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	_, ok := types.Unalias(typ).Underlying().(*types.Signature)
	return ok
}

func unwrapCallExpression(expression ast.Expr) ast.Expr {
	for expression != nil {
		switch value := expression.(type) {
		case *ast.ParenExpr:
			expression = value.X
		case *ast.IndexExpr:
			expression = value.X
		case *ast.IndexListExpr:
			expression = value.X
		default:
			return expression
		}
	}
	return nil
}

func unresolvedCallKey(packageKey, path string, position graph.Position) string {
	return fmt.Sprintf("unresolved:call:%s:%s:%d:%d", packageKey, strings.ReplaceAll(path, "\\", "/"), position.StartLine, position.StartColumn)
}
