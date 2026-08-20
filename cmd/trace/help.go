package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// Help follows Go CLI convention -- Usage, Commands, Flags, Examples -- rather
// than reproducing argparse's layout. Columns are aligned with text/tabwriter.
//
// Only flags this binary implements are listed. Advertising a flag the Go build
// silently ignores would be worse than omitting it, so anything unported is
// named in a closing note.

type flagDoc struct {
	name  string // "--submitter URL"
	usage string
}

type commandDoc struct {
	name  string
	usage string
}

func writeSection(w io.Writer, title string, rows [][2]string) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", title)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, row := range rows {
		fmt.Fprintf(tw, "  %s\t%s\n", row[0], row[1])
	}
	_ = tw.Flush()
}

func flagRows(flags []flagDoc) [][2]string {
	rows := make([][2]string, 0, len(flags))
	for _, flag := range flags {
		rows = append(rows, [2]string{flag.name, flag.usage})
	}
	return rows
}

func commandRows(commands []commandDoc) [][2]string {
	rows := make([][2]string, 0, len(commands))
	for _, command := range commands {
		rows = append(rows, [2]string{command.name, command.usage})
	}
	return rows
}

// rootHelp documents the whole tool.
var listedCommands = []commandDoc{
	{"open", "Reopen an exported trace artifact."},
	{"install-plugin", "Register this binary as a DPM component."},
}

func rootHelp(w io.Writer) {
	fmt.Fprintln(w, "dpm-trace inspects participant-scoped Canton transactions.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  dpm trace <update-id> [flags]")
	fmt.Fprintln(w, "  dpm trace <command> [flags]")

	writeSection(w, "Commands", commandRows(listedCommands))

	writeSection(w, "Flags", flagRows(traceFlags))

	fmt.Fprintln(w, "\nExamples:")
	for _, example := range []string{
		"dpm trace <update-id> --submitter http://localhost:7575 --read-as '<party>'",
		"dpm trace open trace.json",
		"dpm trace <update-id> --export trace.json",
	} {
		fmt.Fprintf(w, "  %s\n", example)
	}

	fmt.Fprintln(w, "\nOutput is participant-scoped: it is one participant's projection, not a")
	fmt.Fprintln(w, "global Canton transaction.")
}

var traceFlags = []flagDoc{
	{"--submitter URL", "Ledger JSON API base URL. Alias of --ledger-url, --participant-url."},
	{"--scan-url URL", "Scan API base URL, e.g. https://.../api/scan."},
	{"--read-as PARTY", "Party to read as. Repeatable. Alias of --party."},
	{"--token TOKEN", "Bearer token for the Ledger JSON API."},
	{"--token-file PATH", "Bearer token file. Alias of --access-token-file."},
	{"--completion-file PATH", "Inspect a captured completion instead of an update."},
	{"--daml-yaml PATH", "daml.yaml for local source diagnostics. Repeatable."},
	{"--dar PATH", "Local DAR, verified with damlc inspect. Repeatable."},
	{"--damlc PATH", "damlc or daml executable for inspection. Defaults to daml."},
	{"--debug-info PATH", "daml-debug-info/v1 file for source positions. Repeatable."},
	{"--wait SECONDS", "Retry while the update is not yet visible on this participant."},
	{"--export PATH", "Write a portable trace artifact. Alias of --out."},
	{"--print-json", "Print the normalized trace JSON and exit."},
	{"--source MODE", "auto, scan or ledger. Defaults to auto."},
	{"--source-root PATH", "Local Daml source root for diagnostics. Repeatable."},
	{"--log-file PATH", "Operator log to correlate with the completion. Repeatable."},
	{"--command-id ID", "Look up a failed submission by command id."},
	{"--max-source-locations N", "Maximum diagnostics to resolve. Defaults to 5."},
	{"--explain-apis", "Explain Scan API vs Ledger API."},
	{"--config PATH", "Config JSON. Defaults to .dpm-trace.json in this directory or a parent."},
	{"--color MODE", "auto, always or never. Defaults to auto."},
	{"-h, --help", "Show this help."},
}

