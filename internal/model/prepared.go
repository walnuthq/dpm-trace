package model

import (
	"fmt"
	"os"
	"time"
)

// PreparedArtifactSchema is the schema string prepared artifacts carry.
const PreparedArtifactSchema = "dpm-trace/prepared-artifact/v0"

// LoadPreparedArtifact reads and validates a prepared-command artifact.
// Ports load_prepared_artifact.
func LoadPreparedArtifact(path string) (*Object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	artifact, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("prepared artifact must be a JSON object: %s", path)
	}
	if schema := pickString(artifact, "schema"); schema != PreparedArtifactSchema {
		if schema == "" {
			return nil, fmt.Errorf("%s has no schema field, so it is not a prepared artifact", path)
		}
		return nil, fmt.Errorf("unsupported prepared artifact schema in %s: '%s'", path, schema)
	}
	return artifact, nil
}

// PreparedSummary is the prepared side of a comparison header.
// Ports prepared_summary_for_compare.
type PreparedSummary struct {
	CommandID               string
	ActAs                   []string
	ReadAs                  []string
	Commands                int
	PreparedTransactionHash string
	Committed               bool
}

// SummarizePrepared ports prepared_summary_for_compare.
func SummarizePrepared(prepared *Object) PreparedSummary {
	request, _ := pickObject(prepared, "request")
	response, _ := pickObject(prepared, "response")
	commands, _ := pick(request, "commands").([]any)
	return PreparedSummary{
		CommandID:               pickString(request, "commandId"),
		ActAs:                   listString(pick(request, "actAs")),
		ReadAs:                  listString(pick(request, "readAs")),
		Commands:                len(commands),
		PreparedTransactionHash: pickString(response, "preparedTransactionHash"),
		Committed:               false,
	}
}

// CommandRow is one submitted command, shaped for comparison against an event.
// Ports command_compare_row.
type CommandRow struct {
	Kind       string
	Template   string
	Choice     string
	ContractID string
	Value      any
	ValueLabel string
	Keys       []string // populated for unknown command shapes
}

// CommandCompareRow ports command_compare_row.
func CommandCompareRow(command *Object) CommandRow {
	if body, ok := pickObject(command, "CreateCommand"); ok {
		return CommandRow{
			Kind:       KindCreate,
			Template:   pickString(body, "templateId"),
			Value:      pick(body, "createArguments"),
			ValueLabel: "arguments",
		}
	}
	if body, ok := pickObject(command, "ExerciseCommand"); ok {
		return CommandRow{
			Kind:       KindExercise,
			Template:   pickString(body, "templateId"),
			Choice:     pickString(body, "choice"),
			ContractID: shortValue(pickString(body, "contractId"), 48),
			Value:      pick(body, "choiceArgument"),
			ValueLabel: "choice argument",
		}
	}
	return CommandRow{Kind: "unknown", Keys: sortedKeys(command)}
}

// PreparedComparison is the result of comparing a prepared command to a
// committed update.
//
// When the artifact's prepared transaction decodes, PreparedTree and
// CommittedTree hold both sides as event trees, children included, and
// TreeCompared is set. Otherwise only the commands can be compared against the
// root events, and TreeSkipped says why.
type PreparedComparison struct {
	Kind          string
	Left          PreparedSummary
	Right         TraceSummary
	Commands      []CommandRow
	RootEvents    []CompareRow
	StateDiff     map[string]int
	PreparedLen   int
	RootLen       int
	TreeCompared  bool
	TreeSkipped   string
	PreparedTree  []CompareRow
	CommittedTree []CompareRow
}

// ComparePreparedToTrace ports compare_prepared_to_trace.
func ComparePreparedToTrace(prepared *Object, trace *Trace) *PreparedComparison {
	request, _ := pickObject(prepared, "request")
	rawCommands, _ := pick(request, "commands").([]any)

	commands := []CommandRow{}
	for _, raw := range rawCommands {
		if obj, ok := asObject(raw); ok {
			commands = append(commands, CommandCompareRow(obj))
		}
	}

	roots := []CompareRow{}
	for _, id := range trace.RootEventIDs {
		if ev, ok := trace.EventsByID[id]; ok {
			// These rows describe the committed side, which has children: a
			// consuming exercise and the create it produced. Building them
			// without the event map dropped that subtree, so the same event
			// carried its children in an update comparison and none here.
			roots = append(roots, EventCompareRow(ev, trace.EventsByID))
		}
	}

	comparison := &PreparedComparison{
		Kind:        KindPreparedVsUpdate,
		Left:        SummarizePrepared(prepared),
		Right:       SummarizeTrace(trace),
		Commands:    commands,
		RootEvents:  roots,
		StateDiff:   StateDiffCounts(trace),
		PreparedLen: len(rawCommands),
		RootLen:     len(roots),
	}

	tx, err := PreparedTransactionOf(prepared)
	if err != nil {
		comparison.TreeSkipped = err.Error()
		return comparison
	}
	readAs := trace.Projection.ReadAs
	comparison.TreeCompared = true
	comparison.PreparedTree = comparableTree(tx.Trace(readAs), len(readAs) > 0)
	comparison.CommittedTree = comparableTree(trace, len(readAs) > 0)
	return comparison
}

