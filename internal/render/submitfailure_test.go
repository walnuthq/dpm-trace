package render

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/source"
)

func loadCompletion(t *testing.T, rel string) *model.Completion {
	t.Helper()
	c, err := model.LoadCompletion(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// SubmitFailure is the compact view a user sees when `dpm trace submit` is
// rejected: what was sent, why it failed, and where to look next.
func TestSubmitFailureRendersTheRejection(t *testing.T) {
	c := loadCompletion(t, "tests/fixtures/compare/completion-fail.json")

	var buf bytes.Buffer
	SubmitFailure(&buf, c, nil, "", plain(), source.NewIndex(), 5)
	out := buf.String()

	if !strings.Contains(out, "submission failed") {
		t.Errorf("no failure banner in:\n%s", out)
	}
	if !strings.Contains(out, "FAILED_PRECONDITION") {
		t.Errorf("status code missing from:\n%s", out)
	}
}

// The "what you sent" block reads the submitted request, so a user can see the
// command without re-reading their shell history.
func TestSubmitFailureShowsTheCommand(t *testing.T) {
	c := loadCompletion(t, "tests/fixtures/compare/completion-fail.json")

	args := model.NewObject()
	args.Set("newOwner", "Bob::1220abcd")
	request := map[string]any{
		"commands": []any{
			map[string]any{"ExerciseCommand": map[string]any{
				"templateId":     "pkg:Asset:Asset",
				"choice":         "Transfer",
				"contractId":     "00abcdef0123456789",
				"choiceArgument": args,
			}},
		},
	}

	var buf bytes.Buffer
	SubmitFailure(&buf, c, request, "", plain(), source.NewIndex(), 5)
	out := buf.String()

	for _, want := range []string{"exercise", "Asset:Asset", "Transfer", "newOwner="} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// An interpretation failure has no completion, so the next step must read the
// captured rejection rather than look the command id up.
func TestSubmitFailureNextStepReadsTheCapturedRejection(t *testing.T) {
	c := loadCompletion(t, "examples/failed-withdraw.completion.json")

	cases := []struct {
		name       string
		exportPath string
		want       []string
	}{
		{"exported", "failure.json", []string{
			"dpm trace compare --prepared <prepared.json> --completion-file failure.json",
		}},
		{"not exported", "", []string{
			"rerun with --export <file>",
			"dpm trace compare --prepared <prepared.json> --completion-file <file>",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			SubmitFailure(&buf, c, nil, tc.exportPath, plain(), source.NewIndex(), 5)
			out := buf.String()

			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			if strings.Contains(out, "--command-id") {
				t.Errorf("suggests a --command-id lookup that finds nothing:\n%s", out)
			}
		})
	}
}

func TestFormatCommandSummary(t *testing.T) {
	createArgs := model.NewObject()
	createArgs.Set("issuer", "Alice")
	createArgs.Set("name", "GOLD")

	create := map[string]any{"CreateCommand": map[string]any{
		"templateId": "pkg:Asset:Asset", "createArguments": createArgs,
	}}
	got := formatCommandSummary(create)
	if !strings.HasPrefix(got, "create ") || !strings.Contains(got, "Asset:Asset") {
		t.Errorf("create summary = %q", got)
	}
	if !strings.Contains(got, "issuer=Alice") {
		t.Errorf("arguments missing from %q", got)
	}

	exercise := map[string]any{"ExerciseCommand": map[string]any{
		"templateId": "pkg:Asset:Asset", "choice": "Burn", "contractId": "00abcdef0123456789",
	}}
	if got := formatCommandSummary(exercise); !strings.Contains(got, "exercise") || !strings.Contains(got, "Burn") {
		t.Errorf("exercise summary = %q", got)
	}

	// An unrecognised shape must still render something rather than panic.
	if got := formatCommandSummary(map[string]any{"Mystery": 1}); got == "" {
		t.Error("unknown command rendered nothing")
	}
}

// Only the first three arguments are shown, in the order they were given --
// --arg flag order is what a user recognises.
func TestSummarizeArgsCapsAtThree(t *testing.T) {
	args := model.NewObject()
	for _, key := range []string{"one", "two", "three", "four"} {
		args.Set(key, key)
	}
	got := summarizeArgs(args)
	if strings.Contains(got, "four") {
		t.Errorf("more than three arguments rendered: %q", got)
	}
	if strings.Index(got, "one") > strings.Index(got, "two") {
		t.Errorf("arguments reordered: %q", got)
	}
	if got := summarizeArgs(model.NewObject()); got != "" {
		t.Errorf("empty arguments rendered %q", got)
	}
	if got := summarizeArgs("not an object"); got != "" {
		t.Errorf("non-object rendered %q", got)
	}
}

func TestOrQuestion(t *testing.T) {
	if got := orQuestion(""); got != "?" {
		t.Errorf("empty = %q, want ?", got)
	}
	if got := orQuestion("Transfer"); got != "Transfer" {
		t.Errorf("value = %q", got)
	}
}

func TestCommandBodyPrefersTheFirstMatchingKey(t *testing.T) {
	obj := map[string]any{"createCommand": map[string]any{"templateId": "t"}}
	if _, ok := commandBody(obj, "CreateCommand", "createCommand"); !ok {
		t.Error("lower-camel key not matched")
	}
	if _, ok := commandBody(obj, "ExerciseCommand"); ok {
		t.Error("matched a key that is not present")
	}
}
