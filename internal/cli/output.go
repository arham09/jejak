package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/arham09/jejak/internal/brief"
	"github.com/arham09/jejak/internal/impact"
	"github.com/arham09/jejak/internal/repository"
)

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	return nil
}

func renderImpactText(output io.Writer, target repository.Target, report impact.Report) error {
	fmt.Fprintln(output, "Jejak Impact Report v1")
	renderMetadata(output, target, report.Metadata)
	fmt.Fprintf(output, "Task        %s\n", report.Task)
	renderReportSource(output, report.Metadata)
	if report.Metadata.SourceMode == "working-tree" && len(report.Metadata.ChangedPaths) > 0 {
		fmt.Fprintln(output, "CHANGED CODE")
		for _, path := range report.Metadata.ChangedPaths {
			fmt.Fprintf(output, "  %s\n", path)
		}
	}
	renderSeeds(output, report.Seeds)
	renderRadius(output, "CONTEXT RADIUS", report.Context)
	renderRadius(output, "IMPLEMENTATION RADIUS", report.Implementation)
	renderRadius(output, "VALIDATION RADIUS", report.Validation)
	renderDiagnostics(output, report.Diagnostics)
	return nil
}

func renderContextText(output io.Writer, target repository.Target, report brief.Report) error {
	fmt.Fprintln(output, "Jejak Context Report v1")
	renderMetadata(output, target, report.Impact.Metadata)
	fmt.Fprintf(output, "Task        %s\n", report.Impact.Task)
	renderReportSource(output, report.Impact.Metadata)
	if report.Impact.Metadata.SourceMode == "working-tree" && len(report.Impact.Metadata.ChangedPaths) > 0 {
		fmt.Fprintln(output, "CHANGED CODE")
		for _, path := range report.Impact.Metadata.ChangedPaths {
			fmt.Fprintf(output, "  %s\n", path)
		}
	}
	renderSeeds(output, report.Impact.Seeds)
	renderRadius(output, "CONTEXT RADIUS", report.Impact.Context)
	fmt.Fprintln(output, "SOURCE EXCERPTS")
	if len(report.Excerpts) == 0 {
		fmt.Fprintln(output, "  (none)")
	}
	for _, excerpt := range report.Excerpts {
		marker := ""
		if excerpt.Truncated {
			marker = " (truncated)"
		}
		fmt.Fprintf(output, "  %s:%d-%d [blob %s]%s\n", excerpt.Path, excerpt.StartLine, excerpt.EndLine, excerpt.BlobSHA, marker)
		for _, line := range splitLines(excerpt.Text) {
			fmt.Fprintf(output, "    %s\n", line)
		}
	}
	renderRadius(output, "IMPLEMENTATION RADIUS", report.Impact.Implementation)
	renderRadius(output, "VALIDATION RADIUS", report.Impact.Validation)
	diagnostics := append([]impact.Diagnostic(nil), report.Impact.Diagnostics...)
	for _, diagnostic := range report.Diagnostics {
		diagnostics = append(diagnostics, impact.Diagnostic{Severity: diagnostic.Severity, Code: diagnostic.Code, Message: diagnostic.Message})
	}
	renderDiagnostics(output, diagnostics)
	return nil
}

func renderMetadata(output io.Writer, target repository.Target, metadata impact.Metadata) {
	fmt.Fprintf(output, "Repository  %s\n", target.Repository.ID)
	fmt.Fprintf(output, "Worktree    %s\n", target.Worktree.ID)
	fmt.Fprintf(output, "HEAD        %s\n", metadata.Commit)
	fmt.Fprintf(output, "Generation  %d\n", metadata.GenerationID)
	fmt.Fprintf(output, "Fingerprint %s\n", metadata.BuildFingerprint)
	fmt.Fprintf(output, "Analyzer    %s\n", metadata.AnalyzerVersion)
	if metadata.SourceMode == "working-tree" {
		if metadata.BaseCommit != "" {
			fmt.Fprintf(output, "Base commit  %s\n", metadata.BaseCommit)
		}
		if metadata.BaseGenerationID > 0 {
			fmt.Fprintf(output, "Base gen     %d\n", metadata.BaseGenerationID)
		}
		if metadata.OverlayID != "" {
			fmt.Fprintf(output, "Overlay      %s\n", metadata.OverlayID)
		}
		if metadata.SourceManifest != "" {
			fmt.Fprintf(output, "Manifest     %s\n", metadata.SourceManifest)
		}
		if metadata.EffectiveBuildFingerprint != "" {
			fmt.Fprintf(output, "Effective FP  %s\n", metadata.EffectiveBuildFingerprint)
		}
		if metadata.ChangedFiles > 0 {
			fmt.Fprintf(output, "Changed files %d\n", metadata.ChangedFiles)
		}
		if len(metadata.ChangedPaths) > 0 {
			fmt.Fprintf(output, "Changed paths %v\n", metadata.ChangedPaths)
		}
		if metadata.Incomplete {
			fmt.Fprintln(output, "Incomplete    true")
		}
	}
}

