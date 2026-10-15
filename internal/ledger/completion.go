package ledger

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/walnuthq/dpm-trace/internal/model"
)

// CompletionLookup describes a completions query.
type CompletionLookup struct {
	CommandID      string
	Parties        []string
	UserID         string
	BeginExclusive string
	Limit          int
	TimeoutMs      int
}

// FetchCompletion finds the completion for a command id.
//
// A failed submission has no update id, so this is the only way to see what
// happened to it. The window is bounded by beginExclusive and limit, and a
// command outside that window is reported as such rather than as "no failure".
// Ports fetch_completion_by_command_id.
func (c *Client) FetchCompletion(lookup CompletionLookup) (*model.Completion, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("--submitter/--participant-url/--ledger-url is required")
	}
	if len(lookup.Parties) == 0 {
		return nil, fmt.Errorf("--act-as, --read-as, or --party is required for completion lookup")
	}

	// int() tolerates surrounding whitespace, so " 5 " is 5. An empty value is
	// rejected by the flag parser, which knows whether the flag was given.
	beginExclusive := int64(0)
	if lookup.BeginExclusive != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(lookup.BeginExclusive), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--begin-exclusive must be an integer offset")
		}
		beginExclusive = parsed
	}

	body := map[string]any{
		"parties":        lookup.Parties,
		"beginExclusive": beginExclusive,
	}
	switch {
	case lookup.UserID != "":
		body["userId"] = lookup.UserID
	case c.Token == "":
		// Without a token the participant cannot resolve the user itself.
		body["userId"] = UserID("", c.BaseURL, "", "")
	}

	limit := lookup.Limit
	if limit < 1 {
		limit = 1
	}
	timeout := lookup.TimeoutMs
	if timeout < 1 {
		timeout = 1
	}

	url := JoinURL(c.BaseURL, CompletionsPath)
	query := fmt.Sprintf("?limit=%d&stream_idle_timeout_ms=%d", limit, timeout)
	raw, err := c.JSONValue("POST", url+query, body, true)
	if err != nil {
		return nil, err
	}

	for _, candidate := range NormalizeCompletionList(raw) {
		if model.ObjectString(candidate, "commandId") != lookup.CommandID {
			continue
		}
		candidate.Set("source", "ledger-json-api")
		candidate.Set("sourceUrl", url)

		context := model.NewObject()
		context.Set("commandId", lookup.CommandID)
		context.Set("parties", toAnySlice(lookup.Parties))
		context.Set("beginExclusive", beginExclusive)
		context.Set("limit", lookup.Limit)
		candidate.Set("lookup", context)

		return &model.Completion{Raw: candidate}, nil
	}

	// Single quotes, matching Python's !r formatting of the command id.
	return nil, fmt.Errorf(
		"no completion for command '%s'.\n"+
			"A command rejected during interpretation never reaches the ledger, so it has no "+
			"completion to look up; capture the rejection with `submit --export <file>` "+
			"and read it with --completion-file <file>.\n"+
			"If the command was sequenced, it may be outside the queried window: widen it with "+
			"--begin-exclusive or --completion-limit",
		lookup.CommandID)
}

// NormalizeCompletionList unwraps the several shapes a completions response
// arrives in. Ports normalize_completion_list.
func NormalizeCompletionList(raw any) []*model.Object {
	var candidates []any
	switch value := raw.(type) {
	case *model.Object:
		// An or-chain, as normalize_completion_list uses: a falsy value (an
		// empty list) falls through to the next key, and the first truthy one
		// wins even if it is not a list -- in which case there are no
		// completions rather than the whole object being treated as one.
		var picked any
		for _, key := range []string{"completions", "items", "responses", "completionResponses"} {
			nested, ok := value.Get(key)
			if !ok || isFalsy(nested) {
				continue
			}
			picked = nested
			break
		}
		switch {
		case picked == nil:
			candidates = []any{value}
		default:
			list, isList := picked.([]any)
			if !isList {
				return nil
			}
			candidates = list
		}
	case []any:
		candidates = value
	default:
		return nil
	}

	var result []*model.Object
	for _, item := range candidates {
		obj, ok := item.(*model.Object)
		if !ok {
			continue
		}
		if completion := model.NormalizeCompletion(obj); completion.Len() > 0 {
			result = append(result, completion)
		}
	}
	return result
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// isFalsy reports whether a decoded JSON value is falsy the way Python's or
// treats it: nil, false, an empty string, an empty list or an empty object.
func isFalsy(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return !v
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case *model.Object:
		return v.Len() == 0
	}
	return false
}
