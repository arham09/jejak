package graph

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateGeneration is the public validation entry point used by storage
// before a candidate can be activated.
func ValidateGeneration(g Generation) error { return g.Validate() }

// ValidateAnalysis checks the structural invariants required before a
// candidate generation can be activated. It deliberately does not attempt to
// validate language semantics; the analyzer owns those diagnostics.
func ValidateAnalysis(result AnalysisResult) error {
	if strings.TrimSpace(result.BuildFingerprint) == "" {
		return fmt.Errorf("analysis build fingerprint is empty")
	}
	if strings.TrimSpace(result.AnalyzerVersion) == "" {
		return fmt.Errorf("analysis analyzer version is empty")
	}
	packages := make(map[string]struct{}, len(result.Packages))
	for _, item := range result.Packages {
		if item.Key == "" {
			return fmt.Errorf("analysis package key is empty")
		}
		if _, exists := packages[item.Key]; exists {
			return fmt.Errorf("duplicate analysis package %q", item.Key)
		}
		if item.Directory != "" {
			directory := filepath.Clean(filepath.FromSlash(item.Directory))
			if filepath.IsAbs(directory) || directory == ".." || strings.HasPrefix(directory, ".."+string(filepath.Separator)) {
				return fmt.Errorf("analysis package %q directory escapes repository", item.Key)
			}
		}
		packages[item.Key] = struct{}{}
	}
	blobs := make(map[string]struct{}, len(result.Blobs))
	for _, item := range result.Blobs {
		if strings.TrimSpace(item.SHA) == "" {
			return fmt.Errorf("analysis blob SHA is empty")
		}
		if item.ByteSize < 0 {
			return fmt.Errorf("analysis blob %q has negative size", item.SHA)
		}
		if _, exists := blobs[item.SHA]; exists {
			return fmt.Errorf("duplicate analysis blob %q", item.SHA)
		}
		blobs[item.SHA] = struct{}{}
	}
	files := make(map[string]struct{}, len(result.Files))
	paths := make(map[string]struct{}, len(result.Files))
	for _, item := range result.Files {
		if item.Key == "" || item.Path == "" {
			return fmt.Errorf("analysis file key/path is empty")
		}
		if _, exists := files[item.Key]; exists {
			return fmt.Errorf("duplicate analysis file %q", item.Key)
		}
		files[item.Key] = struct{}{}
		cleanPath := filepath.Clean(filepath.FromSlash(item.Path))
		if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("analysis file %q escapes repository", item.Path)
		}
		path := filepath.ToSlash(cleanPath)
		if _, exists := paths[path]; exists {
			return fmt.Errorf("duplicate analysis file path %q", item.Path)
		}
		paths[path] = struct{}{}
		if item.PackageKey != "" {
			if _, exists := packages[item.PackageKey]; !exists {
				return fmt.Errorf("file %q refers to missing package %q", item.Key, item.PackageKey)
			}
		}
		if item.BlobSHA != "" {
			if _, exists := blobs[item.BlobSHA]; !exists {
				return fmt.Errorf("file %q refers to missing blob %q", item.Key, item.BlobSHA)
			}
		}
	}
	symbols := make(map[string]struct{}, len(result.Symbols))
	for _, item := range result.Symbols {
		if item.Key == "" || item.PackageKey == "" || item.FileKey == "" {
			return fmt.Errorf("analysis symbol %q has incomplete ownership", item.Key)
		}
		if item.Position.StartLine < 0 || item.Position.StartColumn < 0 || item.Position.EndLine < 0 || item.Position.EndColumn < 0 {
			return fmt.Errorf("analysis symbol %q has negative source position", item.Key)
		}
		if _, exists := symbols[item.Key]; exists {
			return fmt.Errorf("duplicate analysis symbol %q", item.Key)
		}
		symbols[item.Key] = struct{}{}
		if _, exists := packages[item.PackageKey]; !exists {
			return fmt.Errorf("symbol %q refers to missing package %q", item.Key, item.PackageKey)
		}
		if _, exists := files[item.FileKey]; !exists {
			return fmt.Errorf("symbol %q refers to missing file %q", item.Key, item.FileKey)
		}
	}
	nodes := make(map[string]struct{}, len(result.Nodes))
	packageNodes := make(map[string]struct{})
	for _, item := range result.Nodes {
		if item.Key == "" {
			return fmt.Errorf("analysis node key is empty")
		}
		if item.Kind == "" {
			return fmt.Errorf("analysis node %q has no kind", item.Key)
		}
		if _, exists := nodes[item.Key]; exists {
			return fmt.Errorf("duplicate analysis node %q", item.Key)
		}
		nodes[item.Key] = struct{}{}
		if item.Kind == NodePackage {
			packageNodes[item.Key] = struct{}{}
		}
		if item.Owned && item.PackageKey != "" {
			if _, exists := packages[item.PackageKey]; !exists {
				return fmt.Errorf("owned node %q refers to missing package %q", item.Key, item.PackageKey)
			}
		}
		if item.Owned && item.FileKey != "" {
			if _, exists := files[item.FileKey]; !exists {
				return fmt.Errorf("owned node %q refers to missing file %q", item.Key, item.FileKey)
			}
		}
		if item.Owned && item.SymbolKey != "" {
			if _, exists := symbols[item.SymbolKey]; !exists {
				return fmt.Errorf("node %q refers to missing symbol %q", item.Key, item.SymbolKey)
			}
		}
		if item.SourceBlob != "" {
			if _, exists := blobs[item.SourceBlob]; !exists {
				return fmt.Errorf("node %q refers to missing source blob %q", item.Key, item.SourceBlob)
			}
		}
		if item.SourceStart < 0 || item.SourceEnd < 0 {
			return fmt.Errorf("analysis node %q has negative source position", item.Key)
		}
	}
	edges := make(map[string]struct{}, len(result.Edges))
	for _, edge := range result.Edges {
		if edge.SourceKey == "" || edge.TargetKey == "" || edge.Kind == "" {
			return fmt.Errorf("analysis edge has incomplete endpoint")
		}
		if _, exists := nodes[edge.SourceKey]; !exists {
			return fmt.Errorf("edge source %q is missing", edge.SourceKey)
		}
		if _, exists := nodes[edge.TargetKey]; !exists {
			return fmt.Errorf("edge target %q is missing", edge.TargetKey)
		}
		if edge.Confidence != "" && !validConfidence(edge.Confidence) {
			return fmt.Errorf("edge %q -> %q has unknown confidence %q", edge.SourceKey, edge.TargetKey, edge.Confidence)
		}
		if edge.OwnerPackage != "" {
			if _, exists := packages[edge.OwnerPackage]; !exists {
				return fmt.Errorf("edge %q -> %q has missing owner package %q", edge.SourceKey, edge.TargetKey, edge.OwnerPackage)
			}
		}
		key := fmt.Sprintf("%s\x00%s\x00%s", edge.SourceKey, edge.TargetKey, edge.Kind)
		if _, exists := edges[key]; exists {
			return fmt.Errorf("duplicate analysis edge %q -> %q (%s)", edge.SourceKey, edge.TargetKey, edge.Kind)
		}
		edges[key] = struct{}{}
	}
	evidence := make(map[string]struct{}, len(result.Evidence))
	for _, item := range result.Evidence {
		if item.SourceKey == "" || item.TargetKey == "" || item.Kind == "" {
			return fmt.Errorf("analysis edge evidence has incomplete endpoint")
		}
		key := fmt.Sprintf("%s\x00%s\x00%s", item.SourceKey, item.TargetKey, item.Kind)
		if _, exists := edges[key]; !exists {
			return fmt.Errorf("edge evidence refers to missing edge %q -> %q (%s)", item.SourceKey, item.TargetKey, item.Kind)
		}
		evidenceKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s", item.SourceKey, item.TargetKey, item.Kind, item.SourceBlob, item.StartLine, item.StartColumn, item.EndLine, item.EndColumn, item.Details)
		if _, exists := evidence[evidenceKey]; exists {
			return fmt.Errorf("duplicate edge evidence %q -> %q (%s)", item.SourceKey, item.TargetKey, item.Kind)
		}
		evidence[evidenceKey] = struct{}{}
		if item.SourceBlob != "" {
			if _, exists := blobs[item.SourceBlob]; !exists {
				return fmt.Errorf("edge evidence refers to missing source blob %q", item.SourceBlob)
			}
		}
		if item.StartLine < 0 || item.StartColumn < 0 || item.EndLine < 0 || item.EndColumn < 0 {
			return fmt.Errorf("edge evidence has negative source position")
		}
	}
	dependencies := make(map[string]struct{}, len(result.PackageDependencies))
	for _, item := range result.PackageDependencies {
		if item.SourcePackage == "" || item.TargetPackage == "" {
			return fmt.Errorf("analysis package dependency has incomplete endpoint")
		}
		if _, exists := packages[item.SourcePackage]; !exists {
			return fmt.Errorf("package dependency source %q is missing", item.SourcePackage)
		}
		if _, exists := packages[item.TargetPackage]; !exists && !strings.HasPrefix(item.TargetPackage, "external:package:") {
			return fmt.Errorf("package dependency target %q is missing", item.TargetPackage)
		}
		key := item.SourcePackage + "\x00" + item.TargetPackage
		if _, exists := dependencies[key]; exists {
			return fmt.Errorf("duplicate package dependency %q -> %q", item.SourcePackage, item.TargetPackage)
		}
		dependencies[key] = struct{}{}
	}
	testRelationships := make(map[string]struct{}, len(result.TestRelationships))
	for _, item := range result.TestRelationships {
		if item.TestKey == "" || item.TargetKey == "" {
			return fmt.Errorf("analysis test relationship has incomplete endpoint")
		}
		if _, exists := symbols[item.TestKey]; !exists {
			return fmt.Errorf("test relationship test symbol %q is missing", item.TestKey)
		}
		if _, exists := symbols[item.TargetKey]; !exists {
			if _, packageExists := packageNodes[item.TargetKey]; !packageExists {
				return fmt.Errorf("test relationship target symbol or package %q is missing", item.TargetKey)
			}
		}
		if item.Confidence != "" && !validConfidence(item.Confidence) {
			return fmt.Errorf("test relationship %q -> %q has unknown confidence %q", item.TestKey, item.TargetKey, item.Confidence)
		}
		key := item.TestKey + "\x00" + item.TargetKey
		if _, exists := testRelationships[key]; exists {
			return fmt.Errorf("duplicate test relationship %q -> %q", item.TestKey, item.TargetKey)
		}
		testRelationships[key] = struct{}{}
	}
	for _, diagnostic := range result.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return err
		}
	}
	if result.HasErrors() {
		return fmt.Errorf("analysis reported %d error diagnostics", countErrorDiagnostics(result.Diagnostics))
	}
	return nil
}

func validConfidence(confidence Confidence) bool {
	switch confidence {
	case ConfidenceExact, ConfidenceInferred, ConfidencePossible:
		return true
	default:
		return false
	}
}

func countErrorDiagnostics(diagnostics []Diagnostic) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			count++
		}
	}
	return count
}