func renderReportSource(output io.Writer, metadata impact.Metadata) {
	if metadata.SourceMode == "working-tree" {
		fmt.Fprintln(output, "Source      working-tree overlay (temporary effective graph)")
		return
	}
	// Keep the committed wording stable for agents and existing consumers.
	fmt.Fprintln(output, "Source      committed HEAD (working tree changes excluded)")
}

func renderSeeds(output io.Writer, seeds impact.SeedReport) {
	fmt.Fprintln(output, "SEEDS")
	fmt.Fprintf(output, "  status      %s\n", seeds.Status)
	if len(seeds.Terms) > 0 {
		fmt.Fprintf(output, "  terms       %v\n", seeds.Terms)
	}
	if len(seeds.Candidates) > 0 {
		fmt.Fprintln(output, "  candidates")
		for _, candidate := range seeds.Candidates {
			marker := ""
			if candidate.Historical {
				marker += " historical"
			}
			if candidate.ChangeKind != "" {
				marker += " change=" + candidate.ChangeKind
			}
			fmt.Fprintf(output, "    %s %s score=%d %s%s\n", candidate.Name, candidate.Path, candidate.Score, candidate.Match, marker)
			fmt.Fprintf(output, "      key    %s\n", candidate.Key)
			fmt.Fprintf(output, "      reason %s\n", candidate.Reason)
		}
	}
	if len(seeds.Selected) > 0 {
		fmt.Fprintln(output, "  selected")
		for _, candidate := range seeds.Selected {
			marker := ""
			if candidate.Historical {
				marker = " historical"
			}
			fmt.Fprintf(output, "    %s (%s)%s\n", candidate.Name, candidate.Key, marker)
		}
	}
	if seeds.Truncated {
		fmt.Fprintln(output, "  truncated   true")
	}
}

func renderRadius(output io.Writer, heading string, radius impact.Radius) {
	fmt.Fprintln(output, heading)
	if len(radius.Items) == 0 {
		fmt.Fprintln(output, "  (none)")
	}
	for _, item := range radius.Items {
		name := item.Name
		if name == "" {
			name = item.Key
		}
		fmt.Fprintf(output, "  %s [%s] %s score=%d\n", name, item.Kind, item.Label, item.Score)
		if item.Historical {
			fmt.Fprintf(output, "    change     %s (historical baseline)\n", item.ChangeKind)
		}
		if item.Path != "" {
			fmt.Fprintf(output, "    location   %s", item.Path)
			if item.Source.Position.StartLine > 0 {
				fmt.Fprintf(output, ":%d-%d", item.Source.Position.StartLine, item.Source.Position.EndLine)
			}
			fmt.Fprintln(output)
		}
		fmt.Fprintf(output, "    reason     %s\n", item.Reason)
		for _, evidence := range item.Evidence {
			confidence := ""
			if evidence.Confidence != "" {
				confidence = " confidence=" + string(evidence.Confidence)
			}
			fmt.Fprintf(output, "    evidence   %s%s\n", evidence.Reason, confidence)
			for _, step := range evidence.Path {
				fmt.Fprintf(output, "      path     %s -[%s]-> %s", step.FromKey, step.Kind, step.ToKey)
				if step.Position.Path != "" && step.Position.StartLine > 0 {
					fmt.Fprintf(output, " %s:%d", step.Position.Path, step.Position.StartLine)
				}
				fmt.Fprintln(output)
			}
		}
	}
	if radius.Truncated {
		fmt.Fprintf(output, "  truncated   true (limit %d)\n", radius.Limit)
	}
}

func renderDiagnostics(output io.Writer, diagnostics []impact.Diagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	fmt.Fprintln(output, "DIAGNOSTICS")
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(output, "  [%s] %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
	}
}

func splitLines(value string) []string {
	if value == "" {
		return []string{""}
	}
	return strings.Split(value, "\n")
}
