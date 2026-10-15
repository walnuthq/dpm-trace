package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These drive the subcommand entry points in-process, so the shipping CLI
// surface is gated by `go test` alone. The golden harness covers the same
// ground byte-for-byte, but it exists to prove parity with the Python
// implementation; these must keep working once that oracle is retired.

// capture runs fn with stdout and stderr redirected, returning what was written
// to each and the exit code fn reported.
func capture(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	realOut, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { data, _ := io.ReadAll(outR); outCh <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errCh <- string(data) }()

	func() {
		defer func() {
			os.Stdout, os.Stderr = realOut, realErr
			outW.Close()
			errW.Close()
		}()
		code = fn()
	}()

	return <-outCh, <-errCh, code
}

func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func TestOpenRendersAnArtifact(t *testing.T) {
	stdout, stderr, code := capture(t, func() int {
		return runOpen([]string{repoPath("examples", "create.trace.json"), "--color", "never"})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{"Trace artifact", "CREATE Asset:Asset", "signatories:", "quantity: 100"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestOpenPrintJSONEmitsTheArtifact(t *testing.T) {
	stdout, _, code := capture(t, func() int {
		return runOpen([]string{repoPath("examples", "create.trace.json"), "--print-json"})
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var artifact map[string]any
	if err := json.Unmarshal([]byte(stdout), &artifact); err != nil {
		t.Fatalf("--print-json did not emit valid JSON: %v", err)
	}
	if artifact["schema"] != "dpm-trace/trace-artifact/v0" {
		t.Errorf("schema = %v", artifact["schema"])
	}
}

// A missing file and a file that is not an artifact must both fail with a
// message on stderr, not a panic and not a silent success.
func TestOpenRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"missing", repoPath("examples", "does-not-exist.json")},
		{"not an artifact", repoPath("tests", "fixtures", "compare", "completion-fail.json")},
	} {
		stdout, stderr, code := capture(t, func() int {
			return runOpen([]string{tc.path, "--color", "never"})
		})
		if code == 0 {
			t.Errorf("%s: exit 0, want failure (stdout: %s)", tc.name, stdout)
		}
		if !strings.Contains(stderr, "error:") {
			t.Errorf("%s: stderr = %q, want an error", tc.name, stderr)
		}
	}
}

func TestCompareUpdateVsUpdate(t *testing.T) {
	stdout, stderr, code := capture(t, func() int {
		return runCompare([]string{
			repoPath("tests", "fixtures", "compare", "trace-a.json"),
			repoPath("tests", "fixtures", "compare", "trace-b.json"),
			"--full", "--color", "never",
		})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{"DPM trace comparison", "update-vs-update", "Event counts", "Root events"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestComparePreparedVsUpdate(t *testing.T) {
	stdout, stderr, code := capture(t, func() int {
		return runCompare([]string{
			"--prepared", repoPath("examples", "transfer.prepared.json"),
			"--update", repoPath("examples", "transfer.trace.json"),
			"--full", "--color", "never",
		})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{"prepared-vs-update", "Operation", "Event diff", "prep hash"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestComparePreparedVsCompletion(t *testing.T) {
	stdout, _, code := capture(t, func() int {
		return runCompare([]string{
			"--prepared", repoPath("tests", "fixtures", "compare", "prepared.json"),
			"--completion-file", repoPath("tests", "fixtures", "compare", "completion-fail.json"),
			"--color", "never",
		})
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "prepared-vs-completion") {
		t.Errorf("wrong comparison kind:\n%s", stdout)
	}
}

// A completion is not a committed transaction: the failed-submission workflow
// must render from completion data with no update id.
func TestTraceFromCompletionFile(t *testing.T) {
	stdout, stderr, code := capture(t, func() int {
		return runTrace([]string{
			"--completion-file", repoPath("examples", "failed-withdraw.completion.json"),
			"--daml-yaml", repoPath("examples", "asset", "daml.yaml"),
			"--color", "never",
		})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{"completion failed", "Insufficient balance", "Source diagnostics", "Asset.daml"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

// Argument validation must fail before any network call is attempted.
func TestSubcommandsRejectMissingArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() int
		want string
	}{
		{"trace without a target", func() int { return runTrace([]string{"--color", "never"}) },
			"update id"},
		{"compare without operands", func() int { return runCompare([]string{"--color", "never"}) },
			"error:"},
		{"prepare without a ledger", func() int { return runPrepare([]string{"--act-as", "Alice", "--template", "T:T"}) },
			"error:"},
		{"submit without a ledger", func() int { return runSubmit([]string{"--act-as", "Alice", "--template", "T:T"}) },
			"error:"},
	} {
		_, stderr, code := capture(t, tc.run)
		if code == 0 {
			t.Errorf("%s: exit 0, want failure", tc.name)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: stderr = %q, want it to mention %q", tc.name, stderr, tc.want)
		}
	}
}

// An unknown flag must be reported rather than ignored, or a typo silently
// changes what the command does.
func TestUnknownFlagIsRejected(t *testing.T) {
	_, stderr, code := capture(t, func() int {
		return runTrace([]string{"1220abcd", "--not-a-flag"})
	})
	if code == 0 {
		t.Fatal("exit 0 for an unknown flag")
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr = %q, want an unknown-flag error", stderr)
	}
}

// install-plugin writes the component and registers it in the SDK manifest.
// Both halves matter: without the manifest entry `dpm trace` does not resolve.
func TestInstallPluginRegistersTheComponent(t *testing.T) {
	home := t.TempDir()
	manifestDir := filepath.Join(home, "cache", "sdk", "open-source")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(manifestDir, "3.5.1.yaml")
	content := "apiVersion: digitalasset.com/v1\nkind: SdkManifest\nspec:\n  components:\n  assistant:\n    version: 1.0.0\n  version: 3.5.1\n  edition: open-source\n"
	if err := os.WriteFile(manifest, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := capture(t, func() int {
		return runInstallPlugin([]string{"--dpm-home", home, "--sdk-version", "3.5.1", "--component-version", "0.1.0"})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Registered") {
		t.Errorf("stdout = %q", stdout)
	}

	component := filepath.Join(home, "cache", "components", "dpm-trace", "0.1.0")
	if _, err := os.Stat(filepath.Join(component, "component.yaml")); err != nil {
		t.Errorf("component.yaml not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(component, "bin", "dpm-trace")); err != nil {
		t.Errorf("binary not installed: %v", err)
	}
	updated, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "dpm-trace:") {
		t.Errorf("manifest not updated:\n%s", updated)
	}
}

// --help must succeed for every subcommand: a non-zero exit here breaks
// `dpm trace <cmd> --help` for users.
func TestSubcommandHelpExits0(t *testing.T) {
	for name, run := range map[string]func() int{
		"open":           func() int { return runOpen([]string{"--help"}) },
		"compare":        func() int { return runCompare([]string{"--help"}) },
		"prepare":        func() int { return runPrepare([]string{"--help"}) },
		"submit":         func() int { return runSubmit([]string{"--help"}) },
		"test":           func() int { return runTest([]string{"--help"}) },
		"install-plugin": func() int { return runInstallPlugin([]string{"--help"}) },
	} {
		stdout, stderr, code := capture(t, run)
		if code != 0 {
			t.Errorf("%s --help exited %d (stderr: %s)", name, code, stderr)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Errorf("%s --help printed no usage:\n%s", name, stdout)
		}
	}
}

// A CantonScan update URL is accepted wherever an update id is, so a user can
// paste a link straight from the explorer.
func TestExtractUpdateID(t *testing.T) {
	const id = "1220c8f9d9b2ca3aa72aae3eee228cb84e30e559dfdcfc2b7189def41a3b4589b6d5"
	for _, in := range []string{
		id,
		"https://scan.example.com/update/" + id,
		"https://scan.example.com/update/" + id + "?tab=events",
		"https://scan.example.com/update/" + id + "#events",
	} {
		if got := extractUpdateID(in); got != id {
			t.Errorf("extractUpdateID(%q) = %q", in, got)
		}
	}
	// Anything unrecognised passes through, so the ledger reports the error.
	if got := extractUpdateID("not-an-id"); got != "not-an-id" {
		t.Errorf("got %q", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := orDefaultString("", "daml"); got != "daml" {
		t.Errorf("orDefaultString(empty) = %q", got)
	}
	if got := orDefaultString("damlc", "daml"); got != "damlc" {
		t.Errorf("orDefaultString(set) = %q", got)
	}
	if got := firstOrEmpty(nil); got != "" {
		t.Errorf("firstOrEmpty(nil) = %q", got)
	}
	if got := firstOrEmpty([]string{"a", "b"}); got != "a" {
		t.Errorf("firstOrEmpty = %q", got)
	}
}

// resolvePath must make a path absolute: the test runner spawns daml from a
// temporary working directory where a relative path would not resolve.
func TestResolvePath(t *testing.T) {
	got, err := resolvePath(".")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolvePath(.) = %q, want absolute", got)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if got, err := resolvePath("~"); err != nil || got == "~" {
			t.Errorf("tilde not expanded: %q (%v)", got, err)
		} else if !strings.HasPrefix(got, filepath.Dir(home)) && got != home {
			t.Logf("resolvePath(~) = %q (symlinks resolved)", got)
		}
	}
}

// An empty party value means the caller's variable was unset. Dropping it
// silently gave a narrower projection than was asked for, with nothing in the
// output naming the parties that were requested rather than used -- so a
// mistyped $ALICE produced a plausible answer to a different question.
func TestPartyFlagsRejectAnEmptyValue(t *testing.T) {
	artifact := repoPath("examples", "create.trace.json")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"trace read-as", []string{"some-update-id", "--submitter", "http://127.0.0.1:1", "--read-as", ""}},
		{"trace party", []string{"some-update-id", "--submitter", "http://127.0.0.1:1", "--party", ""}},
		// The mixed case is the one that bit: one good party hides one empty.
		{"trace mixed", []string{"some-update-id", "--submitter", "http://127.0.0.1:1", "--read-as", "", "--read-as", "Alice::1220aa"}},
		// On the completion path a narrowed party set reads as "your failed
		// submission is not in this window" -- the same plausible-but-wrong
		// answer, from a different command.
		{"trace act-as", []string{"--command-id", "c1", "--submitter", "http://127.0.0.1:1", "--act-as", ""}},
		{"trace act-as mixed", []string{"--command-id", "c1", "--submitter", "http://127.0.0.1:1", "--act-as", "", "--act-as", "Alice::1220aa"}},
		{"compare read-as", []string{"--update", artifact, "--submitter", "http://127.0.0.1:1", "--read-as", ""}},
		{"compare act-as", []string{"--update", artifact, "--submitter", "http://127.0.0.1:1", "--act-as", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			var stderr string
			if strings.HasPrefix(tc.name, "compare") {
				_, stderr, code = capture(t, func() int { return runCompare(tc.args) })
			} else {
				_, stderr, code = capture(t, func() int { return runTrace(tc.args) })
			}
			if code == 0 {
				t.Errorf("empty party accepted, exit 0")
			}
			if !strings.Contains(stderr, "requires a party id") {
				t.Errorf("stderr = %q, want a party-id error", stderr)
			}
		})
	}
}

// A prepared artifact is the other thing --export writes. Refusing to reopen
// it meant a prepared command could only ever be diffed against something,
// never looked at again -- which is most of the point of exporting it.
func TestOpenRendersAPreparedArtifact(t *testing.T) {
	prepared := repoPath("tests", "fixtures", "compare", "prepared.json")
	stdout, stderr, code := capture(t, func() int { return runOpen([]string{prepared}) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	for _, want := range []string{
		"Prepared command",
		"committed:    no",       // labelled prepared, not committed
		"dpm-trace-prepare-001",  // the command id
		"create Counter:Counter", // the command itself, not just a count
		"owner=Alice",            // and its arguments
		"non-committing prepare call",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q:\n%s", want, stdout)
		}
	}
}

func TestOpenPreparedPrintJSON(t *testing.T) {
	prepared := repoPath("tests", "fixtures", "compare", "prepared.json")
	stdout, _, code := capture(t, func() int { return runOpen([]string{prepared, "--print-json"}) })
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if decoded["schema"] != "dpm-trace/prepared-artifact/v0" {
		t.Errorf("schema = %v", decoded["schema"])
	}
}

// A live participant returns a cost estimation and a hashing scheme version;
// the older fixture carries neither. "cost: returned" said a field existed
// without saying what it was.
func TestOpenPreparedReportsCostAndHashing(t *testing.T) {
	prepared := repoPath("tests", "fixtures", "compare", "prepared-with-cost.json")
	stdout, _, code := capture(t, func() int { return runOpen([]string{prepared}) })
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"hashing:      HASHING_SCHEME_VERSION_V2",
		"cost:         0 traffic (request 0, response 0)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q:\n%s", want, stdout)
		}
	}
}

// An artifact without those fields must not grow empty lines for them.
func TestOpenPreparedOmitsAbsentCostAndHashing(t *testing.T) {
	prepared := repoPath("tests", "fixtures", "compare", "prepared.json")
	stdout, _, _ := capture(t, func() int { return runOpen([]string{prepared}) })
	for _, absent := range []string{"hashing:", "cost:"} {
		if strings.Contains(stdout, absent) {
			t.Errorf("reported %q for an artifact that has none:\n%s", absent, stdout)
		}
	}
}

// --visualize must not be silently ignored: a prepared command has no tree, so
// say so rather than printing the summary as though the flag were absent.
func TestOpenPreparedExplainsWhyThereIsNoSession(t *testing.T) {
	prepared := repoPath("tests", "fixtures", "compare", "prepared.json")
	stdout, _, _ := capture(t, func() int { return runOpen([]string{prepared, "--visualize"}) })
	if !strings.Contains(stdout, "no transaction tree to step through") {
		t.Errorf("--visualize was ignored without explanation:\n%s", stdout)
	}
}

func TestOpenPointsAtCompletionFile(t *testing.T) {
	for _, artifact := range []string{
		repoPath("tests", "fixtures", "compare", "completion-fail.json"),
		repoPath("tests", "fixtures", "compare", "completion-ok.json"),
	} {
		_, stderr, code := capture(t, func() int { return runOpen([]string{artifact}) })
		if code == 0 {
			t.Errorf("%s: open accepted completion data", artifact)
		}
		if !strings.Contains(stderr, "--completion-file") {
			t.Errorf("%s: error does not name the right flag: %s", artifact, stderr)
		}
	}
}

func TestOpenRejectsAFileWithNoSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.json")
	if err := os.WriteFile(path, []byte(`{"hello": "world"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := capture(t, func() int { return runOpen([]string{path}) })
	if code == 0 {
		t.Error("open accepted a file with no schema")
	}
	if !strings.Contains(stderr, "no schema field") || strings.Contains(stderr, "None") {
		t.Errorf("unhelpful error: %s", stderr)
	}
}

// submit parsed --export but only prepare ever used it, so the flag was
// advertised in --help and silently ignored. The rejection path is the one
// that matters: --completion-file reads the file back.
func TestSubmitExportWritesTheResponse(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	body = `{"code":"DAML_FAILURE","cause":"boom","errorCategory":9}`

	export := filepath.Join(t.TempDir(), "rejection.json")
	_, _, code := capture(t, func() int {
		return runSubmit([]string{
			"--submitter", server.URL, "--act-as", "Alice::1220aa",
			"--template", "pkg:M:T", "--arg", "x=1",
			"--allow-fail", "--export", export,
		})
	})
	if code != 0 {
		t.Fatalf("--allow-fail did not exit 0: %d", code)
	}

	written, err := os.ReadFile(export)
	if err != nil {
		t.Fatalf("--export wrote nothing: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(written, &decoded); err != nil {
		t.Fatalf("exported file is not JSON: %v", err)
	}
	if decoded["code"] != "DAML_FAILURE" {
		t.Errorf("exported %v, want the rejection body", decoded)
	}
}

// A command that accepts a flag it never reads makes the flag look like it did
// something. prepare and submit share a parser but not a purpose, so each
// accepts exactly what its own help lists -- the parser derives its set from
// that listing, and this pins both ends of it.
func TestSubmissionFlagsMatchEachCommand(t *testing.T) {
	submitOnly := []string{
		"--allow-fail", "--full", "-v", "--verbose", "--log-file", "--color",
		"--daml-yaml", "--dar", "--damlc", "--debug-info", "--source-root",
		"--max-source-locations",
	}
	shared := []string{
		"--submitter", "--act-as", "--read-as", "--party", "--token",
		"--token-file", "--config", "--template", "--contract-id", "--choice",
		"--arg", "--args-json", "--args-file", "--commands", "--command-json",
		"--command-id", "--user-id", "--export", "--out", "--print-json",
		"--synchronizer-id",
	}
	// Neither command reads these: submitting through Scan is not a thing, and
	// submit-and-wait already waits.
	rejectedByBoth := []string{"--wait", "--scan-url"}

	rejects := func(run func([]string) int, flag string) bool {
		t.Helper()
		_, stderr, _ := capture(t, func() int {
			return run([]string{"--submitter", "http://127.0.0.1:1", flag, "x"})
		})
		return strings.Contains(stderr, "unknown flag "+strconv.Quote(flag))
	}

	for _, flag := range submitOnly {
		if rejects(runSubmit, flag) {
			t.Errorf("submit rejects %s, which it acts on", flag)
		}
		if !rejects(runPrepare, flag) {
			t.Errorf("prepare accepts %s, which only submit acts on", flag)
		}
	}
	for _, flag := range shared {
		if rejects(runSubmit, flag) {
			t.Errorf("submit rejects %s", flag)
		}
		if flag == "--synchronizer-id" {
			continue
		}
		if rejects(runPrepare, flag) {
			t.Errorf("prepare rejects %s", flag)
		}
	}
	for _, flag := range rejectedByBoth {
		if !rejects(runSubmit, flag) || !rejects(runPrepare, flag) {
			t.Errorf("%s is accepted but neither command acts on it", flag)
		}
	}
}

// -v is an alias for --full, so it must reach the same option.
func TestVerboseIsAnAliasForFull(t *testing.T) {
	for _, flag := range []string{"-v", "--verbose", "--full"} {
		opts, _, code := parseSubmissionFlags("submit", []string{flag})
		if code != 0 {
			t.Fatalf("%s exited %d", flag, code)
		}
		if !opts.full {
			t.Errorf("%s did not set full", flag)
		}
	}
}

// -v means --full everywhere it exists, and help says so: an alias nobody can
// find is the same as no alias.
func TestVerboseAliasIsUniformAndDocumented(t *testing.T) {
	for _, tc := range []struct {
		name string
		help func() int
	}{
		{"submit", func() int { return runSubmit([]string{"--help"}) }},
		{"compare", func() int { return runCompare([]string{"--help"}) }},
	} {
		stdout, _, code := capture(t, tc.help)
		if code != 0 {
			t.Fatalf("%s --help exited %d", tc.name, code)
		}
		var row string
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "-v, --verbose") {
				row = strings.TrimSpace(line)
			}
		}
		if row == "" {
			t.Errorf("%s --help does not list -v, --verbose", tc.name)
		} else if !strings.Contains(row, "--full") {
			t.Errorf("%s --help does not name the --full alias: %q", tc.name, row)
		}
	}

	// compare renders the same thing either way.
	artifact := repoPath("examples", "transfer.trace.json")
	drifted := repoPath("examples", "transfer-drifted.trace.json")
	var rendered []string
	for _, flag := range []string{"-v", "--verbose", "--full"} {
		out, _, _ := capture(t, func() int {
			return runCompare([]string{artifact, drifted, flag, "--color", "never"})
		})
		rendered = append(rendered, out)
	}
	for i, out := range rendered[1:] {
		if out != rendered[0] {
			t.Errorf("compare renders differently for spelling %d", i+1)
		}
	}
}

// compare hand-rolled its parser, so it accepted flags its help never listed
// and silently ignored two it did. It now uses the same derivation as prepare
// and submit: the parser's set comes from the help listing.
func TestCompareFlagsMatchItsHelp(t *testing.T) {
	stdout, _, code := capture(t, func() int { return runCompare([]string{"--help"}) })
	if code != 0 {
		t.Fatalf("compare --help exited %d", code)
	}
	documented := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "-") {
			continue
		}
		documented[strings.TrimSuffix(fields[0], ",")] = true
	}

	// Used by the source index, and previously undocumented.
	for _, flag := range []string{
		"--daml-yaml", "--source-root", "--debug-info", "--damlc",
		"--max-source-locations", "--completion-timeout-ms", "--completion-user-id",
	} {
		if !documented[flag] {
			t.Errorf("compare --help omits %s, which it acts on", flag)
		}
	}

	// Neither the fetcher nor anything else read these.
	for _, flag := range []string{"--wait", "--source"} {
		if documented[flag] {
			t.Errorf("compare --help documents %s, which nothing reads", flag)
		}
		_, stderr, _ := capture(t, func() int {
			return runCompare([]string{"a.json", "b.json", flag, "x"})
		})
		if !strings.Contains(stderr, "unknown flag "+strconv.Quote(flag)) {
			t.Errorf("compare accepts %s, which nothing reads", flag)
		}
	}
}

// completionLookup.fetch hardcoded the window and dropped the four fields the
// struct carries, so `compare --command-id X --completion-limit 500` searched
// the default window anyway -- a documented flag that did nothing.
func TestCompareCompletionLookupCarriesItsWindow(t *testing.T) {
	var query, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	_, _, _ = capture(t, func() int {
		return runCompare([]string{
			"--prepared", repoPath("examples", "transfer.prepared.json"),
			"--command-id", "c1", "--submitter", server.URL, "--act-as", "Alice::1220aa",
			"--begin-exclusive", "42", "--completion-limit", "500",
			"--completion-timeout-ms", "250", "--completion-user-id", "someone",
		})
	})

	if !strings.Contains(query, "limit=500") {
		t.Errorf("query = %q, want limit=500", query)
	}
	if !strings.Contains(query, "stream_idle_timeout_ms=250") {
		t.Errorf("query = %q, want the requested timeout", query)
	}
	for _, want := range []string{"42", "someone"} {
		if !strings.Contains(body, want) {
			t.Errorf("request body does not carry %s: %s", want, body)
		}
	}
}
