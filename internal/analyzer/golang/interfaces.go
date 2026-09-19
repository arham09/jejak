package golang

import (
	"context"
	"go/ast"
	"go/types"
	"sort"

	"github.com/arham09/jejak/internal/graph"
)

type interfaceCandidate struct {
	object types.Object
	typ    types.Type
	key    string
}

type concreteCandidate struct {
	object      *types.TypeName
	typ         types.Type
	key         string
	packageKey  string
	variantName string
}

type instanceFact struct {
	ident    *ast.Ident
	instance types.Instance
}

// collectInterfaces records proven type/method implementation relationships.
// It considers interfaces declared in source packages and directly imported
// packages; implementations are restricted to source-owned types so an
// external module never appears to be part of the repository graph.
func (e *extractor) collectInterfaces(ctx context.Context) {
	interfaces := e.interfaceCandidates()
	concretes := e.concreteCandidates()
	for _, concrete := range concretes {
		if err := ctx.Err(); err != nil {
			return
		}
		for _, iface := range interfaces {
			if sameTypeObject(concrete.object, iface.object) {
				continue
			}
			implementationType, mode, ok := implementsType(concrete.typ, iface.typ)
			if !ok {
				continue
			}
			e.addImplementation(concrete, iface, implementationType, mode)
		}
	}
}

// collectDispatchCalls adds possible runtime targets for statically resolved
// interface method calls. The exact call edge remains present and explains
// the compile-time target; possible edges make known implementations visible
// without presenting dynamic dispatch as certain.
func (e *extractor) collectDispatchCalls() {
	for _, call := range e.calls {
		implementers := e.implementers[call.targetKey]
		if len(implementers) == 0 {
			continue
		}
		targets := make([]string, 0, len(implementers))
		for target := range implementers {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			if target == call.targetKey {
				continue
			}
			edge := graph.Edge{SourceKey: call.sourceKey, TargetKey: target, Kind: graph.EdgePossibleCall, Confidence: graph.ConfidencePossible, OwnerPackage: call.ownerPackage}
			e.addEdge(edge)
			details := "interface dispatch candidate"
			if call.details != "" {
				details += "; " + call.details
			}
			e.result.Evidence = append(e.result.Evidence, graph.EdgeEvidence{
				SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind,
				AnalyzerSource: analyzerVersion, SourceBlob: call.blob.BlobSHA,
				StartLine: call.position.StartLine, StartColumn: call.position.StartColumn,
				EndLine: call.position.EndLine, EndColumn: call.position.EndColumn, Details: details,
			})
		}
	}
}

func (e *extractor) addImplementation(concrete concreteCandidate, iface interfaceCandidate, implementationType types.Type, mode string) {
	if concrete.key == "" || iface.key == "" {
		return
	}
	concreteSymbol, exists := e.symbolsByKey[concrete.key]
	if !exists {
		return
	}
	edge := graph.Edge{SourceKey: concrete.key, TargetKey: iface.key, Kind: graph.EdgeImplements, Confidence: graph.ConfidenceExact, OwnerPackage: concrete.packageKey}
	e.addEdge(edge)
	e.addEvidence(edge, concreteSymbol, "implements "+mode+" method set")

	interfaceType := interfaceUnderlying(iface.typ)
	if interfaceType == nil {
		return
	}
	interfaceType.Complete()
	methodSet := types.NewMethodSet(implementationType)
	for index := 0; index < interfaceType.NumMethods(); index++ {
		interfaceMethod := interfaceType.Method(index)
		if interfaceMethod == nil {
			continue
		}
		selection := methodSet.Lookup(interfaceMethod.Pkg(), interfaceMethod.Name())
		if selection == nil || selection.Obj() == nil {
			continue
		}
		concreteMethodKey := e.objectTargetKey(selection.Obj())
		interfaceMethodKey := e.objectTargetKey(interfaceMethod)
		if concreteMethodKey == "" || interfaceMethodKey == "" || concreteMethodKey == interfaceMethodKey {
			continue
		}
		methodEdge := graph.Edge{SourceKey: concreteMethodKey, TargetKey: interfaceMethodKey, Kind: graph.EdgeImplements, Confidence: graph.ConfidenceExact, OwnerPackage: concrete.packageKey}
		e.addEdge(methodEdge)
		e.addEvidence(methodEdge, concreteSymbol, "implements "+mode+" method set")
		// Interface-to-interface satisfaction is useful as a type relationship,
		// but an interface is not a concrete runtime dispatch candidate. Only
		// named non-interface types participate in possible call expansion.
		if !isInterfaceType(concrete.typ) {
			if e.implementers[interfaceMethodKey] == nil {
				e.implementers[interfaceMethodKey] = make(map[string]struct{})
			}
			e.implementers[interfaceMethodKey][concreteMethodKey] = struct{}{}
		}
	}
}

