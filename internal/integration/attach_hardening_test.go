package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/walnuthq/dpm-trace/internal/ledger"
)

func TestInFlightGrantCompletesBeforeRevokeWhenSetupIsCancelled(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.grantGate = make(chan struct{})
	fake.grantArrived = make(chan struct{}, 1)
	participants := []Participant{{URL: fake.server.URL, TokenFile: writeToken(t, "ta")}}
	ctx, cancel := context.WithCancel(context.Background())

	type outcome struct {
		grants *Grants
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		_, grants, err := Attach(ctx, io.Discard, participants, []byte("dar"), ParsePlacements("Alice"), "s", 5*time.Second)
		finished <- outcome{grants, err}
	}()

	select {
	case <-fake.grantArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the grant never reached the participant")
	}
	cancel()
	time.AfterFunc(300*time.Millisecond, func() { close(fake.grantGate) })

	got := <-finished
	got.grants.Revoke(io.Discard)
	time.Sleep(600 * time.Millisecond)

	if remaining := fake.activeRights(); remaining != 0 {
		t.Errorf("%d rights remain active after teardown: a revoke overtook the grant the server had accepted", remaining)
	}
}

func TestGrantIsNotIssuedOnceSetupIsCancelled(t *testing.T) {
	fake := newFakeParticipant(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	grants := &Grants{}

	err := grants.Give(ctx, Grant{Participant: Participant{URL: fake.server.URL}, UserID: "u", ActAs: []string{"Alice::fp"}})

	if err == nil || fake.activeRights() != 0 || len(fake.rights) != 0 {
		t.Errorf("err=%v rights=%v, want no grant after cancellation", err, fake.rights)
	}
}

func TestRequestRequiresASuccessStatus(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusSeeOther, http.StatusMultipleChoices} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var targetHits int
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetHits++
				fmt.Fprint(w, `{}`)
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer server.Close()

			_, err := (Participant{URL: server.URL}).request(context.Background(), "GET", "/v2/parties/x", "", nil)

			var httpErr *ledger.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status {
				t.Errorf("err = %v, want an HTTP error with status %d", err, status)
			}
			if targetHits != 0 {
				t.Errorf("the redirect was followed %d time(s)", targetHits)
			}
		})
	}
}

func TestRequestReportsAnUnreadableResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, `{"partial`)
	}))
	defer server.Close()

	_, err := (Participant{URL: server.URL}).request(context.Background(), "GET", "/v2/parties/x", "", nil)

	if err == nil {
		t.Error("a truncated response body must be an error")
	}
}

func TestRevokeWarnsWhenTheStatusIsNotSuccess(t *testing.T) {
	fake := newFakeParticipant(t)
	participants := []Participant{{URL: fake.server.URL, TokenFile: writeToken(t, "ta")}}
	_, grants, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice"), "s", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fake.revokeStatus = http.StatusFound
	fake.revokeTarget = fake.server.URL + "/elsewhere"
	var warnings strings.Builder

	grants.Revoke(&warnings)

	if !strings.Contains(warnings.String(), "warning: could not revoke") {
		t.Errorf("a redirected revoke must warn, got %q", warnings.String())
	}
}

func TestWaitForPartyStopsAtTheDeadlineEvenWhenTheServerIsSlow(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.lookupDelay = 5 * time.Second

	started := time.Now()
	err := (Participant{URL: fake.server.URL}).WaitForParty(context.Background(), "ghost::fp", time.Second)

	if err == nil || !strings.Contains(err.Error(), "not visible") {
		t.Errorf("err = %v, want a visibility timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Errorf("took %s with a 1s timeout", elapsed)
	}
}

func TestAttachSpendsOneTimeoutPerPartyAcrossAllParticipants(t *testing.T) {
	first, second, participants := newAttachFakes(t)
	first.lookupDelay, second.lookupDelay = 700*time.Millisecond, 700*time.Millisecond

	started := time.Now()
	_, _, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice@1"), "s", time.Second)

	if err == nil || !strings.Contains(err.Error(), "not visible") {
		t.Errorf("err = %v, want the shared 1s deadline to expire on the second participant", err)
	}
	if elapsed := time.Since(started); elapsed > 1800*time.Millisecond {
		t.Errorf("took %s with a 1s timeout", elapsed)
	}
}

func TestValidateAttachRejectsUnsafeLedgerURLs(t *testing.T) {
	cases := map[string]string{
		"credentials":      "http://alice:hunter2@localhost:7575",
		"user only":        "http://alice@localhost:7575",
		"empty hostname":   "http://:7575",
		"credentials only": "http://alice:hunter2@",
		"no scheme":        "alice:hunter2@host:7575",
		"scheme relative":  "//alice:hunter2@host",
		"slash in secret":  "http://alice:hun/ter2@host:1",
	}
	for name, ledgerURL := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateAttach(Options{LedgerURLs: []string{ledgerURL}}, 1)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			for _, secret := range []string{"hunter2", "hun/ter2", "ter2", "alice"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q echoes %q", err, secret)
				}
			}
		})
	}
}

