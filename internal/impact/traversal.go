package impact

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

type nodeState struct {
	symbol        graph.Symbol
	result        graph.SymbolResult
	file          graph.File
	hasFile       bool
	depth         int
	score         int
	path          []PathStep
	contributions []ScoreContribution
	confidence    graph.Confidence
	first         graph.SymbolRelationship
	seed          bool
	historical    bool
	changeKind    string
}

type packageEvidence struct {
	key        string
	name       string
	score      int
	path       []PathStep
	confidence graph.Confidence
	external   bool
	reason     string
}

type traversalResult struct {
	nodes       map[string]*nodeState
	packages    map[string]*packageEvidence
	diagnostics []Diagnostic
	truncated   bool
}

func traverse(ctx context.Context, reader Reader, seeds []SeedCandidate, request Request) (traversalResult, error) {
	result := traversalResult{nodes: make(map[string]*nodeState, len(seeds)), packages: make(map[string]*packageEvidence)}
	queue := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return traversalResult{}, err
		}
		node, err := loadNode(ctx, reader, seed.Key, request.MaxFanout+1)
		if err != nil {
			result.diagnostics = append(result.diagnostics, Diagnostic{Severity: "warning", Code: "seed-load-failed", Message: fmt.Sprintf("seed %s could not be loaded: %v", seed.Key, err)})
			continue
		}
		node.score = seed.Score
		node.depth = 0
		node.seed = true
		node.contributions = []ScoreContribution{{Signal: "direct task seed", Value: seed.Score}}
		if node.confidence == "" {
			node.confidence = graph.ConfidenceExact
		}
		if _, exists := result.nodes[node.symbol.Key]; exists {
			continue
		}
		result.nodes[node.symbol.Key] = node
		queue = append(queue, node.symbol.Key)
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return traversalResult{}, err
		}
		key := queue[0]
		queue = queue[1:]
		current := result.nodes[key]
		if current == nil {
			continue
		}
		relationships := relationshipsOf(current.result)
		if len(relationships) == 0 {
			continue
		}
		if current.depth >= request.MaxDepth {
			result.truncated = true
			result.diagnostics = appendOnce(result.diagnostics, Diagnostic{Severity: "info", Code: "depth-limit", Message: fmt.Sprintf("impact traversal stopped at depth %d for %s", request.MaxDepth, current.symbol.Name)})
			continue
		}
		for index, relationship := range relationships {
			if index >= request.MaxFanout {
				result.truncated = true
				result.diagnostics = appendOnce(result.diagnostics, Diagnostic{Severity: "info", Code: "fanout-limit", Message: fmt.Sprintf("impact traversal capped at %d relationships from %s", request.MaxFanout, current.symbol.Name)})
				break
			}
			neighborKey, neighborKind := relationshipNeighbor(current.symbol.Key, relationship)
			if neighborKey == "" || neighborKey == current.symbol.Key {
				continue
			}
			step := pathStep(relationship)
			if neighborKind == graph.NodePackage || relationship.TargetKind == graph.NodePackage || relationship.SourceKind == graph.NodePackage {
				addPackageEvidence(result.packages, relationship, current, step)
				continue
			}
			if _, exists := result.nodes[neighborKey]; exists {
				continue
			}
			neighbor, loadErr := loadNode(ctx, reader, neighborKey, request.MaxFanout+1)
			if loadErr != nil {
				if strings.HasPrefix(neighborKey, "external:") || strings.HasPrefix(relationship.TargetPackage, "external:") {
					if relationship.TargetPackage == "" {
						relationship.TargetPackage = externalPackageKey(neighborKey)
					}
					addPackageEvidence(result.packages, relationship, current, step)
					result.diagnostics = appendOnce(result.diagnostics, Diagnostic{Severity: "info", Code: "external-target-unavailable", Message: fmt.Sprintf("external relationship target %s for %s has no local source; validate the package boundary", neighborKey, current.symbol.Name)})
					continue
				}
				result.diagnostics = appendOnce(result.diagnostics, Diagnostic{Severity: "warning", Code: "relationship-target-unavailable", Message: fmt.Sprintf("relationship target %s for %s is unavailable: %v", neighborKey, current.symbol.Name, loadErr)})
				continue
			}
			neighbor.depth = current.depth + 1
			neighbor.score = current.score + transitionScore(relationship, current, neighbor)
			neighbor.path = append(clonePath(current.path), step)
			neighbor.first = relationship
			neighbor.confidence = strongerConfidence(current.confidence, relationship.Confidence)
			neighbor.contributions = append(cloneContributions(current.contributions), relationshipContribution(relationship, current.depth+1))
			result.nodes[neighborKey] = neighbor
			queue = append(queue, neighborKey)
		}
	}
	return result, nil
}

