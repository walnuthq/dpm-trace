package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sharedParties struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (p *sharedParties) add(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids[id] = true
}

func (p *sharedParties) has(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ids[id]
}

type fakeParticipant struct {
	network *sharedParties

	mu      sync.Mutex
	server  *httptest.Server
	tokens  []string
	uploads int
	hints   []string
	rights  map[string]string
	revoked map[string]string
	parties []string
	listed  int
	lookups int

	active map[string]bool

	userID             string
	failGrant          bool
	rejectAnonymous    bool
	dropGrantResponses bool
	revokeDelay        time.Duration
	revokeRequests     int
	userRequests       int

	grantGate     chan struct{}
	grantArrived  chan struct{}
	revokeArrived chan struct{}
	uploadGate    chan struct{}
	uploadArrived chan struct{}
	revokeStatus  int
	revokeTarget  string
	lookupDelay   time.Duration
}

func notify(channel chan struct{}) {
	if channel == nil {
		return
	}
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (f *fakeParticipant) applyRights(path, body string, granted bool) {
	var request struct {
		Rights []any `json:"rights"`
	}
	_ = json.Unmarshal([]byte(body), &request)
	for _, right := range request.Rights {
		encoded, _ := json.Marshal(right)
		key := path + "|" + string(encoded)
		if granted {
			f.active[key] = true
		} else {
			delete(f.active, key)
		}
	}
}

func (f *fakeParticipant) activeRights() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.active)
}

func (f *fakeParticipant) waitFor(gate chan struct{}) {
	f.mu.Unlock()
	<-gate
	f.mu.Lock()
}

func newFakeParticipant(t *testing.T) *fakeParticipant {
	t.Helper()
	fake := &fakeParticipant{network: &sharedParties{ids: map[string]bool{}}, rights: map[string]string{}, revoked: map[string]string{}, active: map[string]bool{}, userID: "user"}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeParticipant) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == "POST" && r.URL.Path == "/v2/packages":
		f.uploads++
		notify(f.uploadArrived)
		if f.uploadGate != nil {
			f.waitFor(f.uploadGate)
		}
		fmt.Fprint(w, `{}`)
	case r.Method == "POST" && r.URL.Path == "/v2/parties":
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		hint := req["partyIdHint"].(string)
		f.hints = append(f.hints, hint)
		id := hint + "::fp"
		f.parties = append(f.parties, id)
		f.network.add(id)
		fmt.Fprintf(w, `{"partyDetails":{"party":%q}}`, id)
	case r.Method == "GET" && r.URL.Path == "/v2/authenticated-user":
		f.userRequests++
		if f.rejectAnonymous && r.Header.Get("Authorization") == "" {
			http.Error(w, `{"code":"INVALID_TOKEN","cause":"requests with an empty user-id are only supported if there is an authenticated user"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"user":{"id":%q,"identityProviderId":"","primaryParty":""}}`, f.userID)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v2/users/"):
		if f.failGrant {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		f.rights[r.URL.Path] = string(body)
		notify(f.grantArrived)
		if f.grantGate != nil {
			f.waitFor(f.grantGate)
		}
		f.applyRights(r.URL.Path, string(body), true)
		if f.dropGrantResponses {
			hijacked, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				hijacked.Close()
			}
			return
		}
		fmt.Fprint(w, `{}`)
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/v2/users/"):
		f.revokeRequests++
		notify(f.revokeArrived)
		if f.revokeDelay > 0 {
			f.mu.Unlock()
			time.Sleep(f.revokeDelay)
			f.mu.Lock()
		}
		f.revoked[r.URL.Path] = string(body)
		f.applyRights(r.URL.Path, string(body), false)
		if f.revokeStatus != 0 {
			if f.revokeTarget != "" {
				w.Header().Set("Location", f.revokeTarget)
			}
			w.WriteHeader(f.revokeStatus)
			return
		}
		fmt.Fprint(w, `{}`)
	case r.Method == "GET" && r.URL.Path == "/v2/parties":
		f.listed++
		http.Error(w, "listing every party does not scale; look parties up one by one", http.StatusTeapot)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v2/parties/"):
		f.lookups++
		if f.lookupDelay > 0 {
			f.mu.Unlock()
			time.Sleep(f.lookupDelay)
			f.mu.Lock()
		}
		wanted := strings.TrimPrefix(r.URL.Path, "/v2/parties/")
		if f.network.has(wanted) {
			fmt.Fprintf(w, `{"partyDetails":[{"party":%q}]}`, wanted)
			return
		}
		fmt.Fprint(w, `{"partyDetails":[]}`)
	default:
		http.NotFound(w, r)
	}
}

