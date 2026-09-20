package golang

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"

	"github.com/arham09/jejak/internal/graph"
)

type loadPlan struct {
	Root        string
	ModuleRoots []moduleRoot
	Workspace   bool
}

type moduleRoot struct {
	Directory string
	Path      string
}

func discoverPlan(root string) (loadPlan, []graph.Diagnostic) {
	plan := loadPlan{Root: root}
	workPath := filepath.Join(root, "go.work")
	contents, err := os.ReadFile(workPath)
	if err == nil {
		work, parseErr := modfile.ParseWork(workPath, contents, nil)
		if parseErr != nil {
			return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.work", Message: fmt.Sprintf("parse go.work: %v", parseErr)}}
		}
		plan.Workspace = true
		for _, replacement := range work.Replace {
			if diagnostic := validateLocalReplacement(root, workPath, replacement); diagnostic != nil {
				return plan, []graph.Diagnostic{*diagnostic}
			}
		}
		for _, use := range work.Use {
			relative := filepath.FromSlash(use.Path)
			if filepath.IsAbs(relative) || escapes(relative) {
				return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.work", Message: fmt.Sprintf("workspace module %q is outside the committed repository", use.Path)}}
			}
			directory := filepath.Clean(filepath.Join(root, relative))
			if !within(root, directory) {
				return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.work", Message: fmt.Sprintf("workspace module %q escapes the committed repository", use.Path)}}
			}
			moduleContents, moduleErr := os.ReadFile(filepath.Join(directory, "go.mod"))
			if moduleErr != nil {
				return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: filepath.ToSlash(filepath.Join(use.Path, "go.mod")), Message: fmt.Sprintf("workspace member has no readable go.mod: %v", moduleErr)}}
			}
			parsed, moduleParseErr := modfile.Parse(filepath.Join(directory, "go.mod"), moduleContents, nil)
			if moduleParseErr != nil || parsed.Module == nil || parsed.Module.Mod.Path == "" {
				message := "workspace member go.mod has no module path"
				if moduleParseErr != nil {
					message = fmt.Sprintf("parse workspace member go.mod: %v", moduleParseErr)
				}
				return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: filepath.ToSlash(filepath.Join(use.Path, "go.mod")), Message: message}}
			}
			for _, replacement := range parsed.Replace {
				if diagnostic := validateLocalReplacement(directory, filepath.Join(directory, "go.mod"), replacement); diagnostic != nil {
					return plan, []graph.Diagnostic{*diagnostic}
				}
			}
			plan.ModuleRoots = append(plan.ModuleRoots, moduleRoot{Directory: directory, Path: parsed.Module.Mod.Path})
		}
		if len(plan.ModuleRoots) == 0 {
			return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.work", Message: "go.work declares no use modules"}}
		}
		sort.Slice(plan.ModuleRoots, func(i, j int) bool { return plan.ModuleRoots[i].Directory < plan.ModuleRoots[j].Directory })
		return plan, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.work", Message: fmt.Sprintf("read go.work: %v", err)}}
	}
	contents, err = os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.mod", Message: fmt.Sprintf("read go.mod: %v", err)}}
	}
	parsed, parseErr := modfile.Parse(filepath.Join(root, "go.mod"), contents, nil)
	if parseErr != nil {
		return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.mod", Message: fmt.Sprintf("parse go.mod: %v", parseErr)}}
	}
	if parsed.Module == nil || parsed.Module.Mod.Path == "" {
		return plan, []graph.Diagnostic{{Severity: graph.SeverityError, File: "go.mod", Message: "go.mod has no module path"}}
	}
	for _, replacement := range parsed.Replace {
		if diagnostic := validateLocalReplacement(root, filepath.Join(root, "go.mod"), replacement); diagnostic != nil {
			return plan, []graph.Diagnostic{*diagnostic}
		}
	}
	plan.ModuleRoots = []moduleRoot{{Directory: root, Path: parsed.Module.Mod.Path}}
	return plan, nil
}

