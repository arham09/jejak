// Package golang implements Jejak's committed Go package and semantic analyzer.
package golang

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/arham09/jejak/internal/graph"
)

// BuildFingerprint computes the semantic build identity without invoking a
// package load. It is safe to call repeatedly for the same snapshot.
func (a *Analyzer) BuildFingerprint(ctx context.Context, input graph.AnalyzeInput) (string, error) {
	input = normalizeInput(input)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if input.Root == "" {
		return "", errors.New("go analysis snapshot root is empty")
	}
	plan, _ := discoverPlan(input.Root)
	// A malformed build file still receives a deterministic fingerprint so a
	// failed generation can be diagnosed and retried after correction.
	return a.fingerprint(ctx, input, plan)
}

// Analyze loads all supported module/workspace packages from input.Root and
// converts declarations, references, imports, and test identities into
// language-neutral graph records.
func (a *Analyzer) Analyze(ctx context.Context, input graph.AnalyzeInput) (graph.AnalysisResult, error) {
	input = normalizeInput(input)
	result := graph.AnalysisResult{AnalyzerVersion: analyzerVersion}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if input.Root == "" {
		return result, errors.New("go analysis snapshot root is empty")
	}
	plan, planDiagnostics := discoverPlan(input.Root)
	fingerprint, fingerprintErr := a.fingerprint(ctx, input, plan)
	if fingerprintErr != nil {
		return result, fingerprintErr
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.BuildFingerprint = fingerprint
	result.Diagnostics = append(result.Diagnostics, planDiagnostics...)
	if len(planDiagnostics) > 0 {
		return result.Normalize(), nil
	}
	loaded, loadDiagnostics, err := a.load(ctx, input, plan)
	if err != nil {
		return result, err
	}
	result.Diagnostics = append(result.Diagnostics, loadDiagnostics...)
	extractor := newExtractor(input, loaded)
	result = extractor.extract(ctx, result)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result.Normalize(), nil
}

func normalizeInput(input graph.AnalyzeInput) graph.AnalyzeInput {
	if input.Snapshot != nil {
		if input.Root == "" {
			input.Root = input.Snapshot.Root
		}
		if len(input.Files) == 0 {
			input.Files = append([]graph.SnapshotFile(nil), input.Snapshot.Files...)
		}
		if input.Commit == "" {
			input.Commit = input.Snapshot.Commit
		}
	}
	return input
}

type extractor struct {
	input              graph.AnalyzeInput
	loaded             []*packages.Package
	manifest           map[string]graph.SnapshotFile
	packages           map[string]*packageInfo
	importOwners       map[string]string
	objects            map[types.Object]string
	objectsByCanonical map[string]string
	symbolsByKey       map[string]graph.Symbol
	literals           map[*ast.FuncLit]string
	implementers       map[string]map[string]struct{}
	result             graph.AnalysisResult
	calls              []callFact
	edges              map[string]struct{}
	nodes              map[string]struct{}
	symbols            map[string]struct{}
	packagesSeen       map[string]struct{}
	filesSeen          map[string]struct{}
	positions          map[string][]filePosition
}

func newExtractor(input graph.AnalyzeInput, loaded []*packages.Package) *extractor {
	manifest := make(map[string]graph.SnapshotFile, len(input.Files))
	for _, file := range input.Files {
		manifest[filepath.ToSlash(file.Path)] = file
	}
	return &extractor{
		input:              input,
		loaded:             loaded,
		manifest:           manifest,
		packages:           make(map[string]*packageInfo),
		importOwners:       make(map[string]string),
		objects:            make(map[types.Object]string),
		objectsByCanonical: make(map[string]string),
		symbolsByKey:       make(map[string]graph.Symbol),
		literals:           make(map[*ast.FuncLit]string),
		implementers:       make(map[string]map[string]struct{}),
		edges:              make(map[string]struct{}),
		nodes:              make(map[string]struct{}),
		symbols:            make(map[string]struct{}),
		packagesSeen:       make(map[string]struct{}),
		filesSeen:          make(map[string]struct{}),
		positions:          make(map[string][]filePosition),
	}
}

func (e *extractor) extract(ctx context.Context, result graph.AnalysisResult) graph.AnalysisResult {
	e.result = result
	repositoryKey := repositoryNodeKey(string(e.input.Repository))
	e.addNode(graph.Node{Key: repositoryKey, Kind: graph.NodeRepository, Owned: true})
	blobsSeen := make(map[string]struct{}, len(e.input.Files))
	for _, file := range e.input.Files {
		if file.BlobSHA == "" {
			continue
		}
		if _, exists := blobsSeen[file.BlobSHA]; exists {
			continue
		}
		blobsSeen[file.BlobSHA] = struct{}{}
		format := file.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		e.result.Blobs = append(e.result.Blobs, graph.Blob{SHA: file.BlobSHA, ObjectFormat: format, ByteSize: file.Size})
	}
	e.collectPackages(ctx)
	e.collectSymbols(ctx)
	e.collectImports()
	e.collectCalls(ctx)
	e.collectInterfaces(ctx)
	e.collectDispatchCalls()
	e.collectTests()
	return e.result
}

func (e *extractor) collectPackages(ctx context.Context) {
	ordered := append([]*packages.Package(nil), e.loaded...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, pkg := range ordered {
		if err := ctx.Err(); err != nil {
			return
		}
		if pkg == nil || pkg.PkgPath == "" || pkg.Fset == nil {
			continue
		}
		variant, isTest := packageVariant(pkg)
		key := packageKey(pkg.PkgPath, variant)
		info := e.packages[key]
		if info == nil {
			basePath := pkg.PkgPath
			if pkg.ForTest != "" {
				basePath = pkg.ForTest
			}
			info = &packageInfo{pkg: pkg, key: key, basePath: basePath, variant: variant, isTest: isTest}
			e.packages[key] = info
		}
		paths := pkg.CompiledGoFiles
		if len(paths) == 0 {
			paths = pkg.GoFiles
		}
		for index, filePath := range paths {
			if index >= len(pkg.Syntax) || pkg.Syntax[index] == nil {
				continue
			}
			relative, ok := e.relativePath(filePath)
			if !ok {
				continue
			}
			if isTest && !isGoTestPath(relative) {
				continue
			}
			if _, exists := e.filesSeen[relative]; exists {
				// A production package and a test variant can share compiled
				// files. Production facts are canonical; test variants retain
				// only their _test.go declarations.
				continue
			}
			manifest, exists := e.manifest[relative]
			if !exists {
				// Package loading may expose generated/absolute files not part of
				// the committed tree. Never let those bytes enter a generation.
				continue
			}
			fileKey := fileNodeKey(relative)
			loadedFile := loadedFile{path: relative, file: pkg.Syntax[index], fileKey: fileKey, blob: manifest, isTest: isGoTestPath(relative)}
			info.files = append(info.files, loadedFile)
			e.filesSeen[relative] = struct{}{}
		}
		sort.Slice(info.files, func(i, j int) bool { return info.files[i].path < info.files[j].path })
		if len(info.files) == 0 {
			continue
		}
		if _, exists := e.packagesSeen[key]; !exists {
			e.packagesSeen[key] = struct{}{}
			e.result.Packages = append(e.result.Packages, graph.Package{Key: key, ImportPath: pkg.PkgPath, ModulePath: packageModulePath(pkg), Directory: e.packageDirectory(info.files), Name: pkg.Name, Variant: variant})
			e.addNode(graph.Node{Key: packageNodeKey(key), Kind: graph.NodePackage, Owned: true, PackageKey: key})
			e.addEdge(graph.Edge{SourceKey: repositoryNodeKey(string(e.input.Repository)), TargetKey: packageNodeKey(key), Kind: graph.EdgeContains, Confidence: graph.ConfidenceExact, OwnerPackage: key})
			if variant == "production" {
				e.importOwners[pkg.PkgPath] = key
			}
		}
		for _, file := range info.files {
			if _, exists := e.filesSeen[file.path+"\x00record"]; exists {
				continue
			}
			e.filesSeen[file.path+"\x00record"] = struct{}{}
			e.result.Files = append(e.result.Files, graph.File{Key: file.fileKey, Path: file.path, BlobSHA: file.blob.BlobSHA, PackageKey: key, IsTest: file.isTest})
			e.addNode(graph.Node{Key: file.fileKey, Kind: graph.NodeFile, Owned: true, PackageKey: key, SourceBlob: file.blob.BlobSHA})
			e.addEdge(graph.Edge{SourceKey: packageNodeKey(key), TargetKey: file.fileKey, Kind: graph.EdgeContains, Confidence: graph.ConfidenceExact, OwnerPackage: key})
		}
	}
}

func (e *extractor) collectSymbols(ctx context.Context) {
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
		if info == nil || info.pkg == nil || len(info.files) == 0 {
			continue
		}
		initOrdinals := 0
		for _, file := range info.files {
			positions := make([]filePosition, 0)
			for _, decl := range file.file.Decls {
				switch declaration := decl.(type) {
				case *ast.FuncDecl:
					if declaration.Name == nil {
						continue
					}
					name := declaration.Name.Name
					ordinal := 0
					if name == "init" {
						initOrdinals++
						ordinal = initOrdinals
					}
					kind := graph.NodeFunction
					receiver := ""
					if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
						kind = graph.NodeMethod
						receiver = receiverString(info.pkg, declaration.Recv.List[0].Type)
					}
					if file.isTest && isTestName(name) {
						kind = graph.NodeTest
					}
					object := definitionObject(info.pkg, declaration.Name)
					symbol := e.makeSymbol(info, file, declaration, declaration.Name, name, kind, receiver, ordinal, object)
					e.addSymbol(symbol, declaration, object, &positions)
				case *ast.GenDecl:
					for _, spec := range declaration.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if ok && typeSpec.Name != nil {
							kind := graph.NodeType
							switch typeSpec.Type.(type) {
							case *ast.StructType:
								kind = graph.NodeStruct
							case *ast.InterfaceType:
								kind = graph.NodeInterface
							}
							object := definitionObject(info.pkg, typeSpec.Name)
							symbol := e.makeSymbol(info, file, typeSpec, typeSpec.Name, typeSpec.Name.Name, kind, "", 0, object)
							e.addSymbol(symbol, typeSpec, object, &positions)
							if structType, ok := typeSpec.Type.(*ast.StructType); ok && structType.Fields != nil {
								for _, field := range structType.Fields.List {
									for _, nameIdent := range field.Names {
										if nameIdent == nil || nameIdent.Name == "_" {
											continue
										}
										fieldObject := definitionObject(info.pkg, nameIdent)
										fieldSymbol := e.makeSymbol(info, file, field, nameIdent, nameIdent.Name, graph.NodeSymbol, "", 0, fieldObject)
										e.addSymbol(fieldSymbol, field, fieldObject, &positions)
									}
								}
							}
							if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok && interfaceType.Methods != nil {
								for _, field := range interfaceType.Methods.List {
									for _, nameIdent := range field.Names {
										if nameIdent == nil || nameIdent.Name == "_" {
											continue
										}
										methodObject := definitionObject(info.pkg, nameIdent)
										methodSymbol := e.makeSymbol(info, file, nameIdent, nameIdent, nameIdent.Name, graph.NodeMethod, "", 0, methodObject)
										e.addSymbol(methodSymbol, nameIdent, methodObject, &positions)
									}
								}
							}
							continue
						}
						valueSpec, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						kind := graph.NodeSymbol
						if declaration.Tok.String() == "const" {
							kind = graph.NodeSymbol
						}
						for _, nameIdent := range valueSpec.Names {
							if nameIdent == nil || nameIdent.Name == "_" {
								continue
							}
							object := definitionObject(info.pkg, nameIdent)
							symbol := e.makeSymbol(info, file, valueSpec, nameIdent, nameIdent.Name, kind, "", 0, object)
							e.addSymbol(symbol, valueSpec, object, &positions)
						}
					}
				}
			}
			// Anonymous function literals are source symbols too. Their source
			// position is part of the key, which keeps repeated closures distinct.
			ast.Inspect(file.file, func(node ast.Node) bool {
				literal, ok := node.(*ast.FuncLit)
				if !ok {
					return true
				}
				position := e.position(info.pkg.Fset, literal, file.path)
				name := fmt.Sprintf("$anon@%d:%d", position.StartLine, position.StartColumn)
				var signature string
				if info.pkg.TypesInfo != nil {
					if typ := info.pkg.TypesInfo.TypeOf(literal); typ != nil {
						signature = typ.String()
					}
				}
				symbol := graph.Symbol{Key: fmt.Sprintf("symbol:%s:function:%s:%d:%d", info.key, file.path, position.StartLine, position.StartColumn), Kind: graph.NodeFunction, PackageKey: info.key, FileKey: file.fileKey, Name: name, Signature: signature, Position: position, Exported: false}
				e.addSymbol(symbol, literal, nil, &positions)
				e.literals[literal] = symbol.Key
				return true
			})
			e.positions[file.fileKey] = append(e.positions[file.fileKey], positions...)
		}
	}
	// Resolve references only after every owned declaration has been mapped to
	// its go/types object. Package load order is not a dependency order, so a
	// one-pass extractor would incorrectly label forward cross-package uses as
	// external symbols.
	for _, key := range keys {
		info := e.packages[key]
		if info == nil {
			continue
		}
		for _, file := range info.files {
			e.collectReferences(info, file, e.positions[file.fileKey])
		}
	}
}

