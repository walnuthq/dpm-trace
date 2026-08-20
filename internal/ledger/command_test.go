package ledger

import (
	"testing"
)

func TestParseScalarInfersTypes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want any
	}{
		{"null", nil},
		{"true", true},
		{"false", false},
		// Numbers stay strings: the JSON Ledger API encodes Daml Int64 and
		// Numeric as strings, and Canton 3.5 rejects a JSON number with
		// "Expected ujson.Str".
		{"42", "42"},
		{"-7", "-7"},
		{"1.5", "1.5"},
		{"GOLD", "GOLD"},
		{"1.2.3", "1.2.3"},         // not a number
		{"{bad json", "{bad json"}, // invalid JSON stays a string
	} {
		if got := ParseScalar(tc.in); got != tc.want {
			t.Errorf("ParseScalar(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
	if _, ok := ParseScalar(`{"a":1}`).(map[string]any); !ok {
		if ParseScalar(`{"a":1}`) == nil {
			t.Error("object literal should decode")
		}
	}
}

// Flag order is observable: the failure view prints the first three arguments
// as given, so assignments must not be reordered.
func TestParseArgAssignmentsPreservesOrder(t *testing.T) {
	args, err := ParseArgAssignments([]string{"payer=A", "owner=B", "amount=3"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"payer", "owner", "amount"}
	got := args.Keys()
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}

func TestParseArgAssignmentsRejectsBadSyntax(t *testing.T) {
	if _, err := ParseArgAssignments([]string{"novalue"}); err == nil {
		t.Error("expected an error for a missing =")
	}
	if _, err := ParseArgAssignments([]string{"=value"}); err == nil {
		t.Error("expected an error for an empty key")
	}
}

// parse_parties deliberately does not deduplicate: the request carries the
// parties as given.
func TestParsePartiesKeepsDuplicates(t *testing.T) {
	got := ParseParties([]string{"A,B", " A ", "C"})
	want := []string{"A", "B", "A", "C"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCommandsRequiresASource(t *testing.T) {
	if _, err := (CommandSpec{}).Commands(); err == nil {
		t.Error("expected an error with no --commands, --command-json or --template")
	}
	if _, err := (CommandSpec{CommandsFile: "a", CommandJSON: "b"}).Commands(); err == nil {
		t.Error("expected an error when both --commands and --command-json are given")
	}
}

func TestExerciseRequiresContractIDAndChoice(t *testing.T) {
	if _, err := (CommandSpec{Template: "T", Choice: "C"}).Commands(); err == nil {
		t.Error("expected an error: --contract-id is required for an exercise")
	}
	if _, err := (CommandSpec{Template: "T", ContractID: "cid"}).Commands(); err == nil {
		t.Error("expected an error: --choice is required for an exercise")
	}
}

func TestUserIDFallsBackOnlyWithoutCredentials(t *testing.T) {
	if got := UserID("", "http://localhost:7575", "", ""); got != "participant_admin" {
		t.Errorf("got %q, want the default user id", got)
	}
	if got := UserID("", "http://localhost:7575", "token", ""); got != "" {
		t.Errorf("got %q; a token means the participant resolves the user", got)
	}
	if got := UserID("explicit", "http://localhost:7575", "", ""); got != "explicit" {
		t.Errorf("got %q, want the explicit user id", got)
	}
}
