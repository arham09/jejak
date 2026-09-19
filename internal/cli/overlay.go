package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arham09/jejak/internal/config"
	"github.com/arham09/jejak/internal/graph"
	"github.com/arham09/jejak/internal/graphdb"
	"github.com/arham09/jejak/internal/impact"
	"github.com/arham09/jejak/internal/overlay"
	"github.com/arham09/jejak/internal/repository"
)

// openEffectiveView keeps the committed view alive while the temporary
// overlay delegates unaffected facts to it. Callers close overlay first, then
// the committed view, then the writer/store handle.
func (a *App) openEffectiveView(ctx context.Context, options Options) (repository.Target, *targetStore, *graphdb.View, *overlay.View, error) {
	target, handle, base, err := a.openTaskView(ctx, options)
	if err != nil {
		return repository.Target{}, nil, nil, nil, err
	}
	dataRoot, err := config.ResolveDataRoot(options.DataDir, target.Repository.Root)
	if err != nil {
		return repository.Target{}, handle, base, nil, joinClose(err, closeViewAndHandle(base, handle))
	}
	manager, err := a.overlayManager(dataRoot, target)
	if err != nil {
		return repository.Target{}, handle, base, nil, joinClose(err, closeViewAndHandle(base, handle))
	}
	effective, err := manager.Build(ctx, target, base, buildConfig(options))
	if err != nil {
		return repository.Target{}, handle, base, nil, joinClose(err, closeViewAndHandle(base, handle))
	}
	return target, handle, base, effective, nil
}

func closeViewAndHandle(base *graphdb.View, handle *targetStore) error {
	var result error
	if base != nil {
		result = errors.Join(result, base.Close())
	}
	if handle != nil {
		result = errors.Join(result, handle.Close())
	}
	return result
}

func closeEffectiveResources(effective *overlay.View, base *graphdb.View, handle *targetStore) error {
	var result error
	if effective != nil {
		result = errors.Join(result, effective.Close())
	}
	if base != nil {
		result = errors.Join(result, base.Close())
	}
	if handle != nil {
		result = errors.Join(result, handle.Close())
	}
	return result
}

func applyOverlayMetadata(report *impact.Report, view *overlay.View) {
	if report == nil || view == nil {
		return
	}
	metadata := report.Metadata
	metadata.SourceMode = "working-tree"
	metadata.BaseCommit = metadata.Commit
	metadata.BaseGenerationID = metadata.GenerationID
	metadata.OverlayID = view.OverlayID()
	manifest := view.Manifest()
	metadata.SourceManifest = manifest.ID
	metadata.EffectiveBuildFingerprint = view.EffectiveBuildFingerprint()
	metadata.Incomplete = view.Incomplete()
	metadata.ChangedFiles = len(manifest.Changes)
	metadata.ChangedPaths = overlayChangedPaths(manifest)
	report.Metadata = metadata
	for _, diagnostic := range view.Diagnostics() {
		report.Diagnostics = append(report.Diagnostics, impact.Diagnostic{Severity: string(diagnostic.Severity), Code: "overlay-analysis", Message: formatOverlayDiagnostic(diagnostic)})
	}
	if view.Incomplete() {
		report.Diagnostics = append(report.Diagnostics, impact.Diagnostic{Severity: "warning", Code: "overlay-incomplete", Message: "working-tree semantic analysis is incomplete; validate affected packages and inspect diagnostics before relying on the report"})
	}
}

func overlayChangedPaths(manifest overlay.Manifest) []string {
	seen := make(map[string]struct{}, len(manifest.Changes)*2)
	for _, change := range manifest.Changes {
		if change.OldPath != "" {
			seen[change.OldPath] = struct{}{}
		}
		if change.NewPath != "" {
			seen[change.NewPath] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func formatOverlayDiagnostic(diagnostic graph.Diagnostic) string {
	message := strings.TrimSpace(diagnostic.Message)
	if diagnostic.File != "" {
		return fmt.Sprintf("%s (%s)", message, diagnostic.File)
	}
	if diagnostic.Package != "" {
		return fmt.Sprintf("%s (%s)", message, diagnostic.Package)
	}
	return message
}

func actualChangeRequest(request impact.Request, view *overlay.View) impact.Request {
	request.Task = strings.TrimSpace(request.Task)
	if request.Task == "" {
		request.Task = "working tree changes"
	}
	request.SeedOnly = true
	keys := make([]string, 0)
	for _, change := range view.Changes() {
		if change.After.Key != "" {
			keys = append(keys, change.After.Key)
		}
		if change.Kind == overlay.SymbolDeleted || change.Kind == overlay.SymbolRenamed {
			if change.Before.Key != "" {
				keys = append(keys, change.Before.Key)
			}
		}
	}
	sort.Strings(keys)
	request.SeedKeys = uniqueStrings(keys)
	return request
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
