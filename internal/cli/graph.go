package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/repository"
)

type graphReader interface {
	Generation() graph.Generation
	FindSymbols(context.Context, string) ([]graph.SymbolResult, error)
	FindFile(context.Context, string) (graph.FileResult, error)
}

type graphSource struct {
	mode           string
	baseCommit     string
	baseGeneration int64
	overlayID      string
	manifest       string
}

func (a *App) graph(ctx context.Context, options Options, args []string) error {
	commandOptions, positional, err := parseGraphCommandOptions(options, args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return fmt.Errorf("%w: graph requires `symbol <name>` or `file <path>`", errUsage)
	}
	if positional[0] != "symbol" && positional[0] != "file" {
		return fmt.Errorf("%w: unknown graph target %q", errUsage, positional[0])
	}
	workingTree := commandOptions.WorkingTree || !commandOptions.Committed
	if workingTree {
		target, handle, base, effective, openErr := a.openEffectiveView(ctx, options)
		if openErr != nil {
			return openErr
		}
		source := graphSource{mode: "working-tree", baseCommit: string(effective.Generation().Commit), baseGeneration: int64(effective.Generation().ID), overlayID: effective.OverlayID(), manifest: effective.Manifest().ID}
		var queryErr error
		switch positional[0] {
		case "symbol":
			queryErr = renderSymbols(ctx, a.stdout, effective, target, positional[1], source, commandOptions.Format)
		case "file":
			queryErr = renderFile(ctx, a.stdout, effective, target, positional[1], source, commandOptions.Format)
		}
		return joinClose(queryErr, closeEffectiveResources(effective, base, handle))
	}
	target, handle, view, openErr := a.openTaskView(ctx, options)
	if openErr != nil {
		return openErr
	}
	source := graphSource{mode: "committed"}
	var queryErr error
	switch positional[0] {
	case "symbol":
		queryErr = renderSymbols(ctx, a.stdout, view, target, positional[1], source, commandOptions.Format)
	case "file":
		queryErr = renderFile(ctx, a.stdout, view, target, positional[1], source, commandOptions.Format)
	}
	return joinClose(queryErr, joinClose(view.Close(), handle.Close()))
}

type graphCommandOptions struct {
	WorkingTree bool
	Committed   bool
	Format      graphOutputFormat
}

func parseGraphCommandOptions(options Options, args []string) (graphCommandOptions, []string, error) {
	result := graphCommandOptions{Committed: options.Committed, Format: graphOutputText}
	formatSet := false
	jsonRequested := options.JSON
	remaining := make([]string, 0, len(args))
	setFormat := func(value string) error {
		format, err := parseGraphOutputFormat(value)
		if err != nil {
			return err
		}
		if formatSet && result.Format != format {
			return fmt.Errorf("%w: graph output format selected more than once", errUsage)
		}
		result.Format = format
		formatSet = true
		return nil
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--working-tree":
			result.WorkingTree = true
		case arg == "--committed":
			result.Committed = true
		case arg == "--json":
			jsonRequested = true
		case arg == "--format":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return graphCommandOptions{}, nil, fmt.Errorf("%w: --format requires a value", errUsage)
			}
			index++
			if err := setFormat(args[index]); err != nil {
				return graphCommandOptions{}, nil, err
			}
		case strings.HasPrefix(arg, "--format="):
			value := strings.TrimPrefix(arg, "--format=")
			if strings.TrimSpace(value) == "" {
				return graphCommandOptions{}, nil, fmt.Errorf("%w: --format requires a value", errUsage)
			}
			if err := setFormat(value); err != nil {
				return graphCommandOptions{}, nil, err
			}
		default:
			remaining = append(remaining, arg)
		}
	}
	if jsonRequested {
		if formatSet && result.Format != graphOutputJSON {
			return graphCommandOptions{}, nil, fmt.Errorf("%w: --json and --format %s cannot be combined", errUsage, result.Format)
		}
		result.Format = graphOutputJSON
	}
	if result.WorkingTree && result.Committed {
		return graphCommandOptions{}, nil, fmt.Errorf("%w: --working-tree and --committed cannot be combined", errUsage)
	}
	return result, remaining, nil
}

func renderSymbols(ctx context.Context, output io.Writer, view graphReader, target repository.Target, query string, source graphSource, format graphOutputFormat) error {
	// Keep the renderer's data contract in graphdb; the CLI only formats it.
	results, err := view.FindSymbols(ctx, query)
	if err != nil {
		return err
	}
	if format != graphOutputText {
		export := newSymbolGraphExport(target, view.Generation(), source, query, results)
		return renderGraphExport(ctx, output, format, export)
	}
	fmt.Fprintf(output, "Repository %s\n", target.Repository.ID)
	fmt.Fprintf(output, "Generation %d\n", view.Generation().ID)
	fmt.Fprintf(output, "Commit %s\n", view.Generation().Commit)
	renderGraphSource(output, source)
	if len(results) > 1 {
		fmt.Fprintf(output, "Matches %d (ambiguous name; use a canonical key)\n", len(results))
	}
	for _, result := range results {
		symbol := result.Symbol
		fmt.Fprintf(output, "Symbol %s\n", symbol.Name)
		fmt.Fprintf(output, "  key        %s\n", symbol.Key)
		fmt.Fprintf(output, "  kind       %s\n", symbol.Kind)
		fmt.Fprintf(output, "  package    %s\n", symbol.PackageKey)
		fmt.Fprintf(output, "  location   %s:%d-%d\n", symbol.Position.Path, symbol.Position.StartLine, symbol.Position.EndLine)
		if symbol.Signature != "" {
			fmt.Fprintf(output, "  signature  %s\n", symbol.Signature)
		}
		if result.Historical {
			fmt.Fprintf(output, "  historical true (%s)\n", result.ChangeKind)
		}
		for _, reference := range result.References {
			fmt.Fprintf(output, "  reference  %s %s %s", reference.SourceKey, reference.Kind, reference.Confidence)
			if reference.SourceFile != "" {
				fmt.Fprintf(output, " %s:%d", reference.SourceFile, reference.Position.StartLine)
			}
			fmt.Fprintln(output)
		}
		renderRelationshipSection(output, "calls", result.Calls, false)
		renderRelationshipSection(output, "called by", result.CalledBy, true)
		renderRelationshipSection(output, "implements", result.Implementations, false)
		renderRelationshipSection(output, "implemented by", result.ImplementedBy, true)
		renderRelationshipSection(output, "tests", result.Tests, true)
	}
	return nil
}

