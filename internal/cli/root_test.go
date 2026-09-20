package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseArgsSupportsExplicitPaths(t *testing.T) {
	options, command, commandArgs, err := parseArgs([]string{
		"--data-dir=/tmp/jejak data",
		"-C=/tmp/worktree with spaces",
		"status",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.DataDir != "/tmp/jejak data" || options.WorktreePath != "/tmp/worktree with spaces" {
		t.Fatalf("options = %#v", options)
	}
	if command != "status" || len(commandArgs) != 0 {
		t.Fatalf("command=%q args=%q", command, commandArgs)
	}
}

func TestParseArgsRejectsEmptyInlinePaths(t *testing.T) {
	for _, arg := range []string{"-C=", "--worktree=", "--data-dir="} {
		if _, _, _, err := parseArgs([]string{arg, "status"}); err == nil {
			t.Errorf("parseArgs(%q) unexpectedly succeeded", arg)
		}
	}
}

func TestParseBuildOptionsBeforeAndAfterCommand(t *testing.T) {
	options, command, commandArgs, err := parseArgs([]string{"--download-deps", "--tags=unit,linux", "graph", "symbol", "Answer", "--goos", "linux", "--goarch=arm64", "--cgo=0"})
	if err != nil {
		t.Fatal(err)
	}
	options, commandArgs, err = parseCommandOptions(options, commandArgs)
	if err != nil {
		t.Fatal(err)
	}
	if command != "graph" || len(commandArgs) != 2 || commandArgs[0] != "symbol" || commandArgs[1] != "Answer" {
		t.Fatalf("command=%q args=%q", command, commandArgs)
	}
	if !options.DownloadDependencies || options.GOOS != "linux" || options.GOARCH != "arm64" || options.CGOEnabled != "0" || len(options.Tags) != 2 {
		t.Fatalf("build options = %#v", options)
	}
}

func TestParseQuietBeforeAndAfterCommand(t *testing.T) {
	options, command, commandArgs, err := parseArgs([]string{"--quiet", "sync", "-q"})
	if err != nil {
		t.Fatal(err)
	}
	options, commandArgs, err = parseCommandOptions(options, commandArgs)
	if err != nil {
		t.Fatal(err)
	}
	if command != "sync" || !options.Quiet || len(commandArgs) != 0 {
		t.Fatalf("quiet parse command=%q options=%#v args=%q", command, options, commandArgs)
	}
}

func TestParseInitNoHooksBeforeAndAfterCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--no-hooks", "init"},
		{"init", "--no-hooks"},
	} {
		options, command, commandArgs, err := parseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		options, commandArgs, err = parseCommandOptions(options, commandArgs)
		if err != nil {
			t.Fatal(err)
		}
		if command != "init" || !options.NoHooks || len(commandArgs) != 0 {
			t.Fatalf("args=%q command=%q options=%#v commandArgs=%q", args, command, options, commandArgs)
		}
	}
}

func TestParseInitNoAgentSkillsBeforeAndAfterCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--no-agent-skills", "init"},
		{"init", "--no-agent-skills"},
	} {
		options, command, commandArgs, err := parseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		options, commandArgs, err = parseCommandOptions(options, commandArgs)
		if err != nil {
			t.Fatal(err)
		}
		if command != "init" || !options.NoAgentSkills || len(commandArgs) != 0 {
			t.Fatalf("args=%q command=%q options=%#v commandArgs=%q", args, command, options, commandArgs)
		}
	}
}

func TestParseCommittedModeBeforeAndAfterCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--committed", "impact", "--task", "Answer"},
		{"impact", "--committed", "--task", "Answer"},
	} {
		options, command, commandArgs, err := parseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		options, commandArgs, err = parseCommandOptions(options, commandArgs)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parseTaskCommandOptions(options, commandArgs)
		if err != nil {
			t.Fatal(err)
		}
		if command != "impact" || !parsed.Committed || parsed.WorkingTree || parsed.Request.Task != "Answer" {
			t.Fatalf("args=%q command=%q options=%#v parsed=%#v", args, command, options, parsed)
		}
	}
}

func TestParseTaskRejectsConflictingSourceModes(t *testing.T) {
	if _, err := parseTaskCommandOptions(Options{}, []string{"--working-tree", "--committed", "--task", "Answer"}); err == nil {
		t.Fatal("conflicting source modes unexpectedly succeeded")
	}
	if _, _, err := parseGraphCommandOptions(Options{}, []string{"--working-tree", "--committed", "symbol", "Answer"}); err == nil {
		t.Fatal("conflicting graph source modes unexpectedly succeeded")
	}
}