func (e *extractor) addEvidence(edge graph.Edge, source graph.Symbol, details string) {
	e.result.Evidence = append(e.result.Evidence, graph.EdgeEvidence{
		SourceKey: edge.SourceKey, TargetKey: edge.TargetKey, Kind: edge.Kind,
		AnalyzerSource: analyzerVersion, SourceBlob: e.blobForFile(source.FileKey),
		StartLine: source.Position.StartLine, StartColumn: source.Position.StartColumn,
		EndLine: source.Position.EndLine, EndColumn: source.Position.EndColumn, Details: details,
	})
}

func (e *extractor) interfaceCandidates() []interfaceCandidate {
	candidates := make(map[types.Object]interfaceCandidate)
	addPackage := func(pkg *types.Package) {
		if pkg == nil || pkg.Scope() == nil {
			return
		}
		for _, name := range pkg.Scope().Names() {
			object := pkg.Scope().Lookup(name)
			typeName, ok := object.(*types.TypeName)
			if !ok || !isInterfaceType(typeName.Type()) || !usableInterfaceType(typeName.Type()) {
				continue
			}
			key := e.objectTargetKey(typeName)
			if key == "" {
				continue
			}
			candidates[object] = interfaceCandidate{object: object, typ: typeName.Type(), key: key}
		}
	}
	addInstances := func(info *types.Info) {
		for _, fact := range sortedInstanceFacts(info) {
			ident, instance := fact.ident, fact.instance
			if ident == nil || instance.Type == nil || !isInterfaceType(instance.Type) || !usableInterfaceType(instance.Type) {
				continue
			}
			object, ok := info.Uses[ident].(*types.TypeName)
			if !ok {
				continue
			}
			key := e.objectTargetKey(object)
			if key == "" {
				continue
			}
			candidates[object] = interfaceCandidate{object: object, typ: instance.Type, key: key}
		}
	}
	packageKeys := make([]string, 0, len(e.packages))
	for key := range e.packages {
		packageKeys = append(packageKeys, key)
	}
	sort.Strings(packageKeys)
	for _, key := range packageKeys {
		info := e.packages[key]
		if info == nil || info.pkg == nil || info.variant != "production" {
			continue
		}
		addPackage(info.pkg.Types)
		addInstances(info.pkg.TypesInfo)
		imports := make([]string, 0, len(info.pkg.Imports))
		for importPath := range info.pkg.Imports {
			imports = append(imports, importPath)
		}
		sort.Strings(imports)
		for _, importPath := range imports {
			if imported := info.pkg.Imports[importPath]; imported != nil {
				addPackage(imported.Types)
			}
		}
	}
	result := make([]interfaceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key < result[j].key })
	return result
}