func TestValidateAttachRejectsACantonJarFlagEvenWhenEmpty(t *testing.T) {
	opts := Options{LedgerURLs: []string{"http://localhost:7575"}, CantonJarGiven: true}
	if err := ValidateAttach(opts, 1); err == nil || !strings.Contains(err.Error(), "--canton-jar") {
		t.Errorf("err = %v, want --canton-jar rejected", err)
	}
}

func TestRunIgnoresAnAmbientCantonJarInAttachMode(t *testing.T) {
	fake := newFakeParticipant(t)
	t.Setenv("DPM_TRACE_CANTON_JAR", "/nonexistent/canton.jar")
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\n"), 0o755)

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})

	if err != nil || code != 0 {
		t.Errorf("code=%d err=%v, want an ambient DPM_TRACE_CANTON_JAR to be harmless", code, err)
	}
}

func writeSuiteConfig(t *testing.T, suite, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(suite, "lit.cfg.py"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsASuiteWhoseLitConfigDoesNotForwardTheRunnerConfig(t *testing.T) {
	cases := map[string]string{
		"old scaffold":   "import os\nconfig.name = 'x'\n",
		"only mentioned": "import os\n# DPM_TRACE_CONFIG is not forwarded here\nconfig.name = 'x'\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeParticipant(t)
			dir := t.TempDir()
			dar := filepath.Join(dir, "x.dar")
			os.WriteFile(dar, []byte("dar"), 0o600)
			suite := filepath.Join(dir, "suite")
			os.Mkdir(suite, 0o755)
			writeSuiteConfig(t, suite, content)
			fakeLit := filepath.Join(dir, "lit")
			os.WriteFile(fakeLit, []byte("#!/bin/sh\n"), 0o755)

			code, err := Run(io.Discard, io.Discard, Options{
				TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
				LedgerURLs: []string{fake.server.URL},
			})

			if code != 2 || err == nil || !strings.Contains(err.Error(), "DPM_TRACE_CONFIG") || !strings.Contains(err.Error(), "lit.cfg.py") {
				t.Errorf("code=%d err=%v, want exit 2 naming DPM_TRACE_CONFIG and the lit config", code, err)
			}
			if fake.uploads != 0 {
				t.Errorf("the DAR was uploaded %d time(s) before the suite was rejected", fake.uploads)
			}
		})
	}
}

func TestRunAcceptsASuiteThatForwardsTheRunnerConfig(t *testing.T) {
	fake := newFakeParticipant(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, "import os\nif os.environ.get(\"DPM_TRACE_CONFIG\"):\n    config.environment[\"DPM_TRACE_CONFIG\"] = os.environ[\"DPM_TRACE_CONFIG\"]\n")
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\n"), 0o755)

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})

	if code != 0 || err != nil {
		t.Errorf("code=%d err=%v", code, err)
	}
}

