package cli

import (
	"context"

	"github.com/arham09/jejak/internal/brief"
	"github.com/arham09/jejak/internal/impact"
)

func (a *App) context(ctx context.Context, options Options, args []string) error {
	commandOptions, err := parseTaskCommandOptions(options, args)
	if err != nil {
		return err
	}
	workingTree := commandOptions.WorkingTree || !commandOptions.Committed
	if workingTree {
		target, handle, base, effective, openErr := a.openEffectiveView(ctx, options)
		if openErr != nil {
			return openErr
		}
		request := commandOptions.Request
		if commandOptions.WorkingTree && request.Task == "" {
			request = actualChangeRequest(request, effective)
		}
		impactReport, analysisErr := impact.Analyze(ctx, effective, request)
		if analysisErr == nil {
			applyOverlayMetadata(&impactReport, effective)
		}
		var contextReport brief.Report
		if analysisErr == nil {
			builder, builderErr := brief.NewBuilder(effective, effective.Root(), brief.Options{MaxLines: commandOptions.ExcerptLines, MaxBytes: commandOptions.ExcerptBytes})
			if builderErr != nil {
				analysisErr = builderErr
			} else {
				contextReport, analysisErr = builder.Build(ctx, impactReport)
			}
		}
		closeErr := closeEffectiveResources(effective, base, handle)
		if analysisErr != nil {
			return joinClose(analysisErr, closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
		if commandOptions.JSON {
			return writeJSON(a.stdout, contextReport)
		}
		return renderContextText(a.stdout, target, contextReport)
	}
	target, handle, view, err := a.openTaskView(ctx, options)
	if err != nil {
		return err
	}
	report, err := impact.Analyze(ctx, view, commandOptions.Request)
	if err != nil {
		return joinClose(err, joinClose(view.Close(), handle.Close()))
	}
	builder, err := brief.NewBuilder(gitSourceReader{client: a.git}, target.Worktree.Path, brief.Options{MaxLines: commandOptions.ExcerptLines, MaxBytes: commandOptions.ExcerptBytes})
	if err != nil {
		return joinClose(err, joinClose(view.Close(), handle.Close()))
	}
	contextReport, err := builder.Build(ctx, report)
	closeErr := joinClose(view.Close(), handle.Close())
	if err != nil {
		return joinClose(err, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if commandOptions.JSON {
		return writeJSON(a.stdout, contextReport)
	}
	return renderContextText(a.stdout, target, contextReport)
}
