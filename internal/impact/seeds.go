package impact

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/arham09/jejak/internal/graph"
)

type seedDiscovery struct {
	report      SeedReport
	selected    []SeedCandidate
	diagnostics []Diagnostic
}

func discoverSeeds(ctx context.Context, reader Reader, request Request) (seedDiscovery, error) {
	terms := tokenize(request.Task)
	discovery := seedDiscovery{report: SeedReport{Terms: append([]string(nil), terms...)}}
	searchLimit := request.MaxSeeds * 8
	if searchLimit < 32 {
		searchLimit = 32
	}
	if searchLimit > 512 {
		searchLimit = 512
	}
	searchTerms := significantTerms(terms)
	if len(searchTerms) == 0 {
		searchTerms = terms
	}
	var matches []graph.SymbolResult
	if !request.SeedOnly {
		var err error
		matches, err = reader.SearchSymbols(ctx, searchTerms, searchLimit)
		if err != nil {
			return seedDiscovery{}, err
		}
	}
	candidates := make([]SeedCandidate, 0, len(matches)+len(request.SeedKeys))
	seen := make(map[string]struct{}, len(matches)+len(request.SeedKeys))
	for _, match := range matches {
		if match.Symbol.Key == "" {
			continue
		}
		candidate := scoreCandidate(request.Task, searchTerms, match.Symbol)
		candidate.Historical = match.Historical
		candidate.ChangeKind = match.ChangeKind
		if candidate.Key == "" {
			continue
		}
		if _, exists := seen[candidate.Key]; exists {
			continue
		}
		seen[candidate.Key] = struct{}{}
		candidates = append(candidates, candidate)
	}
	overrides := make([]SeedCandidate, 0, len(request.SeedKeys))
	for _, key := range request.SeedKeys {
		var results []graph.SymbolResult
		var findErr error
		if bounded, ok := reader.(boundedReader); ok {
			var result graph.SymbolResult
			result, findErr = bounded.FindSymbolForImpact(ctx, key, request.MaxFanout+1)
			if findErr == nil {
				results = []graph.SymbolResult{result}
			}
		} else {
			results, findErr = reader.FindSymbols(ctx, key)
		}
		if findErr != nil {
			discovery.diagnostics = append(discovery.diagnostics, Diagnostic{Severity: "warning", Code: "seed-not-found", Message: "qualified seed " + key + " could not be loaded: " + findErr.Error()})
			continue
		}
		var selected *SeedCandidate
		for _, result := range results {
			if result.Symbol.Key != key {
				continue
			}
			candidate := scoreCandidate(request.Task, searchTerms, result.Symbol)
			candidate.Score += 1000
			candidate.Match = "qualified override"
			candidate.Reason = "explicit --seed override"
			candidate.Historical = result.Historical
			candidate.ChangeKind = result.ChangeKind
			copy := candidate
			selected = &copy
			break
		}
		if selected == nil {
			discovery.diagnostics = append(discovery.diagnostics, Diagnostic{Severity: "warning", Code: "seed-not-found", Message: "qualified seed " + key + " did not match an indexed symbol key"})
			continue
		}
		overrides = append(overrides, *selected)
		if _, exists := seen[selected.Key]; !exists {
			seen[selected.Key] = struct{}{}
			candidates = append(candidates, *selected)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return seedLess(candidates[i], candidates[j]) })
	if len(candidates) > searchLimit {
		candidates = candidates[:searchLimit]
		discovery.report.Truncated = true
	}
	discovery.report.Candidates = append([]SeedCandidate(nil), candidates...)
	if len(candidates) == 0 {
		discovery.report.Status = SeedNoMatch
		return discovery, nil
	}
	selected := make([]SeedCandidate, 0, request.MaxSeeds)
	selectedKeys := make(map[string]struct{}, request.MaxSeeds)
	for _, candidate := range overrides {
		if len(selected) >= request.MaxSeeds {
			discovery.report.Truncated = true
			break
		}
		if _, exists := selectedKeys[candidate.Key]; exists {
			continue
		}
		selectedKeys[candidate.Key] = struct{}{}
		selected = append(selected, candidate)
	}
	if len(selected) == 0 {
		topScore := candidates[0].Score
		for _, candidate := range candidates {
			if candidate.Score != topScore || len(selected) >= request.MaxSeeds {
				if candidate.Score == topScore && len(selected) >= request.MaxSeeds {
					discovery.report.Truncated = true
				}
				break
			}
			selectedKeys[candidate.Key] = struct{}{}
			selected = append(selected, candidate)
		}
	}
	discovery.selected = append([]SeedCandidate(nil), selected...)
	discovery.report.Selected = append([]SeedCandidate(nil), selected...)
	if len(selected) > 1 {
		discovery.report.Status = SeedAmbiguous
		discovery.diagnostics = append(discovery.diagnostics, Diagnostic{Severity: "warning", Code: "ambiguous-seeds", Message: "multiple equally ranked task seeds were selected; use --seed with a canonical symbol key to narrow the report"})
	} else {
		discovery.report.Status = SeedMatched
	}
	return discovery, nil
}