func (a *Analyzer) load(ctx context.Context, input graph.AnalyzeInput, plan loadPlan) ([]*packages.Package, []graph.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	env := controlledEnvironment(input.Build, plan)
	config := &packages.Config{
		Context: ctx,
		Dir:     input.Root,
		Env:     env,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedTypesSizes | packages.NeedSyntax |
			packages.NeedModule | packages.NeedForTest | packages.NeedEmbedFiles,
		// Test variants roughly double the loaded packages and the graph they
		// produce, so they are loaded only on request. Without them no
		// _test.go file is parsed and no test symbol or relationship exists.
		Tests: input.Build.IncludeTests,
	}
	patterns := make([]string, 0, len(plan.ModuleRoots))
	for _, module := range plan.ModuleRoots {
		relative, err := filepath.Rel(input.Root, module.Directory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("workspace module %q is outside snapshot", module.Directory)
		}
		if relative == "." {
			patterns = append(patterns, "./...")
		} else {
			patterns = append(patterns, "./"+filepath.ToSlash(relative)+"/...")
		}
	}
	packagesLoaded, err := packages.Load(config, patterns...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return packagesLoaded, []graph.Diagnostic{{Severity: graph.SeverityError, Message: fmt.Sprintf("load Go packages: %v", err)}}, nil
	}
	diagnostics := make([]graph.Diagnostic, 0)
	for _, pkg := range packagesLoaded {
		for _, packageErr := range pkg.Errors {
			severity := graph.SeverityError
			if packageErr.Kind == packages.ListError {
				severity = graph.SeverityError
			}
			diagnostics = append(diagnostics, graph.Diagnostic{Severity: severity, Package: pkg.PkgPath, File: packageErrorFile(input.Root, packageErr), Message: packageErrorMessage(packageErr.Msg)})
		}
		if pkg.IllTyped && len(pkg.Errors) == 0 {
			diagnostics = append(diagnostics, graph.Diagnostic{Severity: graph.SeverityError, Package: pkg.PkgPath, Message: "package is ill-typed"})
		}
	}
	return packagesLoaded, diagnostics, nil
}

func packageErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	lower := strings.ToLower(message)
	if strings.Contains(lower, "could not import") ||
		strings.Contains(lower, "cannot find module") ||
		strings.Contains(lower, "no required module provides package") ||
		strings.Contains(lower, "module lookup disabled") {
		return "missing module dependency: " + message
	}
	return message
}

func packageErrorFile(root string, err packages.Error) string {
	if err.Pos == "" {
		return ""
	}
	parts := strings.SplitN(err.Pos, ":", 2)
	if len(parts) == 0 {
		return ""
	}
	path := parts[0]
	if abs, absErr := filepath.Abs(path); absErr == nil {
		if relative, relErr := filepath.Rel(root, abs); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(relative)
		}
	}
	return filepath.ToSlash(path)
}

func controlledEnvironment(build graph.BuildConfig, plan loadPlan) []string {
	build = build.Normalize()
	env := append([]string(nil), os.Environ()...)
	removeEnv := func(key string) {
		prefix := key + "="
		filtered := env[:0]
		for _, value := range env {
			if !strings.HasPrefix(value, prefix) {
				filtered = append(filtered, value)
			}
		}
		env = filtered
	}
	setEnv := func(key, value string) {
		removeEnv(key)
		env = append(env, key+"="+value)
	}
	removeEnv("GOWORK")
	setEnv("GOTOOLCHAIN", "local")
	setEnv("GO111MODULE", "on")
	if plan.Root != "" {
		setEnv("PWD", plan.Root)
	}
	if plan.Workspace {
		setEnv("GOWORK", filepath.Join(plan.Root, "go.work"))
	} else {
		setEnv("GOWORK", "off")
	}
	flags := sanitizeGoFlags(os.Getenv("GOFLAGS"))
	if !build.DownloadDependencies {
		setEnv("GOPROXY", "off")
		setEnv("GOSUMDB", "off")
		setEnv("GONOSUMDB", "*")
		flags = appendModuleMode(flags, "readonly")
	} else {
		// The explicit opt-in analyzes the immutable snapshot, so Go may
		// materialize missing sums/modules there without touching the checkout.
		flags = appendModuleMode(flags, "mod")
	}
	if build.GOOS != "" {
		setEnv("GOOS", build.GOOS)
	}
	if build.GOARCH != "" {
		setEnv("GOARCH", build.GOARCH)
	}
	if build.CGOEnabled != "" {
		setEnv("CGO_ENABLED", build.CGOEnabled)
	}
	if len(build.Tags) > 0 {
		flags = appendTags(flags, build.Tags)
	}
	setEnv("GOFLAGS", flags)
	return env
}

func appendTags(existing string, tags []string) string {
	parts := strings.Fields(existing)
	if len(tags) > 0 {
		parts = append(parts, "-tags="+strings.Join(tags, ","))
	}
	return strings.Join(parts, " ")
}

