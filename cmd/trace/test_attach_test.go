package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTestRejectsLedgerURLWithCantonJar(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--ledger-url", "http://localhost:1", "--canton-jar", "x.jar"})
	})
	if code != 2 || !strings.Contains(stderr, "--canton-jar") {
		t.Errorf("code=%d stderr=%q, want exit 2 naming --canton-jar", code, stderr)
	}
}

func TestTestRejectsAnEmptyCantonJarWithLedgerURL(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--ledger-url", "http://localhost:1", "--canton-jar", ""})
	})
	if code != 2 || !strings.Contains(stderr, "--canton-jar") {
		t.Errorf("code=%d stderr=%q, want exit 2 naming --canton-jar", code, stderr)
	}
}

func TestTestAttachIgnoresAnAmbientCantonJar(t *testing.T) {
	t.Setenv("DPM_TRACE_CANTON_JAR", "/ambient/canton.jar")
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--ledger-url", "http://localhost:1", "--token-file", filepath.Join(dir, "missing")})
	})
	if code != 2 || strings.Contains(stderr, "canton-jar") {
		t.Errorf("code=%d stderr=%q, want an error about the token file only", code, stderr)
	}
}

func TestTestRejectsMisalignedLedgerURLs(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--parties", "Alice@1,Bob@2", "--ledger-url", "http://localhost:1"})
	})
	if code != 2 || !strings.Contains(stderr, "one --ledger-url per participant") {
		t.Errorf("code=%d stderr=%q, want exit 2 about alignment", code, stderr)
	}
}

func TestTestRejectsTokenFileWithoutLedgerURL(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--token-file", "token.txt"})
	})
	if code != 2 || !strings.Contains(stderr, "--token-file") || !strings.Contains(stderr, "--ledger-url") {
		t.Errorf("code=%d stderr=%q, want exit 2 naming --token-file and --ledger-url", code, stderr)
	}
}

func TestTestRejectsMismatchedTokenFileCount(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--parties", "Alice@1,Bob@2",
			"--ledger-url", "http://localhost:1", "--ledger-url", "http://localhost:2", "--token-file", "token.txt"})
	})
	if code != 2 || !strings.Contains(stderr, "exactly one --token-file per --ledger-url") {
		t.Errorf("code=%d stderr=%q, want exit 2 about token file count", code, stderr)
	}
}

func TestTestRejectsAttachFlagsOutsideIntegrationMode(t *testing.T) {
	for _, flags := range [][]string{{"--ledger-url", "http://localhost:1"}, {"--token-file", "token.txt"}} {
		_, stderr, code := capture(t, func() int {
			return runTest(append([]string{t.TempDir()}, flags...))
		})
		if code != 2 || !strings.Contains(stderr, "only with --integration") {
			t.Errorf("%v: code=%d stderr=%q, want exit 2 about --integration", flags, code, stderr)
		}
	}
}

func TestTestIgnoresAttachEnvironmentVariables(t *testing.T) {
	for _, name := range []string{"DPM_TRACE_LEDGER_URL", "DPM_TRACE_TOKEN", "DPM_TRACE_TOKEN_FILE"} {
		t.Setenv(name, "http://localhost:1")
	}
	dir := t.TempDir()
	_, stderr, code := capture(t, func() int {
		return runTest([]string{dir, "--integration", dir, "--canton-jar", "missing.jar"})
	})
	if code != 2 || !strings.Contains(stderr, "--canton-jar") {
		t.Errorf("code=%d stderr=%q, want boot mode (jar check), not attach mode", code, stderr)
	}
}