func loadNode(ctx context.Context, reader Reader, key string, relationshipLimit int) (*nodeState, error) {
	var selected graph.SymbolResult
	var err error
	if bounded, ok := reader.(boundedReader); ok {
		selected, err = bounded.FindSymbolForImpact(ctx, key, relationshipLimit)
		if err != nil {
			return nil, err
		}
	} else {
		results, findErr := reader.FindSymbols(ctx, key)
		if findErr != nil {
			return nil, findErr
		}
		var found bool
		for index := range results {
			if results[index].Symbol.Key == key {
				selected = results[index]
				found = true
				break
			}
		}
		if !found && len(results) == 1 {
			selected = results[0]
			found = true
		}
		if !found {
			return nil, fmt.Errorf("symbol %q was not found", key)
		}
	}
	if selected.Symbol.Key == "" {
		return nil, fmt.Errorf("symbol %q was not found", key)
	}
	node := &nodeState{symbol: selected.Symbol, result: selected, confidence: graph.ConfidenceExact, historical: selected.Historical, changeKind: selected.ChangeKind}
	if node.symbol.FileKey != "" || node.symbol.Position.Path != "" {
		var file graph.File
		var fileErr error
		fileQuery := node.symbol.FileKey
		if fileQuery == "" {
			fileQuery = node.symbol.Position.Path
		}
		if node.historical {
			if metadataReader, ok := reader.(historicalFileMetadataReader); ok {
				file, fileErr = metadataReader.FindHistoricalFileMetadata(ctx, fileQuery)
			} else if metadataReader, ok := reader.(fileMetadataReader); ok {
				file, fileErr = metadataReader.FindFileMetadata(ctx, fileQuery)
			} else {
				fileResult, findErr := reader.FindFile(ctx, fileQuery)
				file, fileErr = fileResult.File, findErr
			}
		} else if metadataReader, ok := reader.(fileMetadataReader); ok {
			file, fileErr = metadataReader.FindFileMetadata(ctx, fileQuery)
		} else {
			fileResult, findErr := reader.FindFile(ctx, fileQuery)
			file, fileErr = fileResult.File, findErr
		}
		if fileErr == nil {
			node.file = file
			node.hasFile = true
			if node.symbol.Position.Path == "" {
				node.symbol.Position.Path = file.Path
			}
			if node.symbol.Position.StartLine == 0 {
				node.symbol.Position.StartLine = file.Position.StartLine
			}
			if node.symbol.Position.EndLine == 0 {
				node.symbol.Position.EndLine = file.Position.EndLine
			}
		} else {
			// A symbol remains useful for impact when its source metadata is
			// incomplete. Context will report the missing blob explicitly.
			node.file = graph.File{Key: node.symbol.FileKey, Path: node.symbol.Position.Path, PackageKey: node.symbol.PackageKey}
		}
	}
	return node, nil
}

