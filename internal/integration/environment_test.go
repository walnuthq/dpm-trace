package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/walnuthq/dpm-trace/internal/scaffold"
)

func TestBootModeKeepsAmbientVariablesExceptTokenFiles(t *testing.T) {
	t.Setenv("DPM_TRACE_IT_TOKEN_FILE", "/ambient/token")
	t.Setenv("DPM_TRACE_IT_TOKEN_FILE2", "/ambient/token2")
	t.Setenv("DPM_TRACE_IT_LEDGER2", "http://ambient-second:1")
	extra := map[string]string{"DPM_TRACE_IT_LEDGER": "http://ledger:1"}

	boot := strings.Join(litEnvironment(false, extra), "\n")

	if !strings.Contains(boot, "DPM_TRACE_IT_LEDGER2=http://ambient-second:1") {
		t.Errorf("boot mode environment lost an ambient variable it always kept")
	}
	for _, forbidden := range []string{"DPM_TRACE_IT_TOKEN_FILE=", "DPM_TRACE_IT_TOKEN_FILE2="} {
		if strings.Contains(boot, forbidden) {
			t.Errorf("boot mode environment kept an inherited %s", forbidden)
		}
	}
}

func TestAttachModeDropsAmbientVariablesAndKeepsWhatTheRunnerSet(t *testing.T) {
	t.Setenv("DPM_TRACE_IT_TOKEN_FILE", "/ambient/token")
	t.Setenv("DPM_TRACE_IT_LEDGER2", "http://ambient-second:1")
	extra := map[string]string{"DPM_TRACE_IT_LEDGER": "http://ledger:1", "DPM_TRACE_IT_TOKEN_FILE": "/runner/token"}

	attach := strings.Join(litEnvironment(true, extra), "\n")

	if strings.Contains(attach, "DPM_TRACE_IT_LEDGER2=") {
		t.Errorf("attach mode kept the ambient DPM_TRACE_IT_LEDGER2")
	}
	if strings.Contains(attach, "DPM_TRACE_IT_TOKEN_FILE=/ambient/token") {
		t.Errorf("attach mode kept the ambient DPM_TRACE_IT_TOKEN_FILE")
	}
	for _, want := range []string{"DPM_TRACE_IT_TOKEN_FILE=/runner/token", "DPM_TRACE_IT_LEDGER=http://ledger:1"} {
		if !strings.Contains(attach, want) {
			t.Errorf("attach mode lacks %q", want)
		}
	}
}

func TestFilterEnvironmentComparesKeysCaseInsensitivelyOnWindows(t *testing.T) {
	env := []string{
		"Dpm_Trace_It_Token_File=/ambient/mixed",
		"dpm_trace_it_ledger2=http://ambient-second:1",
		"Dpm_Trace_Token=ambient",
		"dpm_trace_ledger_url=http://ambient-ledger:1",
		"Dpm_Trace_It_Ledger=http://stale:1",
		"DPM_TRACE_IT_LEDGER=http://runner:1",
		"Path=/bin",
	}
	extra := map[string]string{"DPM_TRACE_IT_LEDGER": "http://runner:1"}

	got := filterEnvironment(env, extra, true, true)

	sort.Strings(got)
	want := []string{"DPM_TRACE_IT_LEDGER=http://runner:1", "Path=/bin"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("attach, case-insensitive: got %v, want %v", got, want)
	}

	boot := filterEnvironment(env, extra, false, true)

	sort.Strings(boot)
	wantBoot := []string{
		"DPM_TRACE_IT_LEDGER=http://runner:1",
		"Dpm_Trace_Token=ambient",
		"Path=/bin",
		"dpm_trace_it_ledger2=http://ambient-second:1",
		"dpm_trace_ledger_url=http://ambient-ledger:1",
	}
	if strings.Join(boot, "|") != strings.Join(wantBoot, "|") {
		t.Errorf("boot, case-insensitive: got %v, want %v", boot, wantBoot)
	}
}

func TestFilterEnvironmentKeepsCaseSensitiveKeysApartElsewhere(t *testing.T) {
	env := []string{"dpm_trace_it_token_file=/lower", "DPM_TRACE_IT_LEDGER=http://runner:1", "Dpm_Trace_It_Ledger=http://other:1"}
	extra := map[string]string{"DPM_TRACE_IT_LEDGER": "http://runner:1"}

	got := filterEnvironment(env, extra, false, false)

	sort.Strings(got)
	want := []string{"DPM_TRACE_IT_LEDGER=http://runner:1", "Dpm_Trace_It_Ledger=http://other:1", "dpm_trace_it_token_file=/lower"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func realLit(t *testing.T) string {
	t.Helper()
	lit, err := exec.LookPath("lit")
	if err != nil {
		t.Skip("lit is not installed")
	}
	return lit
}

func authArgumentsRenderedByLit(t *testing.T, attached bool, extra map[string]string) string {
	t.Helper()
	lit := realLit(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "daml.yaml"), []byte("name: probe\nversion: 1.0.0\nsource: daml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scaffold.Init(os.Stderr, scaffold.Options{Root: root, NoUnittests: true, NoCI: true}); err != nil {
		t.Fatal(err)
	}
	suite := filepath.Join(root, "itests")
	if err := os.Remove(filepath.Join(suite, "example.test")); err != nil {
		t.Fatal(err)
	}
	probe := "RUN: for argument in %auth; do echo \"$argument\"; done > %S/arguments.txt\n"
	if err := os.WriteFile(filepath.Join(suite, "probe.test"), []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{"DPM_TRACE_IT_LEDGER": "http://ledger:1", "DPM_TRACE_IT_DPM": "/bin/true"}
	for key, value := range extra {
		base[key] = value
	}
	command := exec.Command(lit, suite, "-v")
	command.Env = litEnvironment(attached, base)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("lit failed: %v\n%s", err, output)
	}
	rendered, err := os.ReadFile(filepath.Join(suite, "arguments.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

func TestBootModeRendersNoTokenFlagThroughRealLitDespiteAnAmbientTokenVariable(t *testing.T) {
	t.Setenv("DPM_TRACE_IT_TOKEN_FILE", "/stale/token")

	rendered := authArgumentsRenderedByLit(t, false, nil)

	if strings.Contains(rendered, "--token-file") || strings.Contains(rendered, "/stale") {
		t.Errorf("boot mode %%auth rendered %q, want nothing", rendered)
	}
}

func TestAttachModeKeepsATokenPathWithLitSubstitutionTextIntactThroughRealLit(t *testing.T) {
	path := "/tmp/test-%alice.token"

	rendered := authArgumentsRenderedByLit(t, true, map[string]string{"DPM_TRACE_IT_TOKEN_FILE": path, "DPM_TRACE_IT_ALICE": "Alice"})

	if want := "--token-file\n" + path + "\n"; rendered != want {
		t.Errorf("%%auth rendered %q, want %q", rendered, want)
	}
}