func (e *extractor) collectReferences(info *packageInfo, file loadedFile, positions []filePosition) {
	if info.pkg.TypesInfo == nil {
		return
	}
	ast.Inspect(file.file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok || ident == nil {
			return true
		}
		if _, defined := info.pkg.TypesInfo.Defs[ident]; defined {
			return true
		}
		object := info.pkg.TypesInfo.Uses[ident]
		if object == nil {
			return true
		}
		targetKey := e.ownedObjectTargetKey(object)
		if targetKey == "" {
			pkgPath := objectPackagePath(object)
			packageKey := externalPackageKey(pkgPath)
			e.addNode(graph.Node{Key: packageNodeKey(packageKey), Kind: graph.NodePackage, Owned: false, PackageKey: packageKey})
			targetKey = externalSymbolKey(pkgPath, object)
			e.addNode(graph.Node{Key: targetKey, Kind: graph.NodeSymbol, Owned: false, PackageKey: packageKey, SymbolKey: targetKey})
		}
		sourceKey := file.fileKey
		if owner := ownerAt(ident.Pos(), positions); owner != "" {
			sourceKey = owner
		}
		edge := graph.Edge{SourceKey: sourceKey, TargetKey: targetKey, Kind: graph.EdgeReferences, Confidence: graph.ConfidenceExact, OwnerPackage: info.key}
		e.addEdge(edge)
		position := e.position(info.pkg.Fset, ident, file.path)
		e.result.Evidence = append(e.result.Evidence, graph.EdgeEvidence{SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind, AnalyzerSource: analyzerVersion, SourceBlob: file.blob.BlobSHA, StartLine: position.StartLine, StartColumn: position.StartColumn, EndLine: position.EndLine, EndColumn: position.EndColumn})
		return true
	})
}