func TestBootModeDoesNotRequireTheForwardingLitConfig(t *testing.T) {
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, "config.name = 'x'\n")

	if err := CheckSuiteForwardsRunnerConfig(suite); err == nil {
		t.Error("the check itself must reject the old config; Run applies it only in attach mode")
	}
	code, _ := Run(io.Discard, io.Discard, Options{TestDir: suite, Root: dir, DAR: filepath.Join(dir, "x.dar"), CantonJar: filepath.Join(dir, "missing.jar")})
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestAttachModeDropsAmbientDPMTraceVariables(t *testing.T) {
	fake := newFakeParticipant(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	envDump := filepath.Join(dir, "env.txt")
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\nenv > \"$ENV_DUMP\"\n"), 0o755)
	t.Setenv("ENV_DUMP", envDump)
	for name, value := range map[string]string{
		"DPM_TRACE_IT_LEDGER2":     "http://stale-second:1",
		"DPM_TRACE_IT_ISSUER":      "stale-issuer",
		"DPM_TRACE_IT_TOKEN_FILE3": "/ambient/three",
		"DPM_TRACE_TOKEN":          "ambient-token",
		"DPM_TRACE_TOKEN_FILE":     "/ambient/token",
		"DPM_TRACE_LEDGER_URL":     "http://ambient-ledger:1",
		"DPM_TRACE_SCAN_URL":       "http://ambient-scan:1",
		"DPM_TRACE_CANTON_JAR":     "/ambient/canton.jar",
	} {
		t.Setenv(name, value)
	}

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	dump, _ := os.ReadFile(envDump)
	for _, forbidden := range []string{"DPM_TRACE_IT_LEDGER2", "DPM_TRACE_IT_ISSUER", "DPM_TRACE_IT_TOKEN", "DPM_TRACE_TOKEN", "DPM_TRACE_LEDGER_URL", "DPM_TRACE_SCAN_URL"} {
		if strings.Contains(string(dump), forbidden) {
			t.Errorf("the suite inherited %s", forbidden)
		}
	}
	for _, kept := range []string{"DPM_TRACE_IT_LEDGER=" + fake.server.URL, "DPM_TRACE_IT_ALICE=Alice-"} {
		if !strings.Contains(string(dump), kept) {
			t.Errorf("the suite lacks %q", kept)
		}
	}
}

func TestEveryTokenFileIsExported(t *testing.T) {
	first, second, third := newFakeParticipant(t), newFakeParticipant(t), newFakeParticipant(t)
	second.network, third.network = first.network, first.network
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	envDump := filepath.Join(dir, "env.txt")
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\nenv > \"$ENV_DUMP\"\n"), 0o755)
	t.Setenv("ENV_DUMP", envDump)
	tokens := []string{writeToken(t, "a"), writeToken(t, "b"), writeToken(t, "c")}

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2,Carol@3", CantonTimeout: 5,
		LedgerURLs: []string{first.server.URL, second.server.URL, third.server.URL}, TokenFiles: tokens,
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	dump, _ := os.ReadFile(envDump)
	for _, want := range []string{"DPM_TRACE_IT_TOKEN_FILE=" + tokens[0], "DPM_TRACE_IT_TOKEN_FILE2=" + tokens[1], "DPM_TRACE_IT_TOKEN_FILE3=" + tokens[2], "DPM_TRACE_IT_LEDGER3=" + third.server.URL} {
		if !strings.Contains(string(dump), want+"\n") {
			t.Errorf("suite environment lacks %q", want)
		}
	}
}

func TestRedactUserinfoKeepsTheHostAndHidesEverythingBeforeTheLastAt(t *testing.T) {
	cases := map[string]string{
		"http://alice:hunter2@localhost:7575": "http://REDACTED@localhost:7575",
		"alice:hunter2@host:7575":             "REDACTED@host:7575",
		"//alice:hunter2@host":                "//REDACTED@host",
		"http://alice:hun/ter2@host:1":        "http://REDACTED@host:1",
		"http://localhost:7575":               "http://localhost:7575",
	}
	for raw, want := range cases {
		if got := redactUserinfo(raw); got != want {
			t.Errorf("redactUserinfo(%q) = %q, want %q", raw, got, want)
		}
	}
}

const oldLitConfig = "config.name = 'x'\n"
const forwardingLitConfig = "import os\nif os.environ.get(\"DPM_TRACE_CONFIG\"):\n    config.environment[\"DPM_TRACE_CONFIG\"] = os.environ[\"DPM_TRACE_CONFIG\"]\n"