func writeToken(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newAttachFakes(t *testing.T) (*fakeParticipant, *fakeParticipant, []Participant) {
	t.Helper()
	first, second := newFakeParticipant(t), newFakeParticipant(t)
	second.network = first.network
	first.userID, second.userID = "user-a", "user-b"
	participants := []Participant{
		{URL: first.server.URL, TokenFile: writeToken(t, "ta")},
		{URL: second.server.URL, TokenFile: writeToken(t, "tb")},
	}
	return first, second, participants
}

func TestAttachSetsUpEachParticipant(t *testing.T) {
	first, second, participants := newAttachFakes(t)

	ids, _, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice@1,Bob@2,Carol@2"), "abc123", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if ids["Alice"] != "Alice-abc123::fp" || ids["Bob"] != "Bob-abc123::fp" || len(ids) != 3 {
		t.Errorf("ids = %v", ids)
	}
	if first.uploads != 1 || second.uploads != 1 {
		t.Errorf("uploads = %d/%d, want 1/1", first.uploads, second.uploads)
	}
	if len(first.hints) != 1 || len(second.hints) != 2 {
		t.Errorf("hints = %v / %v", first.hints, second.hints)
	}
	firstBody := first.rights["/v2/users/user-a/rights"]
	if strings.Count(firstBody, "CanActAs") != 1 || !strings.Contains(firstBody, "Alice-abc123::fp") {
		t.Errorf("participant 1 act rights = %s", firstBody)
	}
	if strings.Count(firstBody, "CanReadAs") != 2 || !strings.Contains(firstBody, "Bob-abc123::fp") {
		t.Errorf("participant 1 must read as Bob and Carol: %s", firstBody)
	}
	secondBody := second.rights["/v2/users/user-b/rights"]
	if strings.Count(secondBody, "CanActAs") != 2 || strings.Count(secondBody, "CanReadAs") != 1 {
		t.Errorf("participant 2 rights = %s", secondBody)
	}
}

func TestRevokeWithdrawsEveryGrant(t *testing.T) {
	first, second, participants := newAttachFakes(t)
	_, grants, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice@1,Bob@2"), "s", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	grants.Revoke(io.Discard)

	if first.revoked["/v2/users/user-a/rights"] != first.rights["/v2/users/user-a/rights"] {
		t.Errorf("participant 1 revoked %q, granted %q", first.revoked, first.rights)
	}
	if second.revoked["/v2/users/user-b/rights"] != second.rights["/v2/users/user-b/rights"] {
		t.Errorf("participant 2 revoked %q, granted %q", second.revoked, second.rights)
	}
}

func TestRevokeAfterPartialFailureCoversEveryAttemptedGrant(t *testing.T) {
	first, second, participants := newAttachFakes(t)
	second.failGrant = true

	_, grants, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice@1,Bob@2"), "s", 5*time.Second)
	if err == nil {
		t.Fatal("expected the grant on participant 2 to fail")
	}
	grants.Revoke(io.Discard)

	if _, ok := first.revoked["/v2/users/user-a/rights"]; !ok {
		t.Errorf("participant 1's grant was not revoked: %v", first.revoked)
	}
	if _, ok := second.revoked["/v2/users/user-b/rights"]; !ok {
		t.Errorf("the failed grant on participant 2 may have been applied; it must be revoked too: %v", second.revoked)
	}
}

func TestRevokeIsSafeWithoutGrants(t *testing.T) {
	var grants *Grants
	grants.Revoke(io.Discard)
}

