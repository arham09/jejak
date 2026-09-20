package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/goccy/go-graphviz"
)

// writeGraphSVG lays the export out with Graphviz and writes the rendered SVG.
//
// The renderer is the Graphviz library compiled to WebAssembly and embedded in
// the binary, so `jejak graph --format svg` needs no installed `dot` command
// and no network access. DOT remains the intermediate representation: the same
// writer serves `--format dot`, so both formats carry identical provenance
// attributes and cannot drift apart.
func writeGraphSVG(ctx context.Context, output io.Writer, export graphExport) error {
	// The embedded renderer runs the layout to completion and does not observe
	// cancellation itself, so the deadline is checked before the work starts.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("render graph SVG: %w", err)
	}
	var source bytes.Buffer
	if err := writeGraphDOT(&source, export); err != nil {
		return err
	}
	parsed, err := graphviz.ParseBytes(source.Bytes())
	if err != nil {
		return fmt.Errorf("parse graph DOT for SVG rendering: %w", err)
	}
	defer func() { _ = parsed.Close() }()

	engine, err := graphviz.New(ctx)
	if err != nil {
		return fmt.Errorf("start graph SVG renderer: %w", err)
	}
	// Render into memory first so a failed layout leaves no partial SVG on a
	// stream a caller may already be piping somewhere.
	var rendered bytes.Buffer
	renderErr := engine.Render(ctx, parsed, graphviz.SVG, &rendered)
	if closeErr := engine.Close(); closeErr != nil && renderErr == nil {
		renderErr = closeErr
	}
	if renderErr != nil {
		return fmt.Errorf("render graph SVG: %w", renderErr)
	}
	if _, err := output.Write(rendered.Bytes()); err != nil {
		return fmt.Errorf("write graph SVG: %w", err)
	}
	return nil
}