func nestedSuite(t *testing.T, parentConfig string) (parent, nested string) {
	t.Helper()
	parent = filepath.Join(t.TempDir(), "itests")
	nested = filepath.Join(parent, "demos")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSuiteConfig(t, parent, parentConfig)
	return parent, nested
}

func TestCheckSuiteJudgesTheLitConfigThatLitWouldPickUpFromAnAncestor(t *testing.T) {
	parent, nested := nestedSuite(t, oldLitConfig)

	err := CheckSuiteForwardsRunnerConfig(nested)

	if err == nil || !strings.Contains(err.Error(), filepath.Join(parent, "lit.cfg.py")) {
		t.Errorf("err = %v, want the ancestor's lit.cfg.py refused by name", err)
	}
}

func TestCheckSuiteAcceptsAnAncestorLitConfigThatForwards(t *testing.T) {
	_, nested := nestedSuite(t, forwardingLitConfig)

	if err := CheckSuiteForwardsRunnerConfig(nested); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestCheckSuiteStopsAtTheNearestLitConfig(t *testing.T) {
	_, nested := nestedSuite(t, oldLitConfig)
	writeSuiteConfig(t, nested, forwardingLitConfig)

	if err := CheckSuiteForwardsRunnerConfig(nested); err != nil {
		t.Errorf("err = %v, the nearest config forwards", err)
	}
}

func TestCheckSuiteResolvesARelativeTestDirBeforeWalkingUp(t *testing.T) {
	parent, nested := nestedSuite(t, oldLitConfig)
	t.Chdir(parent)

	err := CheckSuiteForwardsRunnerConfig(filepath.Base(nested))

	if err == nil {
		t.Error("a relative test dir under an old parent config must be refused")
	}
}

func TestRunRefusesANestedSuiteUnderAnOldParentConfigBeforeUploading(t *testing.T) {
	fake := newFakeParticipant(t)
	parent, nested := nestedSuite(t, oldLitConfig)
	dar := filepath.Join(parent, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	fakeLit := filepath.Join(parent, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\n"), 0o755)

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: nested, Root: parent, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})

	if code != 2 || err == nil || !strings.Contains(err.Error(), "DPM_TRACE_CONFIG") {
		t.Errorf("code=%d err=%v, want exit 2 naming DPM_TRACE_CONFIG", code, err)
	}
	if fake.uploads != 0 {
		t.Errorf("the DAR was uploaded %d time(s) before the suite was rejected", fake.uploads)
	}
}

func writeNamedConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSuiteJudgesTheSiteConfigThatLitPicksOverLitCfg(t *testing.T) {
	suite := t.TempDir()
	writeNamedConfig(t, suite, "lit.site.cfg.py", oldLitConfig)
	writeNamedConfig(t, suite, "lit.cfg.py", forwardingLitConfig)

	err := CheckSuiteForwardsRunnerConfig(suite)

	if err == nil || !strings.Contains(err.Error(), "lit.site.cfg.py") || strings.Contains(err.Error(), filepath.Join(suite, "lit.cfg.py")) {
		t.Errorf("err = %v, want only lit.site.cfg.py refused", err)
	}
}

func TestCheckSuiteAcceptsASiteConfigThatForwardsEvenIfLitCfgDoesNot(t *testing.T) {
	suite := t.TempDir()
	writeNamedConfig(t, suite, "lit.site.cfg.py", forwardingLitConfig)
	writeNamedConfig(t, suite, "lit.cfg.py", oldLitConfig)

	if err := CheckSuiteForwardsRunnerConfig(suite); err != nil {
		t.Errorf("err = %v, lit would execute only the forwarding site config", err)
	}
}

func TestCheckSuitePrefersTheCfgPyOverTheExtensionlessCfg(t *testing.T) {
	suite := t.TempDir()
	writeNamedConfig(t, suite, "lit.cfg", forwardingLitConfig)
	writeNamedConfig(t, suite, "lit.cfg.py", oldLitConfig)

	if err := CheckSuiteForwardsRunnerConfig(suite); err == nil {
		t.Error("lit loads lit.cfg.py before lit.cfg, so the forwarding lit.cfg must not rescue it")
	}
}

func TestCheckSuiteRefusesATreeWithoutAnyLitConfig(t *testing.T) {
	suite := t.TempDir()

	err := CheckSuiteForwardsRunnerConfig(suite)

	if err == nil || !strings.Contains(err.Error(), "lit configuration") || !strings.Contains(err.Error(), "isolat") {
		t.Errorf("err = %v, want a refusal explaining that attach mode cannot be isolated without a lit config", err)
	}
}

func TestCheckSuiteNamesTheCheckAsABestEffortGuard(t *testing.T) {
	suite := t.TempDir()
	writeNamedConfig(t, suite, "lit.cfg.py", oldLitConfig)

	err := CheckSuiteForwardsRunnerConfig(suite)

	if err == nil || !strings.Contains(err.Error(), "best-effort") {
		t.Errorf("err = %v, want the guard named best-effort", err)
	}
}

func TestGrantWithoutAConfirmedAnswerIsStillRevokedAndWarnedAbout(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.grantGate = make(chan struct{})
	t.Cleanup(func() { close(fake.grantGate) })
	participant := Participant{URL: fake.server.URL, Timeout: 100 * time.Millisecond}
	grants := &Grants{}

	err := grants.Give(context.Background(), Grant{Participant: participant, UserID: "ambiguous-user", ActAs: []string{"Alice::fp"}, ReadAs: []string{"Bob::fp"}})
	var warnings strings.Builder
	grants.Revoke(&warnings)

	if err == nil {
		t.Fatal("expected the timed out grant to fail")
	}
	if fake.revokeRequests != 1 {
		t.Errorf("revoke requests = %d, want the ambiguous grant revoked", fake.revokeRequests)
	}
	for _, want := range []string{"ambiguous-user", "Alice::fp", "Bob::fp", fake.server.URL, "may still be"} {
		if !strings.Contains(warnings.String(), want) {
			t.Errorf("warning %q lacks %q", warnings.String(), want)
		}
	}
}

func TestGrantRejectedWithAClientErrorIsNotReportedAsPossiblyGranted(t *testing.T) {
	fake := newFakeParticipant(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		fake.handle(w, r)
	}))
	defer server.Close()
	grants := &Grants{}

	err := grants.Give(context.Background(), Grant{Participant: Participant{URL: server.URL}, UserID: "u", ActAs: []string{"Alice::fp"}})
	var warnings strings.Builder
	grants.Revoke(&warnings)

	if err == nil {
		t.Fatal("expected the forbidden grant to fail")
	}
	if strings.Contains(warnings.String(), "may still be") {
		t.Errorf("a definite refusal was reported as ambiguous: %s", warnings.String())
	}
}