// commandHelp documents one subcommand.
func commandHelp(w io.Writer, usage, description string, flags []flagDoc, notPorted string) {
	fmt.Fprintln(w, description)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  %s\n", usage)
	writeSection(w, "Flags", flagRows(flags))
	if notPorted != "" {
		fmt.Fprintf(w, "\nNot supported:\n  %s\n", notPorted)
	}
}

var openFlags = []flagDoc{
	{"--print-json", "Print the artifact JSON and exit."},
	{"--debug-info PATH", "daml-debug-info/v1 file for source positions. Repeatable."},
	{"--color MODE", "auto, always or never. Defaults to auto."},
	{"-h, --help", "Show this help."},
}

var compareFlags = []flagDoc{
	{"--prepared PATH", "Prepared artifact to compare from."},
	{"--submitter URL", "Ledger JSON API base URL, to fetch updates by id."},
	{"--scan-url URL", "Scan API base URL, to fetch updates by id."},
	{"--read-as PARTY", "Party to read as. Repeatable. Alias of --party."},
	{"--token TOKEN", "Bearer token for the Ledger JSON API."},
	{"--token-file PATH", "Bearer token file. Alias of --access-token-file."},
	{"--dar PATH", "Local DAR. Recorded only; damlc inspect is not ported."},
	{"--config PATH", "Config JSON. Defaults to .dpm-trace.json in this directory or a parent."},
	{"--update PATH", "Committed trace artifact to compare against."},
	{"--completion-file PATH", "Captured completion to compare against."},
	{"--full", "Verbose comparison instead of the compact view."},
	{"--print-json", "Print the comparison JSON and exit."},
	{"--color MODE", "auto, always or never. Defaults to auto."},
	{"-h, --help", "Show this help."},
}

var submissionFlags = []flagDoc{
	{"--submitter URL", "Ledger JSON API base URL. Alias of --ledger-url, --participant-url."},
	{"--act-as PARTY", "Submitting party. Repeatable. Required."},
	{"--read-as PARTY", "Additional read party. Repeatable."},
	{"--template ID", "Template id for a create or exercise."},
	{"--contract-id ID", "Contract id for an exercise."},
	{"--choice NAME", "Choice name for an exercise."},
	{"--arg KEY=VALUE", "Argument assignment. Repeatable."},
	{"--args-json JSON", "Arguments as a JSON object."},
	{"--args-file PATH", "Arguments from a JSON file."},
	{"--commands PATH", "Commands from a JSON file."},
	{"--command-json JSON", "Commands as JSON."},
	{"--command-id ID", "Command id. Generated when omitted."},
	{"--user-id ID", "Ledger API user id."},
	{"--token TOKEN", "Bearer token for the Ledger JSON API."},
	{"--token-file PATH", "Bearer token file. Alias of --access-token-file."},
	{"--export PATH", "Write the artifact to a file."},
	{"--print-json", "Print the JSON and exit."},
	{"--source MODE", "auto, scan or ledger. Defaults to auto."},
	{"--source-root PATH", "Local Daml source root for diagnostics. Repeatable."},
	{"--log-file PATH", "Operator log to correlate with the completion. Repeatable."},
	{"--command-id ID", "Look up a failed submission by command id."},
	{"--max-source-locations N", "Maximum diagnostics to resolve. Defaults to 5."},
	{"--explain-apis", "Explain Scan API vs Ledger API."},
	{"--config PATH", "Config JSON. Defaults to .dpm-trace.json in this directory or a parent."},
	{"-h, --help", "Show this help."},
}

var installPluginFlags = []flagDoc{
	{"--dpm-home PATH", "DPM home to register into. Defaults to $DPM_HOME or ~/.dpm."},
	{"--sdk-version VERSION", "SDK manifest to register into. Defaults to the active SDK."},
	{"--component-version VERSION", "Component version to register."},
	{"-h, --help", "Show this help."},
}

// wantsHelp reports whether -h/--help appears in args.
func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}
