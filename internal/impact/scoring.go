package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/graph"
)

func contextRadius(traversal traversalResult, limit int) Radius {
	nodes := sortedNodes(traversal.nodes, func(node *nodeState) bool { return true })
	items := make([]Item, 0, len(nodes))
	for _, node := range nodes {
		item := itemFromNode(node)
		if node.historical {
			item.Label = LabelHistorical
			item.Reason = "deleted baseline symbol retained to explain former callers and tests"
		} else if node.seed {
			item.Label = LabelMustRead
			item.Reason = "direct task seed; read before implementation"
		} else if node.symbol.Kind == graph.NodeTest {
			item.Label = LabelTest
			item.Reason = fmt.Sprintf("test associated through %s at graph distance %d", node.first.Kind, node.depth)
		} else {
			item.Label = LabelSupporting
			item.Reason = relationReason(node)
		}
		item.Evidence = []Evidence{nodeEvidence(node, item.Reason)}
		items = append(items, item)
	}
	items, truncated := boundItems(items, limit)
	return Radius{Items: items, Limit: limit, Truncated: truncated}
}

func implementationRadius(traversal traversalResult, limit int) Radius {
	nodes := sortedNodes(traversal.nodes, func(node *nodeState) bool { return node.symbol.Kind != graph.NodeTest })
	items := make([]Item, 0, len(nodes))
	for _, node := range nodes {
		item := itemFromNode(node)
		switch {
		case node.historical:
			item.Label = LabelHistorical
			item.Reason = "historical deleted symbol; inspect former dependents before removing behavior"
		case node.seed:
			item.Label = LabelHigh
			item.Reason = "direct task seed; likely implementation owner"
		case node.first.Kind == graph.EdgeUnresolvedCall:
			item.Label = LabelInspectOnly
			item.Reason = "unresolved call target; inspect manually before changing"
		case node.depth == 1:
			item.Label = LabelMedium
			item.Reason = relationReason(node)
		case node.depth > 1 && (node.first.Kind == graph.EdgeCalls || node.first.Kind == graph.EdgePossibleCall):
			item.Label = LabelDownstream
			item.Reason = "downstream dependency reached through bounded call traversal"
		default:
			item.Label = LabelLow
			item.Reason = relationReason(node)
		}
		item.Evidence = []Evidence{nodeEvidence(node, item.Reason)}
		items = append(items, item)
	}
	items, truncated := boundItems(items, limit)
	return Radius{Items: items, Limit: limit, Truncated: truncated}
}

func validationRadius(traversal traversalResult, limit int) Radius {
	items := make([]Item, 0)
	seen := make(map[string]struct{})
	for _, node := range sortedNodes(traversal.nodes, func(node *nodeState) bool { return node.symbol.Kind == graph.NodeTest }) {
		item := itemFromNode(node)
		item.Label = LabelDirectTest
		item.Reason = "test relationship reached from the impacted graph"
		item.Evidence = []Evidence{nodeEvidence(node, item.Reason)}
		if _, exists := seen[item.Key]; exists {
			continue
		}
		seen[item.Key] = struct{}{}
		items = append(items, item)
	}
	packageNames := make(map[string]struct{})
	for _, node := range traversal.nodes {
		if node.symbol.Kind == graph.NodeTest || node.symbol.PackageKey == "" {
			continue
		}
		packageNames[node.symbol.PackageKey] = struct{}{}
	}
	for key := range traversal.packages {
		packageNames[key] = struct{}{}
	}
	packages := make([]string, 0, len(packageNames))
	for key := range packageNames {
		packages = append(packages, key)
	}
	sort.Strings(packages)
	for _, key := range packages {
		if key == "" {
			continue
		}
		packageKey := "package:" + key
		if _, exists := seen[packageKey]; exists {
			continue
		}
		evidence := traversal.packages[key]
		item := Item{Key: packageKey, Kind: graph.NodePackage, Name: displayPackageName(key), Package: key, Score: packageScore(key, traversal), Label: LabelPackageRisk, Reason: "package needs validation even without an exact test-symbol association", Evidence: []Evidence{{Reason: "package-level validation fallback", Path: []PathStep{}, Contributions: []ScoreContribution{{Signal: "package validation fallback", Value: 20}}}}}
		if evidence != nil {
			item.Score = evidence.score
			item.External = evidence.external
			item.Label = LabelPackageTests
			item.Reason = evidence.reason
			item.Evidence[0] = Evidence{Reason: evidence.reason, Confidence: evidence.confidence, Path: clonePath(evidence.path), Contributions: []ScoreContribution{{Signal: "package relationship", Value: edgeWeight(graph.EdgeTests)}}}
			if evidence.external {
				item.Label = LabelExternal
				item.Reason = "external package relationship; validate integration at the boundary"
			}
		}
		seen[packageKey] = struct{}{}
		items = append(items, item)
	}
	items, truncated := boundItems(items, limit)
	return Radius{Items: items, Limit: limit, Truncated: truncated}
}