func (e *extractor) concreteCandidates() []concreteCandidate {
	seen := make(map[string]concreteCandidate)
	packageKeys := make([]string, 0, len(e.packages))
	for key := range e.packages {
		packageKeys = append(packageKeys, key)
	}
	sort.Strings(packageKeys)
	for _, packageKey := range packageKeys {
		info := e.packages[packageKey]
		if info == nil || info.pkg == nil || info.pkg.Types == nil || info.variant != "production" {
			continue
		}
		scope := info.pkg.Types.Scope()
		if scope == nil {
			continue
		}
		for _, name := range scope.Names() {
			typeName, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || !usableConcreteType(typeName.Type()) {
				continue
			}
			key := e.ownedObjectTargetKey(typeName)
			if key == "" {
				continue
			}
			seen[key] = concreteCandidate{object: typeName, typ: typeName.Type(), key: key, packageKey: packageKey, variantName: "declared"}
		}
		if info.pkg.TypesInfo == nil {
			continue
		}
		for _, fact := range sortedInstanceFacts(info.pkg.TypesInfo) {
			ident, instance := fact.ident, fact.instance
			if ident == nil || instance.Type == nil {
				continue
			}
			object, ok := info.pkg.TypesInfo.Uses[ident].(*types.TypeName)
			if !ok {
				continue
			}
			key := e.ownedObjectTargetKey(object)
			if key == "" || !usableConcreteType(instance.Type) {
				continue
			}
			// The graph identity remains the declared type. Keep one candidate per
			// source type; generic instantiations do not create a second semantic
			// declaration edge.
			if _, exists := seen[key]; !exists {
				seen[key] = concreteCandidate{object: object, typ: instance.Type, key: key, packageKey: packageKey, variantName: "instantiated"}
			}
		}
	}
	result := make([]concreteCandidate, 0, len(seen))
	for _, candidate := range seen {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].key != result[j].key {
			return result[i].key < result[j].key
		}
		return result[i].variantName < result[j].variantName
	})
	return result
}

func sortedInstanceFacts(info *types.Info) []instanceFact {
	if info == nil {
		return nil
	}
	instances := make([]instanceFact, 0, len(info.Instances))
	for ident, instance := range info.Instances {
		instances = append(instances, instanceFact{ident: ident, instance: instance})
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].ident == nil || instances[j].ident == nil {
			return instances[i].ident != nil
		}
		if instances[i].ident.Pos() != instances[j].ident.Pos() {
			return instances[i].ident.Pos() < instances[j].ident.Pos()
		}
		return instances[i].ident.Name < instances[j].ident.Name
	})
	return instances
}

func implementsType(candidate, iface types.Type) (types.Type, string, bool) {
	if candidate == nil || iface == nil || !isInterfaceType(iface) || !usableInterfaceType(iface) {
		return nil, "", false
	}
	if types.Identical(types.Unalias(candidate), types.Unalias(iface)) {
		return nil, "", false
	}
	if safeImplements(candidate, iface) {
		return candidate, "value", true
	}
	named, ok := types.Unalias(candidate).(*types.Named)
	if !ok || isInterfaceType(named) {
		return nil, "", false
	}
	pointer := types.NewPointer(named)
	if safeImplements(pointer, iface) {
		return pointer, "pointer", true
	}
	return nil, "", false
}

func safeImplements(candidate, iface types.Type) (ok bool) {
	interfaceType := interfaceUnderlying(iface)
	if interfaceType == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return types.Implements(candidate, interfaceType)
}

func interfaceUnderlying(typ types.Type) *types.Interface {
	if typ == nil {
		return nil
	}
	underlying := types.Unalias(typ).Underlying()
	interfaceType, _ := underlying.(*types.Interface)
	return interfaceType
}

func isInterfaceType(typ types.Type) bool { return interfaceUnderlying(typ) != nil }

func usableInterfaceType(typ types.Type) bool {
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok {
		return true
	}
	return fullyInstantiated(named)
}

func usableConcreteType(typ types.Type) bool {
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok {
		return false
	}
	return fullyInstantiated(named)
}

func fullyInstantiated(named *types.Named) bool {
	if named == nil || named.TypeParams() == nil || named.TypeParams().Len() == 0 {
		return true
	}
	return named.TypeArgs() != nil && named.TypeArgs().Len() == named.TypeParams().Len()
}

func sameTypeObject(left, right types.Object) bool {
	if left == nil || right == nil {
		return false
	}
	return left == right || (left.Pkg() != nil && right.Pkg() != nil && left.Pkg().Path() == right.Pkg().Path() && left.Name() == right.Name())
}