func relationshipsOf(result graph.SymbolResult) []graph.SymbolRelationship {
	values := make([]graph.SymbolRelationship, 0, len(result.Calls)+len(result.CalledBy)+len(result.Implementations)+len(result.ImplementedBy)+len(result.Tests)+len(result.References))
	values = append(values, result.Calls...)
	values = append(values, result.CalledBy...)
	values = append(values, result.Implementations...)
	values = append(values, result.ImplementedBy...)
	values = append(values, result.Tests...)
	for _, reference := range result.References {
		values = append(values, graph.SymbolRelationship{
			SourceKey:     reference.SourceKey,
			TargetKey:     reference.TargetKey,
			SourcePackage: reference.SourcePackage,
			SourceFile:    reference.SourceFile,
			Kind:          reference.Kind,
			Confidence:    reference.Confidence,
			Position:      reference.Position,
			Historical:    reference.Historical,
		})
	}
	seen := make(map[string]struct{}, len(values))
	resultValues := make([]graph.SymbolRelationship, 0, len(values))
	for _, relationship := range values {
		key := relationship.SourceKey + "\x00" + relationship.TargetKey + "\x00" + string(relationship.Kind) + fmt.Sprintf("\x00%d\x00%d", relationship.Position.StartLine, relationship.Position.EndLine)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		resultValues = append(resultValues, relationship)
	}
	sort.SliceStable(resultValues, func(i, j int) bool {
		left, right := resultValues[i], resultValues[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.SourceKey != right.SourceKey {
			return left.SourceKey < right.SourceKey
		}
		if left.TargetKey != right.TargetKey {
			return left.TargetKey < right.TargetKey
		}
		if left.Position.StartLine != right.Position.StartLine {
			return left.Position.StartLine < right.Position.StartLine
		}
		return left.Position.EndLine < right.Position.EndLine
	})
	return resultValues
}

func relationshipNeighbor(current string, relationship graph.SymbolRelationship) (string, graph.NodeKind) {
	if relationship.SourceKey == current {
		return relationship.TargetKey, relationship.TargetKind
	}
	if relationship.TargetKey == current {
		return relationship.SourceKey, relationship.SourceKind
	}
	return "", ""
}

func pathStep(relationship graph.SymbolRelationship) PathStep {
	position := relationship.Position
	if position.Path == "" {
		position.Path = relationship.SourceFile
	}
	return PathStep{FromKey: relationship.SourceKey, ToKey: relationship.TargetKey, Kind: relationship.Kind, Confidence: relationship.Confidence, Position: position, Details: relationship.Details, Historical: relationship.Historical}
}

func addPackageEvidence(packages map[string]*packageEvidence, relationship graph.SymbolRelationship, current *nodeState, step PathStep) {
	key := relationship.TargetPackage
	if relationship.TargetKind == graph.NodePackage || strings.HasPrefix(relationship.TargetKey, "package:") {
		key = relationship.TargetPackage
		if key == "" {
			key = strings.TrimPrefix(relationship.TargetKey, "package:")
		}
	}
	if key == "" {
		key = relationship.SourcePackage
	}
	if key == "" {
		return
	}
	external := strings.HasPrefix(key, "external:") || (relationship.TargetKind == graph.NodePackage && relationship.TargetPackage != current.symbol.PackageKey)
	entry := packages[key]
	score := current.score + transitionScore(relationship, current, nil)
	if entry == nil || score > entry.score || (score == entry.score && pathLess([]PathStep{step}, entry.path)) {
		packages[key] = &packageEvidence{key: key, name: strings.TrimPrefix(strings.TrimPrefix(key, "go:package:"), "external:package:"), score: score, path: append([]PathStep(nil), current.path...), confidence: relationship.Confidence, external: external, reason: "package relationship requires validation"}
		packages[key].path = append(packages[key].path, step)
	}
}

func externalPackageKey(symbolKey string) string {
	value := strings.TrimPrefix(symbolKey, "external:symbol:")
	if index := strings.IndexByte(value, ':'); index > 0 {
		value = value[:index]
	}
	if value == "" {
		return "external:package:unknown"
	}
	return "external:package:" + value
}

func clonePath(path []PathStep) []PathStep {
	return append([]PathStep(nil), path...)
}

func cloneContributions(values []ScoreContribution) []ScoreContribution {
	return append([]ScoreContribution(nil), values...)
}

func strongerConfidence(left, right graph.Confidence) graph.Confidence {
	if confidenceRank(right) > confidenceRank(left) {
		return right
	}
	return left
}

func confidenceRank(value graph.Confidence) int {
	switch value {
	case graph.ConfidenceExact:
		return 3
	case graph.ConfidenceInferred:
		return 2
	case graph.ConfidencePossible:
		return 1
	default:
		return 0
	}
}

func relationshipContribution(relationship graph.SymbolRelationship, depth int) ScoreContribution {
	return ScoreContribution{Signal: fmt.Sprintf("%s at graph distance %d", relationship.Kind, depth), Value: edgeWeight(relationship.Kind) - depthPenalty(depth)}
}

func transitionScore(relationship graph.SymbolRelationship, current *nodeState, neighbor *nodeState) int {
	score := edgeWeight(relationship.Kind) - depthPenalty(current.depth+1)
	if relationship.Confidence == graph.ConfidenceExact {
		score += 10
	} else if relationship.Confidence == graph.ConfidenceInferred {
		score += 5
	}
	if neighbor != nil && neighbor.symbol.PackageKey != "" && neighbor.symbol.PackageKey == current.symbol.PackageKey {
		score += 10
	}
	if neighbor != nil && neighbor.symbol.Position.Path != "" && neighbor.symbol.Position.Path == current.symbol.Position.Path {
		score += 8
	}
	return score
}

func edgeWeight(kind graph.EdgeKind) int {
	switch kind {
	case graph.EdgeCalls:
		return 90
	case graph.EdgePossibleCall:
		return 65
	case graph.EdgeUnresolvedCall:
		return 25
	case graph.EdgeReferences:
		return 55
	case graph.EdgeImplements:
		return 75
	case graph.EdgeTests:
		return 60
	default:
		return 10
	}
}

func depthPenalty(depth int) int { return depth * 15 }

func pathLess(left, right []PathStep) bool {
	leftKey, rightKey := pathKey(left), pathKey(right)
	return leftKey < rightKey
}

func pathKey(path []PathStep) string {
	parts := make([]string, 0, len(path))
	for _, step := range path {
		parts = append(parts, step.FromKey+"\x00"+step.ToKey+"\x00"+string(step.Kind))
	}
	return strings.Join(parts, "\x00")
}

func appendOnce(values []Diagnostic, diagnostic Diagnostic) []Diagnostic {
	for _, value := range values {
		if value.Code == diagnostic.Code && value.Message == diagnostic.Message {
			return values
		}
	}
	return append(values, diagnostic)
}