func TestAuthenticatedUser(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.userID = "svc-account"
	fake.rejectAnonymous = true
	participant := Participant{URL: fake.server.URL, TokenFile: writeToken(t, "ta")}
	if got, err := participant.AuthenticatedUser(context.Background()); err != nil || got != "svc-account" {
		t.Errorf("got %q, %v", got, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer empty.Close()
	if _, err := (Participant{URL: empty.URL}).AuthenticatedUser(context.Background()); err == nil {
		t.Error("a response without a user must fail")
	}
}

func TestUploadDARExplainsPackageVersionCollision(t *testing.T) {
	for _, code := range []string{"KNOWN_PACKAGE_VERSION", "KNOWN_DAR_VERSION"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"code":"`+code+`","cause":"same name and version"}`, http.StatusBadRequest)
			}))
			defer server.Close()
			err := (Participant{URL: server.URL}).UploadDAR(context.Background(), []byte("dar"))
			if err == nil || !strings.Contains(err.Error(), "bump `version` in daml.yaml") {
				t.Fatalf("err = %v, want advice to bump the version", err)
			}
			if strings.Contains(err.Error(), code) || strings.Contains(err.Error(), "same name and version\"") {
				t.Errorf("err = %v, want Canton's raw error hidden", err)
			}
		})
	}
}

func TestAttachParticipantsKeepsTokenSymlinks(t *testing.T) {
	fake := newFakeParticipant(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	link := filepath.Join(dir, "current")
	os.WriteFile(target, []byte("t\n"), 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)

	_, files, grants, err := attachParticipants(context.Background(), io.Discard, Options{LedgerURLs: []string{fake.server.URL}, TokenFiles: []string{link}, CantonTimeout: 5}, dar, ParsePlacements("Alice"))
	if err != nil {
		t.Fatal(err)
	}
	grants.Revoke(io.Discard)

	if files[0] != link {
		t.Errorf("token path = %q, want the symlink %q itself", files[0], link)
	}
}

func TestRunExportsTokenFilesAndRevokesAtTeardown(t *testing.T) {
	first, second, _ := newAttachFakes(t)
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
	tokenA, tokenB := writeToken(t, "ta"), writeToken(t, "tb")

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2", CantonTimeout: 5,
		LedgerURLs: []string{first.server.URL, second.server.URL}, TokenFiles: []string{tokenA, tokenB},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	dump, _ := os.ReadFile(envDump)
	for _, want := range []string{"DPM_TRACE_IT_TOKEN_FILE=" + tokenA, "DPM_TRACE_IT_TOKEN_FILE2=" + tokenB, "DPM_TRACE_IT_LEDGER2=" + second.server.URL} {
		if !strings.Contains(string(dump), want+"\n") {
			t.Errorf("suite environment lacks %q", want)
		}
	}
	if len(first.revoked) != 1 || len(second.revoked) != 1 {
		t.Errorf("rights not revoked after the run: %v / %v", first.revoked, second.revoked)
	}
}

func TestTokenFileIsReReadPerRequest(t *testing.T) {
	fake := newFakeParticipant(t)
	path := filepath.Join(t.TempDir(), "token")
	participant := Participant{URL: fake.server.URL, TokenFile: path}

	for _, token := range []string{"first", "second"} {
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := participant.UploadDAR(context.Background(), []byte("dar")); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.tokens) != 2 || fake.tokens[0] != "first" || fake.tokens[1] != "second" {
		t.Errorf("tokens sent = %v", fake.tokens)
	}
}

func TestWaitForPartyTimesOut(t *testing.T) {
	fake := newFakeParticipant(t)
	participant := Participant{URL: fake.server.URL}
	if err := participant.WaitForParty(context.Background(), "ghost::fp", time.Millisecond); err == nil {
		t.Fatal("expected a timeout")
	}
}

func TestWaitForPartyPollsUntilListed(t *testing.T) {
	fake := newFakeParticipant(t)
	participant := Participant{URL: fake.server.URL}
	go func() {
		time.Sleep(700 * time.Millisecond)
		fake.mu.Lock()
		fake.parties = append(fake.parties, "late::fp")
		fake.network.add("late::fp")
		fake.mu.Unlock()
	}()
	if err := participant.WaitForParty(context.Background(), "late::fp", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if fake.listed != 0 {
		t.Errorf("listed all parties %d times; each poll must look up the one party", fake.listed)
	}
}

func TestUploadDARDoesNotRetryClientErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	if err := (Participant{URL: server.URL}).UploadDAR(context.Background(), []byte("dar")); err == nil || calls != 1 {
		t.Errorf("err=%v calls=%d, want one failed call", err, calls)
	}
}

func TestValidateAttach(t *testing.T) {
	existingToken := writeToken(t, "t")
	emptyToken := writeToken(t, "")
	missingToken := filepath.Join(t.TempDir(), "absent")
	cases := []struct {
		name         string
		opts         Options
		participants int
		wantError    string
	}{
		{"boot mode", Options{CantonJar: "x.jar"}, 1, ""},
		{"token file without ledger url", Options{TokenFiles: []string{"t"}}, 1, "without --ledger-url"},
		{"ledger url with canton jar", Options{LedgerURLs: []string{"http://u"}, CantonJar: "x.jar"}, 1, "--canton-jar"},
		{"too few ledger urls", Options{LedgerURLs: []string{"http://u"}}, 2, "one --ledger-url per participant"},
		{"too many ledger urls", Options{LedgerURLs: []string{"http://u", "http://v", "http://w"}}, 2, "one --ledger-url per participant"},
		{"fewer token files than urls", Options{LedgerURLs: []string{"http://u", "http://v"}, TokenFiles: []string{"t"}}, 2, "exactly one --token-file per --ledger-url"},
		{"more token files than urls", Options{LedgerURLs: []string{"http://u"}, TokenFiles: []string{"t", "s"}}, 1, "exactly one --token-file per --ledger-url"},
		{"aligned urls without tokens", Options{LedgerURLs: []string{"http://u", "http://v"}}, 2, ""},
		{"aligned urls and tokens", Options{LedgerURLs: []string{"http://u", "http://v"}, TokenFiles: []string{existingToken, existingToken}}, 2, ""},
		{"empty ledger url", Options{LedgerURLs: []string{" "}}, 1, "http or https URL"},
		{"ledger url without scheme", Options{LedgerURLs: []string{"localhost:7575"}}, 1, "http or https URL"},
		{"ledger url with other scheme", Options{LedgerURLs: []string{"ftp://host"}}, 1, "http or https URL"},
		{"ledger url without host", Options{LedgerURLs: []string{"http://"}}, 1, "http or https URL"},
		{"unreadable token file", Options{LedgerURLs: []string{"http://u"}, TokenFiles: []string{missingToken}}, 1, "--token-file " + missingToken},
		{"empty token file", Options{LedgerURLs: []string{"http://u"}, TokenFiles: []string{emptyToken}}, 1, "is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAttach(tc.opts, tc.participants)
			switch {
			case tc.wantError == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)):
				t.Errorf("err = %v, want it to contain %q", err, tc.wantError)
			}
		})
	}
}

func TestRunRequiresOneLedgerURLPerHighestParticipant(t *testing.T) {
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, Parties: "Alice@1,Bob@3",
		LedgerURLs: []string{"http://127.0.0.1:1", "http://127.0.0.1:2"},
	})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "3 participant(s)") {
		t.Errorf("code=%d err=%v, want exit 2 naming 3 participants", code, err)
	}
}

func TestRunRejectsTokenFileWithoutLedgerURL(t *testing.T) {
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)

	code, err := Run(io.Discard, io.Discard, Options{TestDir: suite, Root: dir, TokenFiles: []string{"token"}})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "without --ledger-url") {
		t.Errorf("code=%d err=%v, want exit 2 about --token-file", code, err)
	}
}

func TestAttachWithoutTokenFilesSendsNoCredentials(t *testing.T) {
	fake := newFakeParticipant(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)

	_, files, grants, err := attachParticipants(context.Background(), io.Discard, Options{LedgerURLs: []string{fake.server.URL}, CantonTimeout: 5}, dar, ParsePlacements("Alice"))
	if err != nil {
		t.Fatal(err)
	}
	grants.Revoke(io.Discard)

	if len(files) != 0 {
		t.Errorf("token files = %v, want none", files)
	}
	for _, token := range fake.tokens {
		if token != "" {
			t.Errorf("request carried token %q, want none", token)
		}
	}
}

func TestRunWithoutTokenFilesExportsNoTokenVariables(t *testing.T) {
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
	t.Setenv("DPM_TRACE_IT_TOKEN_FILE", "/ambient/one")
	t.Setenv("DPM_TRACE_IT_TOKEN_FILE2", "/ambient/two")

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	dump, _ := os.ReadFile(envDump)
	for _, forbidden := range []string{"DPM_TRACE_IT_TOKEN", "DPM_TRACE_TOKEN"} {
		if strings.Contains(string(dump), forbidden) {
			t.Errorf("suite environment mentions %s", forbidden)
		}
	}
}

func TestRunHandsTheSuiteTheProjectConfigWithoutConnectionKeys(t *testing.T) {
	fake := newFakeParticipant(t)
	project := t.TempDir()
	os.MkdirAll(filepath.Join(project, ".git"), 0o755)
	os.WriteFile(filepath.Join(project, ".git", "HEAD"), []byte("ref\n"), 0o644)
	os.WriteFile(filepath.Join(project, ".dpm-trace.json"), []byte(`{"token":"bogus","tokenFile":"/leak","token_file":"/leak","ledgerUrl":"http://leak","ledger_url":"http://leak","scanUrl":"http://leak","scan_url":"http://leak","damlYaml":["daml.yaml"],"sourceRoots":["daml"]}`), 0o644)
	dar := filepath.Join(project, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(project, "itests")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	dump := filepath.Join(project, "config.txt")
	fakeLit := filepath.Join(project, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\ncat \"$DPM_TRACE_CONFIG\" > \"$CONFIG_DUMP\"\n"), 0o755)
	t.Setenv("CONFIG_DUMP", dump)
	t.Setenv("DPM_TRACE_CONFIG", "")

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: project, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	content, _ := os.ReadFile(dump)
	for _, forbidden := range []string{"token", "leak", "bogus", "ledgerUrl", "scanUrl"} {
		if strings.Contains(string(content), forbidden) {
			t.Errorf("suite config still carries %q: %s", forbidden, content)
		}
	}
	for _, kept := range []string{"damlYaml", "sourceRoots"} {
		if !strings.Contains(string(content), kept) {
			t.Errorf("suite config lost %q: %s", kept, content)
		}
	}
}

func TestAttachOutputDoesNotClaimABootedCanton(t *testing.T) {
	fake := newFakeParticipant(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\n"), 0o755)
	var out strings.Builder

	code, err := Run(&out, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5, KeepArtifacts: true,
		LedgerURLs: []string{fake.server.URL},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, forbidden := range []string{"Canton ready", "kept Canton log"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("attach output contains %q:\n%s", forbidden, out.String())
		}
	}
}

func TestAttachWithoutTokenFileSkipsUserLookupAndGrants(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.rejectAnonymous = true

	_, grants, err := Attach(context.Background(), io.Discard, []Participant{{URL: fake.server.URL}}, []byte("dar"), ParsePlacements("Alice"), "s", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	grants.Revoke(io.Discard)

	if fake.userRequests != 0 || len(fake.rights) != 0 || fake.revokeRequests != 0 {
		t.Errorf("user lookups=%d grants=%v revokes=%d, want none on an unauthenticated participant", fake.userRequests, fake.rights, fake.revokeRequests)
	}
}

func TestGrantWhoseResponseIsLostIsStillRevoked(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.dropGrantResponses = true
	participants := []Participant{{URL: fake.server.URL, TokenFile: writeToken(t, "ta")}}

	_, grants, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice"), "s", 5*time.Second)
	if err == nil {
		t.Fatal("expected the grant to fail when its response is lost")
	}
	grants.Revoke(io.Discard)

	if fake.revokeRequests != 1 {
		t.Errorf("revoke requests = %d, want 1", fake.revokeRequests)
	}
}

func TestRevokeFailureNamesTheUserAndPartiesToCleanUp(t *testing.T) {
	fake := newFakeParticipant(t)
	participants := []Participant{{URL: fake.server.URL, TokenFile: writeToken(t, "ta")}}
	_, grants, err := Attach(context.Background(), io.Discard, participants, []byte("dar"), ParsePlacements("Alice"), "s", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fake.server.Close()
	var warnings strings.Builder

	grants.Revoke(&warnings)

	for _, want := range []string{"user", "Alice-s::fp", fake.server.URL} {
		if !strings.Contains(warnings.String(), want) {
			t.Errorf("warning %q lacks %q", warnings.String(), want)
		}
	}
}

func TestUploadStopsRetryingWhenCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	err := (Participant{URL: server.URL}).UploadDAR(ctx, []byte("dar"))

	if err == nil || time.Since(started) > 900*time.Millisecond {
		t.Errorf("err=%v after %s, want a prompt cancellation", err, time.Since(started))
	}
}