// comparableTree shapes a trace's event tree for a prepared-vs-committed diff.
//
// A contract the transaction creates gets a different id each time the command
// is interpreted, and a prepared transaction's ids are not yet suffixed, so the
// two sides can never agree on one. Each created id, wherever it appears, is
// replaced by its position: the first contract the transaction creates is
// "<created #1>" on both sides. Party lists are sorted, since the Ledger API
// does not promise an order, and witnesses are dropped when there are no read-as
// parties to derive them from.
func comparableTree(trace *Trace, keepWitnesses bool) []CompareRow {
	created := map[string]string{}
	var number func(ids []string)
	number = func(ids []string) {
		for _, id := range ids {
			ev, ok := trace.EventsByID[id]
			if !ok {
				continue
			}
			if ev.Kind == KindCreate && ev.ContractID != "" {
				if _, seen := created[ev.ContractID]; !seen {
					created[ev.ContractID] = fmt.Sprintf("<created #%d>", len(created)+1)
				}
			}
			number(ev.ChildEventIDs)
		}
	}
	number(trace.RootEventIDs)

	events := make(map[string]*Event, len(trace.EventsByID))
	for id, ev := range trace.EventsByID {
		copied := *ev
		if token, ok := created[ev.ContractID]; ok {
			copied.ContractID = token
		}
		copied.Payload = replaceCreatedIDs(ev.Payload, created)
		copied.Argument = replaceCreatedIDs(ev.Argument, created)
		copied.Result = replaceCreatedIDs(ev.Result, created)
		copied.Signatories = sortedCopy(ev.Signatories)
		copied.Observers = sortedCopy(ev.Observers)
		copied.ActingParties = sortedCopy(ev.ActingParties)
		copied.Witnesses = nil
		if keepWitnesses {
			copied.Witnesses = sortedCopy(ev.Witnesses)
		}
		events[id] = &copied
	}

	rows := []CompareRow{}
	for _, id := range trace.RootEventIDs {
		if ev, ok := events[id]; ok {
			rows = append(rows, EventCompareRow(ev, events))
		}
	}
	return rows
}

func replaceCreatedIDs(value any, created map[string]string) any {
	switch v := value.(type) {
	case string:
		if token, ok := created[v]; ok {
			return token
		}
		return v
	case *Object:
		out := NewObject()
		for _, key := range v.Keys() {
			item, _ := v.Get(key)
			out.Set(key, replaceCreatedIDs(item, created))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = replaceCreatedIDs(item, created)
		}
		return out
	}
	return value
}

func sortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string{}, values...)
	sortStrings(out)
	return out
}

// JSON renders the comparison for --print-json.
func (c *PreparedComparison) JSON() map[string]any {
	commands := make([]any, 0, len(c.Commands))
	for _, row := range c.Commands {
		commands = append(commands, row.json())
	}
	return map[string]any{
		"kind":  c.Kind,
		"left":  c.Left.json(),
		"right": c.Right.json(),
		"diff": map[string]any{
			"commandCount": map[string]any{
				"prepared":         c.PreparedLen,
				"updateRootEvents": c.RootLen,
			},
			"rootEvents": rowsJSON(c.RootEvents),
			"commands":   commands,
			"stateDiff":  intMap(c.StateDiff),
			"tree":       c.treeJSON(),
			"notes":      c.notes(),
		},
	}
}

func (c *PreparedComparison) treeJSON() map[string]any {
	if !c.TreeCompared {
		return map[string]any{"compared": false, "reason": c.TreeSkipped}
	}
	return map[string]any{
		"compared":  true,
		"prepared":  rowsJSON(c.PreparedTree),
		"committed": rowsJSON(c.CommittedTree),
	}
}

func (c *PreparedComparison) notes() []string {
	notes := []string{
		"Prepared data is not committed.",
		"The comparison checks visible command/event shape; it is not proof of semantic equivalence.",
	}
	if c.TreeCompared {
		return append(notes, "Contracts the transaction creates are compared by position, since their ids differ between interpretations.")
	}
	return append(notes, "Child events were not compared: "+c.TreeSkipped+".")
}

func (s PreparedSummary) json() map[string]any {
	return map[string]any{
		"commandId":               nilIfBlank(s.CommandID),
		"actAs":                   stringsJSON(s.ActAs),
		"readAs":                  stringsJSON(s.ReadAs),
		"commands":                s.Commands,
		"preparedTransactionHash": nilIfBlank(s.PreparedTransactionHash),
		"committed":               s.Committed,
	}
}

func (r CommandRow) json() map[string]any {
	if r.Kind == "unknown" {
		return map[string]any{"kind": "unknown", "keys": stringsJSON(r.Keys)}
	}
	out := map[string]any{
		"kind":       r.Kind,
		"template":   nilIfBlank(r.Template),
		"value":      r.Value,
		"valueLabel": nilIfBlank(r.ValueLabel),
	}
	if r.Kind == KindExercise {
		out["choice"] = nilIfBlank(r.Choice)
		out["contractId"] = r.ContractID
	}
	return out
}

func sortedKeys(obj *Object) []string {
	keys := obj.Keys()
	sortStrings(keys)
	return keys
}

// NewPreparedArtifact wraps a prepare request and response for export.
// Ports create_prepared_artifact.
//
// committed is always false and the privacy note is fixed: this is prepared
// data from a non-committing call, and the artifact must never read as a
// committed transaction.
func NewPreparedArtifact(request map[string]any, response *Object, sourceURL, ledgerURL string, actAs, readAs []string) map[string]any {
	return map[string]any{
		"schema":    PreparedArtifactSchema,
		"createdAt": isoTimestamp(time.Now().UTC()),
		"kind":      "prepared-command",
		"source":    "ledger-json-api",
		"sourceUrl": sourceURL,
		"participant": map[string]any{
			"ledgerUrl": nilIfBlank(ledgerURL),
			"actAs":     stringsJSON(actAs),
			"readAs":    stringsJSON(readAs),
		},
		"committed": false,
		"request":   request,
		"response":  response,
		"privacy": map[string]any{
			"scope":                    "Prepared command result from an authorized participant endpoint.",
			"missingPrivateDataPolicy": "counterparty-private data outside this authorization context is not present in the artifact",
		},
	}
}