func sortedNodes(nodes map[string]*nodeState, include func(*nodeState) bool) []*nodeState {
	result := make([]*nodeState, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && include(node) {
			result = append(result, node)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if left.depth != right.depth {
			return left.depth < right.depth
		}
		if left.symbol.Position.Path != right.symbol.Position.Path {
			return left.symbol.Position.Path < right.symbol.Position.Path
		}
		return left.symbol.Key < right.symbol.Key
	})
	return result
}

func itemFromNode(node *nodeState) Item {
	path := node.symbol.Position.Path
	blob := ""
	if node.hasFile {
		if path == "" {
			path = node.file.Path
		}
		blob = node.file.BlobSHA
	}
	if path == "" {
		path = node.file.Path
	}
	return Item{Key: node.symbol.Key, Kind: node.symbol.Kind, Name: node.symbol.Name, Package: node.symbol.PackageKey, Path: path, Signature: node.symbol.Signature, Score: node.score, Symbol: node.symbol, Source: SourceRef{Path: path, BlobSHA: blob, Position: node.symbol.Position}, Historical: node.historical, ChangeKind: node.changeKind}
}

func nodeEvidence(node *nodeState, reason string) Evidence {
	return Evidence{Reason: reason, Confidence: node.confidence, Path: clonePath(node.path), Contributions: cloneContributions(node.contributions)}
}

func relationReason(node *nodeState) string {
	if node.seed {
		return "direct task seed"
	}
	if node.first.Kind == "" {
		return fmt.Sprintf("related symbol at graph distance %d", node.depth)
	}
	return fmt.Sprintf("reached through %s at graph distance %d", node.first.Kind, node.depth)
}

func boundItems(items []Item, limit int) ([]Item, bool) {
	if len(items) <= limit {
		return items, false
	}
	selected := make([]Item, 0, limit)
	selectedKeys := make(map[string]struct{}, limit)
	for _, item := range items {
		if !requiredItem(item) || len(selected) >= limit {
			continue
		}
		selected = append(selected, item)
		selectedKeys[item.Key] = struct{}{}
	}
	for _, item := range items {
		if len(selected) >= limit {
			break
		}
		if _, exists := selectedKeys[item.Key]; exists {
			continue
		}
		selected = append(selected, item)
		selectedKeys[item.Key] = struct{}{}
	}
	return selected, true
}

func requiredItem(item Item) bool {
	switch item.Label {
	case LabelMustRead, LabelHigh, LabelDirectTest, LabelPackageTests, LabelPackageRisk, LabelExternal, LabelHistorical:
		return true
	default:
		return false
	}
}

func packageScore(key string, traversal traversalResult) int {
	best := 0
	for _, node := range traversal.nodes {
		if node.symbol.PackageKey == key && node.score > best {
			best = node.score
		}
	}
	if best == 0 {
		best = 20
	}
	return best - 20
}

func displayPackageName(key string) string {
	key = strings.TrimPrefix(key, "go:package:")
	key = strings.TrimPrefix(key, "external:package:")
	return key
}
