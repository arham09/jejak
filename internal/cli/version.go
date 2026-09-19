package cli

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// version and revision are stamped at link time by release builds, for
// example by the Homebrew formula. Source builds leave them empty and fall
// back to the module build information recorded by the Go toolchain.
var (
	version  = ""
	revision = ""
)

const (
	unknownVersion  = "(devel)"
	unknownRevision = "unknown"
)

// buildVersion reports the release version. A linker-stamped value wins; a
// module build records the selected module version; a plain `go build` of the
// working tree reports the development placeholder.
func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return unknownVersion
	}
	return info.Main.Version
}

// buildRevision reports the source revision. A linker-stamped value wins; the
// Go toolchain otherwise records the VCS revision for builds inside a
// repository.
func buildRevision() string {
	if revision != "" {
		return revision
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownRevision
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	return unknownRevision
}

func (a *App) version(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: version accepts no arguments", errUsage)
	}
	writeVersion(a.stdout)
	return nil
}

func writeVersion(output io.Writer) {
	fmt.Fprintf(output, "jejak       %s\n", buildVersion())
	fmt.Fprintf(output, "revision    %s\n", buildRevision())
	fmt.Fprintf(output, "go          %s\n", runtime.Version())
	fmt.Fprintf(output, "platform    %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