func (e *extractor) collectImports() {
	keys := make([]string, 0, len(e.packages))
	for key := range e.packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		info := e.packages[key]
		if info == nil || info.pkg == nil || len(info.files) == 0 {
			continue
		}
		imports := make([]string, 0, len(info.pkg.Imports))
		for importPath := range info.pkg.Imports {
			imports = append(imports, importPath)
		}
		sort.Strings(imports)
		for _, importPath := range imports {
			target := e.importOwners[importPath]
			owned := target != ""
			if !owned {
				target = externalPackageKey(importPath)
				e.addNode(graph.Node{Key: packageNodeKey(target), Kind: graph.NodePackage, Owned: false, PackageKey: target})
			}
			e.addEdge(graph.Edge{SourceKey: packageNodeKey(key), TargetKey: packageNodeKey(target), Kind: graph.EdgeImports, Confidence: graph.ConfidenceExact, OwnerPackage: key})
			e.result.PackageDependencies = append(e.result.PackageDependencies, graph.PackageDependency{SourcePackage: key, TargetPackage: target})
		}
	}
}

func (e *extractor) addSymbol(symbol graph.Symbol, declaration ast.Node, object types.Object, positions *[]filePosition) {
	if symbol.Key == "" {
		return
	}
	if _, exists := e.symbols[symbol.Key]; exists {
		return
	}
	e.symbols[symbol.Key] = struct{}{}
	e.symbolsByKey[symbol.Key] = symbol
	e.result.Symbols = append(e.result.Symbols, symbol)
	e.addNode(graph.Node{Key: symbolNodeKey(symbol.Key), Kind: symbol.Kind, Owned: true, PackageKey: symbol.PackageKey, FileKey: symbol.FileKey, SymbolKey: symbol.Key, SourceBlob: e.blobForFile(symbol.FileKey), SourceStart: symbol.Position.StartLine, SourceEnd: symbol.Position.EndLine})
	e.addEdge(graph.Edge{SourceKey: symbol.FileKey, TargetKey: symbol.Key, Kind: graph.EdgeDefines, Confidence: graph.ConfidenceExact, OwnerPackage: symbol.PackageKey})
	if object != nil {
		// A test variant can expose the same Go object as its production
		// package. Keep the production declaration as the canonical owner so
		// references from tests do not accidentally resolve to a duplicate.
		if existing, exists := e.objects[object]; !exists || e.testSymbolKey(existing) {
			e.objects[object] = symbol.Key
		}
		if canonical := objectCanonicalKey(object); canonical != "" {
			if existing, exists := e.objectsByCanonical[canonical]; !exists || e.testSymbolKey(existing) {
				e.objectsByCanonical[canonical] = symbol.Key
			}
		}
		if canonical := objectLooseCanonicalKey(object); canonical != "" {
			if existing, exists := e.objectsByCanonical[canonical]; !exists || e.testSymbolKey(existing) {
				e.objectsByCanonical[canonical] = symbol.Key
			}
		}
	}
	if declaration != nil {
		*positions = append(*positions, filePosition{start: declaration.Pos(), end: declaration.End(), symbolKey: symbol.Key})
	}
}