func appendModuleMode(existing, mode string) string {
	parts := strings.Fields(sanitizeGoFlags(existing))
	filtered := parts[:0]
	for _, part := range parts {
		if strings.HasPrefix(part, "-mod=") {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(append(filtered, "-mod="+mode), " ")
}

func sanitizeGoFlags(existing string) string {
	parts := strings.Fields(existing)
	filtered := parts[:0]
	for _, part := range parts {
		switch {
		case strings.HasPrefix(part, "-modfile="),
			strings.HasPrefix(part, "-overlay="),
			strings.HasPrefix(part, "-toolexec="),
			strings.HasPrefix(part, "-exec="),
			strings.HasPrefix(part, "-pkgdir="):
			continue
		default:
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, " ")
}

func (a *Analyzer) fingerprint(ctx context.Context, input graph.AnalyzeInput, plan loadPlan) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	build := input.Build.Normalize()
	parts := [][]byte{
		[]byte(analyzerVersion),
		[]byte(runtime.Version()),
		[]byte(effectiveValue(build.GOOS, os.Getenv("GOOS"), runtime.GOOS)),
		[]byte(effectiveValue(build.GOARCH, os.Getenv("GOARCH"), runtime.GOARCH)),
		[]byte(effectiveValue(build.CGOEnabled, os.Getenv("CGO_ENABLED"), "")),
		[]byte(strings.Join(build.Tags, ",")),
		[]byte(fmt.Sprintf("download=%t;workspace=%t;tests=%t", build.DownloadDependencies, plan.Workspace, build.IncludeTests)),
		[]byte(sanitizeGoFlags(os.Getenv("GOFLAGS"))),
		[]byte(os.Getenv("GOPROXY")),
	}
	files := append([]graph.SnapshotFile(nil), input.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !fingerprintPath(file.Path) {
			// Tracked non-Go files can be selected by //go:embed or other
			// build tooling. Include their immutable object identity without
			// reading potentially large binary assets into the fingerprint.
			parts = append(parts, []byte("tracked"), []byte(file.Path), []byte(file.BlobSHA), []byte(file.Mode), []byte(fmt.Sprintf("%d", file.Size)))
			continue
		}
		contents, err := os.ReadFile(filepath.Join(input.Root, filepath.FromSlash(file.Path)))
		if err != nil {
			return "", fmt.Errorf("read build input %q: %w", file.Path, err)
		}
		parts = append(parts, []byte(file.Path), []byte(file.BlobSHA), contents)
	}
	if len(files) == 0 {
		walkErr := filepath.WalkDir(input.Root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil || entry == nil || entry.IsDir() {
				return walkErr
			}
			relative, relErr := filepath.Rel(input.Root, path)
			if relErr != nil || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) || relative == ".git" {
				return nil
			}
			if !fingerprintPath(relative) {
				return nil
			}
			contents, readErr := os.ReadFile(path)
			if readErr == nil {
				parts = append(parts, []byte(filepath.ToSlash(relative)), contents)
			}
			return nil
		})
		if walkErr != nil {
			return "", fmt.Errorf("walk analysis snapshot for fingerprint: %w", walkErr)
		}
	}
	return graph.FingerprintBytes(parts...), nil
}

func effectiveValue(primary, inherited, fallback string) string {
	if primary != "" {
		return primary
	}
	if inherited != "" {
		return inherited
	}
	return fallback
}

func validateLocalReplacement(base, source string, replacement *modfile.Replace) *graph.Diagnostic {
	if replacement == nil || replacement.New.Version != "" {
		return nil
	}
	path := filepath.FromSlash(replacement.New.Path)
	if filepath.IsAbs(path) || escapes(path) {
		return &graph.Diagnostic{Severity: graph.SeverityError, File: filepath.ToSlash(source), Message: fmt.Sprintf("local module replacement %q is outside the committed repository", replacement.New.Path)}
	}
	directory := filepath.Clean(filepath.Join(base, path))
	if !within(base, directory) {
		return &graph.Diagnostic{Severity: graph.SeverityError, File: filepath.ToSlash(source), Message: fmt.Sprintf("local module replacement %q escapes the committed repository", replacement.New.Path)}
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return &graph.Diagnostic{Severity: graph.SeverityError, File: filepath.ToSlash(source), Message: fmt.Sprintf("local module replacement %q is not a readable directory", replacement.New.Path)}
	}
	return nil
}

func fingerprintPath(path string) bool {
	path = filepath.ToSlash(path)
	base := filepath.Base(path)
	if base == "go.mod" || base == "go.work" || base == "go.sum" || base == "go.work.sum" {
		return true
	}
	return strings.HasSuffix(path, ".go")
}

func escapes(path string) bool {
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func within(base, candidate string) bool {
	relative, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