func TestParseGraphOutputFormats(t *testing.T) {
	tests := []struct {
		name       string
		options    Options
		args       []string
		wantFormat graphOutputFormat
		wantArgs   []string
		wantErr    bool
	}{
		{name: "default text", args: []string{"symbol", "Answer"}, wantFormat: graphOutputText, wantArgs: []string{"symbol", "Answer"}},
		{name: "format value", args: []string{"--format", "json", "symbol", "Answer"}, wantFormat: graphOutputJSON, wantArgs: []string{"symbol", "Answer"}},
		{name: "format inline", args: []string{"--format=dot", "file", "main.go"}, wantFormat: graphOutputDOT, wantArgs: []string{"file", "main.go"}},
		{name: "json compatibility", options: Options{JSON: true}, args: []string{"symbol", "Answer"}, wantFormat: graphOutputJSON, wantArgs: []string{"symbol", "Answer"}},
		{name: "json compatibility after command", args: []string{"--json", "symbol", "Answer"}, wantFormat: graphOutputJSON, wantArgs: []string{"symbol", "Answer"}},
		{name: "unsupported format", args: []string{"--format", "svg", "symbol", "Answer"}, wantErr: true},
		{name: "missing format", args: []string{"--format"}, wantErr: true},
		{name: "conflicting format", args: []string{"--format", "text", "--format=dot", "symbol", "Answer"}, wantErr: true},
		{name: "json and dot conflict", options: Options{JSON: true}, args: []string{"--format", "dot", "symbol", "Answer"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, args, err := parseGraphCommandOptions(test.options, test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if got.Format != test.wantFormat {
				t.Fatalf("format = %q, want %q", got.Format, test.wantFormat)
			}
			if strings.Join(args, "\x00") != strings.Join(test.wantArgs, "\x00") {
				t.Fatalf("args = %q, want %q", args, test.wantArgs)
			}
		})
	}
}

func TestParseDoctorOptions(t *testing.T) {
	options, command, args, err := parseArgs([]string{"doctor", "--repair", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	options, args, err = parseCommandOptions(options, args)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseDoctorCommandOptions(options, args)
	if err != nil {
		t.Fatal(err)
	}
	if command != "doctor" || !parsed.Repair || !parsed.JSON || len(args) != 1 || args[0] != "--repair" {
		t.Fatalf("doctor command=%q parsed=%#v args=%q", command, parsed, args)
	}
}

func TestParseGCOptions(t *testing.T) {
	options, command, args, err := parseArgs([]string{"gc", "--dry-run", "--keep-generations=3", "--older-than", "2h"})
	if err != nil {
		t.Fatal(err)
	}
	options, args, err = parseCommandOptions(options, args)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseGCCommandOptions(options, args)
	if err != nil {
		t.Fatal(err)
	}
	if command != "gc" || !parsed.DryRun || parsed.KeepGenerations != 3 || parsed.OlderThan != 2*time.Hour || len(args) != 4 {
		t.Fatalf("gc command=%q parsed=%#v args=%q", command, parsed, args)
	}
}

func TestRunHelpAndUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "Commands implemented") || !strings.Contains(stdout.String(), "init") || stderr.Len() != 0 {
		t.Fatalf("help output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown command exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("usage error output = %q", stderr.String())
	}
}

func TestRunPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"status"}, &stdout, &stderr); code != 1 {
		t.Fatalf("cancelled status exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("cancellation output = %q", stderr.String())
	}
}

func TestParseIncludeTestsBeforeAndAfterCommand(t *testing.T) {
	options, command, commandArgs, err := parseArgs([]string{"--include-tests", "init", "--no-hooks"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.IncludeTests || command != "init" {
		t.Fatalf("global --include-tests options=%#v command=%q", options, command)
	}
	options, commandArgs, err = parseCommandOptions(options, commandArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !options.NoHooks || len(commandArgs) != 0 {
		t.Fatalf("command options=%#v args=%q", options, commandArgs)
	}

	options, command, commandArgs, err = parseArgs([]string{"sync", "--include-tests", "--quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if options.IncludeTests || command != "sync" {
		t.Fatalf("options before command parsing=%#v command=%q", options, command)
	}
	options, commandArgs, err = parseCommandOptions(options, commandArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !options.IncludeTests || !options.Quiet || len(commandArgs) != 0 {
		t.Fatalf("command-position --include-tests options=%#v args=%q", options, commandArgs)
	}
	if !buildConfig(options).IncludeTests {
		t.Fatal("build configuration dropped IncludeTests")
	}
	if buildConfig(Options{}).IncludeTests {
		t.Fatal("tests are included by default")
	}
}