func (e *extractor) testSymbolKey(key string) bool {
	symbol, ok := e.symbolsByKey[key]
	return ok && strings.Contains(symbol.PackageKey, "#")
}

func (e *extractor) makeSymbol(info *packageInfo, file loadedFile, declaration ast.Node, nameIdent *ast.Ident, name string, kind graph.NodeKind, receiver string, ordinal int, object types.Object) graph.Symbol {
	position := e.position(info.pkg.Fset, declaration, file.path)
	keyName := name
	if name == "init" && ordinal > 0 {
		keyName = fmt.Sprintf("init#%d", ordinal)
	}
	if object != nil && object.Type() != nil {
		keyName = fmt.Sprintf("%s@%d:%d", keyName, position.StartLine, position.StartColumn)
	}
	key := fmt.Sprintf("symbol:%s:%s:%s:%s:%s", info.key, kind, receiver, keyName, position.Path)
	if nameIdent != nil {
		key += fmt.Sprintf(":%d:%d", position.StartLine, position.StartColumn)
	}
	signature := objectSignature(object)
	if signature == "" && info.pkg.TypesInfo != nil && nameIdent != nil {
		if typ := info.pkg.TypesInfo.TypeOf(nameIdent); typ != nil {
			signature = typ.String()
		}
	}
	exported := ast.IsExported(name)
	if kind == graph.NodeTest {
		exported = false
	}
	return graph.Symbol{Key: key, Kind: kind, PackageKey: info.key, FileKey: file.fileKey, Name: name, Signature: signature, Receiver: receiver, Position: position, Exported: exported}
}

