package golang

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/arham09/jejak/internal/graph"
)

const analyzerVersion = "go-packages-v1"

// Analyzer loads Go packages from committed snapshots and emits Jejak graph
// records. It owns all go/ast, go/types, and x/tools objects; none escape the
// package boundary.
type Analyzer struct{}

var (
	_ graph.Analyzer      = (*Analyzer)(nil)
	_ graph.Fingerprinter = (*Analyzer)(nil)
)

// New returns the concrete Go analyzer.
func New() *Analyzer { return &Analyzer{} }

// Version identifies the semantic extraction implementation for fingerprints.
func (a *Analyzer) Version() string { return analyzerVersion }

// Supports reports whether path names a Go source/configuration file that the
// analyzer understands.
func (a *Analyzer) Supports(path string) bool {
	switch filepath.Ext(path) {
	case ".go":
		return true
	default:
		base := filepath.Base(path)
		return base == "go.mod" || base == "go.work" || base == "go.sum" || base == "go.work.sum"
	}
}

type packageInfo struct {
	pkg      *packages.Package
	key      string
	basePath string
	variant  string
	isTest   bool
	files    []loadedFile
}

type loadedFile struct {
	path    string
	file    *ast.File
	fileKey string
	blob    graph.SnapshotFile
	isTest  bool
}

type filePosition struct {
	start     token.Pos
	end       token.Pos
	symbolKey string
}

func packageVariant(pkg *packages.Package) (variant string, isTest bool) {
	variant = "production"
	if pkg == nil {
		return variant, false
	}
	if pkg.ForTest != "" {
		variant = "internal-test"
		isTest = true
	}
	if strings.HasSuffix(pkg.Name, "_test") {
		variant = "external-test"
		isTest = true
	}
	if strings.Contains(pkg.ID, ".testmain") || strings.HasSuffix(pkg.ID, ".test") {
		variant = "test-main"
		isTest = true
	}
	return variant, isTest
}

func packageKey(importPath, variant string) string {
	if variant == "production" {
		return "go:package:" + importPath
	}
	return "go:package:" + importPath + "#" + variant
}

func packageNodeKey(packageKey string) string { return "package:" + packageKey }

func fileNodeKey(path string) string { return "file:" + filepath.ToSlash(path) }

func repositoryNodeKey(repo string) string { return "repository:" + repo }

func symbolNodeKey(key string) string { return key }

func externalPackageKey(importPath string) string {
	if importPath == "" {
		importPath = "builtin"
	}
	return "external:package:" + importPath
}

func externalSymbolKey(pkgPath string, obj types.Object) string {
	if obj == nil {
		return "external:symbol:" + pkgPath + ":unknown"
	}
	kind := "object"
	switch obj.(type) {
	case *types.Func:
		kind = "func"
	case *types.TypeName:
		kind = "type"
	case *types.Var:
		kind = "var"
	case *types.Const:
		kind = "const"
	case *types.Label:
		kind = "label"
	}
	return fmt.Sprintf("external:symbol:%s:%s:%s", pkgPath, kind, obj.Name())
}

func objectPackagePath(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return "builtin"
	}
	return obj.Pkg().Path()
}

func objectSignature(obj types.Object) string {
	if obj == nil || obj.Type() == nil {
		return ""
	}
	return obj.Type().String()
}

// objectCanonicalKey is a stable fallback identity for go/types objects that
// were reconstructed by another package type-checking universe (notably
// external _test packages). Restricting the fallback to declarations whose
// identity is safe to compare avoids accidentally mapping local variables to
// package-level symbols with the same name.
func objectCanonicalKey(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	pkgPath := obj.Pkg().Path()
	switch object := obj.(type) {
	case *types.Func:
		return fmt.Sprintf("%s\x00func\x00%s\x00%s", pkgPath, object.FullName(), objectSignature(obj))
	case *types.TypeName:
		return fmt.Sprintf("%s\x00type\x00%s\x00%s", pkgPath, object.Name(), objectSignature(obj))
	default:
		return ""
	}
}

// objectLooseCanonicalKey bridges instantiated generic declarations and
// methods. Go creates distinct objects and substitutes type arguments into
// their signatures, while the graph owns the source declaration.
func objectLooseCanonicalKey(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	pkgPath := obj.Pkg().Path()
	switch object := obj.(type) {
	case *types.Func:
		return fmt.Sprintf("%s\x00func-loose\x00%s", pkgPath, stripTypeArguments(object.FullName()))
	case *types.TypeName:
		return fmt.Sprintf("%s\x00type-loose\x00%s", pkgPath, object.Name())
	default:
		return ""
	}
}

func stripTypeArguments(value string) string {
	var result strings.Builder
	depth := 0
	for _, character := range value {
		switch character {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				result.WriteRune(character)
			}
		}
	}
	return result.String()
}

func positionFor(fset *token.FileSet, node ast.Node) graph.Position {
	if fset == nil || node == nil {
		return graph.Position{}
	}
	start := fset.PositionFor(node.Pos(), false)
	end := fset.PositionFor(node.End(), false)
	return graph.Position{Path: filepath.ToSlash(start.Filename), StartLine: start.Line, StartColumn: start.Column, EndLine: end.Line, EndColumn: end.Column}
}

func isGoTestPath(path string) bool { return strings.HasSuffix(path, "_test.go") }