func TestValidateAttachRejectsALedgerURLWithAQueryOrFragment(t *testing.T) {
	for _, raw := range []string{"http://host:1?token=hunter2", "http://host:1/#secretfrag", "http://host:1/path?x=1#y", "http://host:1?"} {
		err := ValidateAttach(Options{LedgerURLs: []string{raw}}, 1)

		if err == nil {
			t.Errorf("%q was accepted", raw)
			continue
		}
		for _, secret := range []string{"hunter2", "secretfrag", "x=1"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error for %q echoes %q: %v", raw, secret, err)
			}
		}
	}
}

func TestValidateAttachRedactsEverythingBeforeTheLastAtInAQueryURL(t *testing.T) {
	err := ValidateAttach(Options{LedgerURLs: []string{"http://alice:hunter2@host:1?x=1"}}, 1)

	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckSuiteScopesItsErrorToTheSuitesOwnConfigAndNamesNestedSuites(t *testing.T) {
	suite := t.TempDir()
	writeNamedConfig(t, suite, "lit.cfg.py", oldLitConfig)

	err := CheckSuiteForwardsRunnerConfig(suite)

	if err == nil || !strings.Contains(err.Error(), "the suite's own lit configuration") || !strings.Contains(err.Error(), "nested suite") {
		t.Errorf("err = %v, want the check scoped to the suite's own configuration and a reminder about nested suites", err)
	}
}