func (e *extractor) position(fset *token.FileSet, node ast.Node, relative string) graph.Position {
	position := positionFor(fset, node)
	position.Path = relative
	return position
}

func (e *extractor) packageDirectory(files []loadedFile) string {
	if len(files) == 0 {
		return ""
	}
	return filepath.ToSlash(filepath.Dir(files[0].path))
}

func (e *extractor) blobForFile(fileKey string) string {
	path := strings.TrimPrefix(fileKey, "file:")
	if file, ok := e.manifest[path]; ok {
		return file.BlobSHA
	}
	return ""
}

func (e *extractor) relativePath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(e.input.Root, abs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func (e *extractor) addNode(node graph.Node) {
	if node.Key == "" {
		return
	}
	if _, exists := e.nodes[node.Key]; exists {
		return
	}
	e.nodes[node.Key] = struct{}{}
	e.result.Nodes = append(e.result.Nodes, node)
}

func (e *extractor) addEdge(edge graph.Edge) {
	if edge.SourceKey == "" || edge.TargetKey == "" || edge.Kind == "" {
		return
	}
	key := fmt.Sprintf("%s\x00%s\x00%s", edge.SourceKey, edge.TargetKey, edge.Kind)
	if _, exists := e.edges[key]; exists {
		return
	}
	e.edges[key] = struct{}{}
	e.result.Edges = append(e.result.Edges, edge)
}

func ownerAt(position token.Pos, positions []filePosition) string {
	var owner string
	var width token.Pos
	for _, candidate := range positions {
		if candidate.symbolKey == "" || position < candidate.start || position > candidate.end {
			continue
		}
		candidateWidth := candidate.end - candidate.start
		if owner == "" || candidateWidth < width {
			owner = candidate.symbolKey
			width = candidateWidth
		}
	}
	return owner
}

func definitionObject(pkg *packages.Package, ident *ast.Ident) types.Object {
	if pkg == nil || pkg.TypesInfo == nil || ident == nil {
		return nil
	}
	return pkg.TypesInfo.Defs[ident]
}

func packageModulePath(pkg *packages.Package) string {
	if pkg == nil || pkg.Module == nil {
		return ""
	}
	return pkg.Module.Path
}

func receiverString(pkg *packages.Package, expr ast.Expr) string {
	if pkg == nil || pkg.TypesInfo == nil || expr == nil {
		return ""
	}
	if typ := pkg.TypesInfo.TypeOf(expr); typ != nil {
		return typ.String()
	}
	return ""
}

func isTestName(name string) bool {
	return (strings.HasPrefix(name, "Test") && len(name) > len("Test")) ||
		(strings.HasPrefix(name, "Benchmark") && len(name) > len("Benchmark")) ||
		(strings.HasPrefix(name, "Example") && len(name) > len("Example"))
}
