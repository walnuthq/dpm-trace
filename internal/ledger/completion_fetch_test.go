package ledger

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A failed submission has no update id, so the completion stream is the only
// record of it. This is the lookup behind `dpm trace --command-id`.

func completionServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFetchCompletionFindsTheCommand(t *testing.T) {
	server := completionServer(t, `[
	  {"completionResponse":{"Completion":{"value":{"commandId":"other","status":{"code":0}}}}},
	  {"completionResponse":{"Completion":{"value":{"commandId":"wanted",
	     "status":{"code":9,"message":"Insufficient balance"},"offset":12}}}}
	]`)

	completion, err := New(server.URL, "").FetchCompletion(CompletionLookup{
		CommandID: "wanted", Parties: []string{"Alice"}, Limit: 10, TimeoutMs: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := completion.String("commandId", "command_id"); got != "wanted" {
		t.Errorf("commandId = %q", got)
	}
	if _, message := completion.StatusFields(); !strings.Contains(fmt.Sprint(message), "Insufficient balance") {
		t.Errorf("message = %v", message)
	}
}

// A command id that never appears must say so rather than returning the wrong
// completion.
func TestFetchCompletionReportsNoMatch(t *testing.T) {
	server := completionServer(t, `[{"completionResponse":{"Completion":{"value":{"commandId":"other"}}}}]`)

	_, err := New(server.URL, "").FetchCompletion(CompletionLookup{
		CommandID: "missing", Parties: []string{"Alice"}, Limit: 10, TimeoutMs: 100,
	})
	if err == nil {
		t.Fatal("a missing command id returned no error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error does not name the command id: %v", err)
	}
}

// Argument validation happens before the request, so a mistake does not depend
// on a participant being reachable.
func TestFetchCompletionValidatesArguments(t *testing.T) {
	if _, err := New("", "").FetchCompletion(CompletionLookup{Parties: []string{"Alice"}}); err == nil {
		t.Error("no ledger URL accepted")
	}
	if _, err := New("http://x", "").FetchCompletion(CompletionLookup{}); err == nil {
		t.Error("no parties accepted")
	}
	_, err := New("http://x", "").FetchCompletion(CompletionLookup{
		Parties: []string{"Alice"}, BeginExclusive: "not-a-number",
	})
	if err == nil || !strings.Contains(err.Error(), "--begin-exclusive") {
		t.Errorf("non-numeric offset = %v", err)
	}
}

func TestFetchCompletionNotFoundNamesTheLikelyCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	_, err := New(server.URL, "").FetchCompletion(CompletionLookup{
		CommandID: "cmd-1",
		Parties:   []string{"Alice::1220aa"},
	})
	if err == nil {
		t.Fatal("missing completion returned no error")
	}
	for _, want := range []string{"never reaches the ledger", "--export", "--completion-file", "--begin-exclusive", "--completion-limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
