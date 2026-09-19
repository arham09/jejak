package golang

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

const syntaxParserVersion = "go-syntax-v1"

// SyntaxCacheEntries emits source-relative, serializable syntax summaries for
// tracked Go blobs. It deliberately omits go/types objects and package
// identity so the result can be reused across worktrees while semantic
// analysis still runs for each build context.
func (a *Analyzer) SyntaxCacheEntries(ctx context.Context, input graph.AnalyzeInput) ([]graph.SyntaxCacheEntry, error) {
	input = normalizeInput(input)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.Root == "" {
		return nil, fmt.Errorf("go syntax cache snapshot root is empty")
	}
	files := append([]graph.SnapshotFile(nil), input.Files...)
	sort.Slice(files, func(i, j int) bool {
		if files[i].Path != files[j].Path {
			return files[i].Path < files[j].Path
		}
		return files[i].BlobSHA < files[j].BlobSHA
	})
	seen := make(map[string]struct{}, len(files))
	entries := make([]graph.SyntaxCacheEntry, 0, len(files))
	cached := make(map[string]graph.SyntaxCacheEntry, len(input.CachedSyntax))
	for _, entry := range input.CachedSyntax {
		if entry.BlobSHA == "" || entry.ParserVersion != syntaxParserVersion {
			continue
		}
		format := entry.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		key := entry.BlobSHA + "\x00" + format
		if _, exists := cached[key]; exists {
			continue
		}
		entry.ObjectFormat = format
		cached[key] = entry
	}
	var firstErr error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return entries, err
		}
		if filepath.Ext(file.Path) != ".go" || file.BlobSHA == "" {
			continue
		}
		format := file.ObjectFormat
		if format == "" {
			format = "sha1"
		}
		key := file.BlobSHA + "\x00" + format
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if entry, ok := cached[key]; ok {
			entries = append(entries, entry)
			continue
		}
		relative := filepath.FromSlash(file.Path)
		if filepath.IsAbs(relative) || escapes(relative) {
			if firstErr == nil {
				firstErr = fmt.Errorf("unsafe syntax cache source path %q", file.Path)
			}
			continue
		}
		path := filepath.Join(input.Root, relative)
		if !within(input.Root, path) {
			if firstErr == nil {
				firstErr = fmt.Errorf("syntax cache source path %q escapes snapshot", file.Path)
			}
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read syntax cache source %q: %w", file.Path, err)
			}
			continue
		}
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, file.Path, contents, parser.ParseComments)
		if parseErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("parse syntax cache source %q: %w", file.Path, parseErr)
			}
			continue
		}
		summary := syntaxSummaryFromAST(fileSet, parsed)
		payload, marshalErr := json.Marshal(summary)
		if marshalErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("marshal syntax cache source %q: %w", file.Path, marshalErr)
			}
			continue
		}
		entries = append(entries, graph.SyntaxCacheEntry{BlobSHA: file.BlobSHA, ObjectFormat: format, ParserVersion: syntaxParserVersion, SyntaxJSON: payload})
	}
	return entries, firstErr
}

// SyntaxParserVersion identifies the serializable syntax schema.
func (a *Analyzer) SyntaxParserVersion() string { return syntaxParserVersion }

type syntaxSummary struct {
	Package      string              `json:"package"`
	Imports      []string            `json:"imports,omitempty"`
	Declarations []syntaxDeclaration `json:"declarations,omitempty"`
}

type syntaxDeclaration struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func syntaxSummaryFromAST(fileSet *token.FileSet, file *ast.File) syntaxSummary {
	result := syntaxSummary{}
	if file == nil {
		return result
	}
	if file.Name != nil {
		result.Package = file.Name.Name
	}
	for _, importSpec := range file.Imports {
		if importSpec == nil || importSpec.Path == nil {
			continue
		}
		value, err := strconv.Unquote(importSpec.Path.Value)
		if err == nil {
			result.Imports = append(result.Imports, value)
		}
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Name == nil {
				continue
			}
			kind := "function"
			if declaration.Recv != nil {
				kind = "method"
			}
			result.Declarations = append(result.Declarations, syntaxDeclaration{Kind: kind, Name: declaration.Name.Name, StartLine: fileSetLine(fileSet, declaration.Pos()), EndLine: fileSetLine(fileSet, declaration.End())})
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Name != nil {
						result.Declarations = append(result.Declarations, syntaxDeclaration{Kind: "type", Name: spec.Name.Name, StartLine: fileSetLine(fileSet, spec.Pos()), EndLine: fileSetLine(fileSet, spec.End())})
					}
				case *ast.ValueSpec:
					kind := strings.ToLower(declaration.Tok.String())
					for _, name := range spec.Names {
						if name != nil && name.Name != "_" {
							result.Declarations = append(result.Declarations, syntaxDeclaration{Kind: kind, Name: name.Name, StartLine: fileSetLine(fileSet, spec.Pos()), EndLine: fileSetLine(fileSet, spec.End())})
						}
					}
				}
			}
		}
	}
	sort.Strings(result.Imports)
	sort.Slice(result.Declarations, func(i, j int) bool {
		if result.Declarations[i].StartLine != result.Declarations[j].StartLine {
			return result.Declarations[i].StartLine < result.Declarations[j].StartLine
		}
		if result.Declarations[i].Name != result.Declarations[j].Name {
			return result.Declarations[i].Name < result.Declarations[j].Name
		}
		return result.Declarations[i].Kind < result.Declarations[j].Kind
	})
	return result
}

func fileSetLine(fileSet *token.FileSet, position token.Pos) int {
	if fileSet == nil || position == token.NoPos {
		return 0
	}
	return fileSet.PositionFor(position, false).Line
}
