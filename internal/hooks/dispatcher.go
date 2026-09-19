package hooks

import (
	"fmt"
	"strings"
)

const managedMarker = "# jejak-managed-hook: v1"

// GenerateDispatcher returns a portable shell dispatcher for one supported
// Git hook. The original hook is invoked first from an external backup, then
// the normal committed-graph sync command is run for the worktree Git used to
// invoke the hook. Sync failures are diagnostic only; the original exit code
// remains authoritative.
func GenerateDispatcher(name, originalPath, dataRoot string) []byte {
	var builder strings.Builder
	fmt.Fprintf(&builder, "#!/bin/sh\n%s\n", managedMarker)
	fmt.Fprintf(&builder, "# Jejak dispatcher for %s.\n", name)
	builder.WriteString("jejak_original_status=0\n")
	if originalPath != "" {
		fmt.Fprintf(&builder, "if [ -x %s ]; then\n", shellQuote(originalPath))
		fmt.Fprintf(&builder, "    %s \"$@\"\n", shellQuote(originalPath))
		builder.WriteString("    jejak_original_status=$?\n")
		builder.WriteString("fi\n")
	}
	builder.WriteString("JEJAK_BIN=${JEJAK_BIN:-jejak}\n")
	fmt.Fprintf(&builder, "\"$JEJAK_BIN\" --data-dir %s --worktree \"${PWD:-.}\" sync --quiet >/dev/null 2>/dev/null\n", shellQuote(dataRoot))
	builder.WriteString("jejak_sync_status=$?\n")
	builder.WriteString("if [ \"$jejak_sync_status\" -ne 0 ]; then\n")
	fmt.Fprintf(&builder, "    echo %s\"$jejak_sync_status\"%s >&2\n", shellQuote("jejak: "+name+" sync failed (exit "), shellQuote(")"))
	builder.WriteString("fi\n")
	builder.WriteString("exit \"$jejak_original_status\"\n")
	return []byte(builder.String())
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