func scoreCandidate(task string, terms []string, symbol graph.Symbol) SeedCandidate {
	name := normalizeIdentifier(symbol.Name)
	key := normalizeIdentifier(symbol.Key)
	taskIdentifier := normalizeIdentifier(task)
	nameTerms := identifierTerms(symbol.Name)
	fieldText := strings.ToLower(strings.Join([]string{symbol.Key, symbol.Name, symbol.Receiver, symbol.Signature, symbol.PackageKey, symbol.FileKey, symbol.Position.Path}, " "))
	matchedTerms := 0
	nameMatches := 0
	for _, term := range terms {
		if strings.Contains(fieldText, term) {
			matchedTerms++
		}
		if containsTerm(nameTerms, term) {
			nameMatches++
		}
	}
	score := matchedTerms*10 + nameMatches*140
	match := "field token"
	reason := "task terms matched indexed symbol fields"
	if name == taskIdentifier || key == taskIdentifier {
		score += 1000
		match = "exact identifier"
		reason = "task identifier matches the symbol name"
	} else if len(terms) > 0 && allTermsIn(nameTerms, terms) {
		score += 400
		match = "name terms"
		reason = "all task identifier terms match the symbol name"
	} else if taskIdentifier != "" && strings.HasSuffix(taskIdentifier, name) && len(name) >= 4 {
		score += 700
		match = "task identifier"
		reason = "symbol name matches the identifier portion of the task"
	} else if strings.Contains(name, taskIdentifier) && taskIdentifier != "" {
		score += 350
		match = "name prefix"
		reason = "task identifier is contained in the symbol name"
	} else if strings.Contains(strings.ToLower(symbol.Signature), strings.ToLower(task)) {
		score += 140
		match = "signature"
		reason = "task text appears in the indexed signature"
	}
	if symbol.Position.Path != "" && anyTermIn(strings.ToLower(symbol.Position.Path), terms) {
		score += 15
	}
	if symbol.PackageKey != "" && anyTermIn(strings.ToLower(symbol.PackageKey), terms) {
		score += 12
	}
	return SeedCandidate{Key: symbol.Key, Name: symbol.Name, Kind: symbol.Kind, Package: symbol.PackageKey, Path: symbol.Position.Path, Signature: symbol.Signature, Score: score, Match: match, Reason: reason, Symbol: symbol}
}

func significantTerms(terms []string) []string {
	stop := map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "by": {}, "for": {},
		"from": {}, "handle": {}, "handling": {}, "in": {}, "into": {}, "of": {},
		"on": {}, "or": {}, "the": {}, "to": {}, "with": {}, "add": {}, "change": {},
		"update": {}, "support": {}, "implement": {},
	}
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		if _, exists := stop[term]; exists && len(terms) > 1 {
			continue
		}
		result = append(result, term)
	}
	return result
}

func containsTerm(terms []string, value string) bool {
	for _, term := range terms {
		if term == value {
			return true
		}
	}
	return false
}

func seedLess(left, right SeedCandidate) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.Key != right.Key {
		return left.Key < right.Key
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	return left.Name < right.Name
}

func tokenize(value string) []string {
	words := make([]string, 0)
	for _, part := range splitIdentifier(value) {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		words = append(words, part)
	}
	seen := make(map[string]struct{}, len(words))
	result := make([]string, 0, len(words))
	for _, word := range words {
		if _, exists := seen[word]; exists {
			continue
		}
		seen[word] = struct{}{}
		result = append(result, word)
	}
	return result
}

func identifierTerms(value string) []string {
	return tokenize(value)
}

func splitIdentifier(value string) []string {
	runes := []rune(value)
	if len(runes) == 0 {
		return nil
	}
	parts := make([]string, 0, len(runes)/2)
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			parts = append(parts, string(runes[start:end]))
		}
		start = -1
	}
	for index, current := range runes {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			flush(index)
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		previous := runes[index-1]
		var next rune
		if index+1 < len(runes) {
			next = runes[index+1]
		}
		if unicode.IsUpper(current) && unicode.IsLower(previous) {
			flush(index)
			start = index
		} else if unicode.IsUpper(current) && unicode.IsUpper(previous) && unicode.IsLower(next) {
			flush(index)
			start = index
		} else if unicode.IsDigit(current) != unicode.IsDigit(previous) {
			flush(index)
			start = index
		}
	}
	flush(len(runes))
	return parts
}

func normalizeIdentifier(value string) string {
	var builder strings.Builder
	for _, term := range tokenize(value) {
		builder.WriteString(term)
	}
	return builder.String()
}

func allTermsIn(haystack, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	joined := strings.Join(haystack, "")
	for _, term := range terms {
		if !strings.Contains(joined, term) {
			return false
		}
	}
	return true
}

func anyTermIn(value string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}