func renderRelationshipSection(output io.Writer, label string, relationships []graph.SymbolRelationship, incoming bool) {
	if len(relationships) == 0 {
		return
	}
	fmt.Fprintf(output, "  %s\n", label)
	for _, relationship := range relationships {
		name := relationship.TargetName
		// Position is the evidence span for the relationship, not the target
		// declaration. Keep the displayed line tied to the source evidence so a
		// cross-file call never prints a target file with the caller's line.
		location := relationship.Position.Path
		if location == "" {
			location = relationship.SourceFile
		}
		if incoming {
			name = relationship.SourceName
		}
		if name == "" {
			if incoming {
				name = relationship.SourceKey
			} else {
				name = relationship.TargetKey
			}
		}
		fmt.Fprintf(output, "    %s %s %s", name, relationship.Kind, relationship.Confidence)
		if location != "" && relationship.Position.StartLine > 0 {
			fmt.Fprintf(output, " %s:%d", location, relationship.Position.StartLine)
		}
		if relationship.Details != "" {
			fmt.Fprintf(output, " [%s]", relationship.Details)
		}
		if relationship.Historical {
			fmt.Fprint(output, " [historical]")
		}
		fmt.Fprintln(output)
	}
}

func renderFile(ctx context.Context, output io.Writer, view graphReader, target repository.Target, query string, source graphSource, format graphOutputFormat) error {
	result, err := view.FindFile(ctx, query)
	if err != nil {
		return err
	}
	if format != graphOutputText {
		export := newFileGraphExport(target, view.Generation(), source, query, result)
		return renderGraphExport(ctx, output, format, export)
	}
	fmt.Fprintf(output, "Repository %s\n", target.Repository.ID)
	fmt.Fprintf(output, "Generation %d\n", view.Generation().ID)
	fmt.Fprintf(output, "Commit %s\n", view.Generation().Commit)
	renderGraphSource(output, source)
	fmt.Fprintf(output, "File %s\n", result.File.Path)
	fmt.Fprintf(output, "  key        %s\n", result.File.Key)
	fmt.Fprintf(output, "  package    %s\n", result.File.PackageKey)
	fmt.Fprintf(output, "  blob       %s\n", result.File.BlobSHA)
	if len(result.Symbols) > 0 {
		fmt.Fprintln(output, "  symbols")
		for _, symbol := range result.Symbols {
			fmt.Fprintf(output, "    %s %s:%d\n", symbol.Name, symbol.Kind, symbol.Position.StartLine)
		}
	}
	if len(result.Imports) > 0 {
		fmt.Fprintln(output, "  imports")
		for _, importPath := range result.Imports {
			fmt.Fprintf(output, "    %s\n", strings.TrimSpace(importPath))
		}
	}
	for _, symbolResult := range result.SymbolResults {
		if len(symbolResult.Calls) == 0 && len(symbolResult.CalledBy) == 0 && len(symbolResult.Implementations) == 0 && len(symbolResult.ImplementedBy) == 0 && len(symbolResult.Tests) == 0 {
			continue
		}
		fmt.Fprintf(output, "  relationships %s\n", symbolResult.Symbol.Name)
		renderRelationshipSection(output, "calls", symbolResult.Calls, false)
		renderRelationshipSection(output, "called by", symbolResult.CalledBy, true)
		renderRelationshipSection(output, "implements", symbolResult.Implementations, false)
		renderRelationshipSection(output, "implemented by", symbolResult.ImplementedBy, true)
		renderRelationshipSection(output, "tests", symbolResult.Tests, true)
	}
	return nil
}

func renderGraphSource(output io.Writer, source graphSource) {
	if source.mode != "working-tree" {
		fmt.Fprintln(output, "Source      committed HEAD (working tree changes excluded)")
		return
	}
	fmt.Fprintln(output, "Source      working-tree overlay (temporary effective graph)")
	if source.baseCommit != "" {
		fmt.Fprintf(output, "Base commit %s\n", source.baseCommit)
	}
	if source.baseGeneration > 0 {
		fmt.Fprintf(output, "Base generation %d\n", source.baseGeneration)
	}
	if source.overlayID != "" {
		fmt.Fprintf(output, "Overlay     %s\n", source.overlayID)
	}
	if source.manifest != "" {
		fmt.Fprintf(output, "Manifest    %s\n", source.manifest)
	}
}
