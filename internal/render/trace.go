package render

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/walnuthq/dpm-trace/internal/model"
	"github.com/walnuthq/dpm-trace/internal/source"
)

// PrettyTrace writes the participant-visible trace: header, party aliases and
// the event tree. Ports print_pretty_trace.
//
// index may be nil, in which case no source lines are emitted -- the same as
// Python without source metadata.
func PrettyTrace(w io.Writer, trace *model.Trace, color Color, index *source.Index) {
	ctx := NewContext(trace)

	fmt.Fprintln(w, color.Apply("Canton trace", "bold"))
	fmt.Fprintf(w, "  update:       %s\n", Short(trace.UpdateID, 80))
	fmt.Fprintf(w, "  source:       %s (%s)\n", trace.Source, orDash(trace.SourceURL))
	fmt.Fprintf(w, "  offset:       %s\n", orDash(trace.Offset))
	fmt.Fprintf(w, "  record time:  %s\n", orDash(trace.RecordTime))
	fmt.Fprintf(w, "  synchronizer: %s\n", Short(trace.SynchronizerID, 80))
	if len(trace.Projection.ReadAs) > 0 {
		rendered := make([]string, 0, len(trace.Projection.ReadAs))
		for _, party := range trace.Projection.ReadAs {
			rendered = append(rendered, ctx.PartyWithFull(party))
		}
		fmt.Fprintf(w, "  read-as:      %s\n", strings.Join(rendered, ", "))
	}
	fmt.Fprintf(w, "  visibility:   %s\n", trace.Projection.Note)
	fmt.Fprintf(w, "  events:       %s\n", StateDiffSummary(trace, color))
	if index != nil && index.HasSources() {
		fmt.Fprintf(w, "  source roots: %s\n", strings.Join(index.Roots, ", "))
	}

	if len(ctx.PartyAliases) > 0 {
		fmt.Fprintln(w, "  parties:")
		// Python sorts by alias, not by party id.
		type pair struct{ party, alias string }
		pairs := make([]pair, 0, len(ctx.PartyAliases))
		for party, alias := range ctx.PartyAliases {
			pairs = append(pairs, pair{party, alias})
		}
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i].alias != pairs[j].alias {
				return pairs[i].alias < pairs[j].alias
			}
			return pairs[i].party < pairs[j].party
		})
		for _, p := range pairs {
			fmt.Fprintf(w, "    %s = %s\n", p.alias, p.party)
		}
	}
	fmt.Fprintln(w)

	if len(trace.RootEventIDs) == 0 {
		fmt.Fprintln(w, color.Apply("No events found.", "yellow"))
		return
	}

	fmt.Fprintln(w, color.Apply("Trace", "bold"))
	for i, eventID := range trace.RootEventIDs {
		eventTree(w, trace, eventID, "", i == len(trace.RootEventIDs)-1, color, ctx, index)
	}
}

// glyphSet is the four pieces a tree is drawn from.
type glyphSet struct {
	branch   string // a child with siblings after it
	last     string // the final child
	vertical string // the column under a branch, carrying the line down
	blank    string // the column under the last child
}

// Branch, Last, Vertical and Blank expose the glyphs to other renderers, so a
// second tree does not invent its own notation for the same nesting.
func (g glyphSet) Branch() string   { return g.branch }
func (g glyphSet) Last() string     { return g.last }
func (g glyphSet) Vertical() string { return g.vertical }
func (g glyphSet) Blank() string    { return g.blank }

var (
	unicodeGlyphs = glyphSet{branch: "\u251c\u2500\u2500 ", last: "\u2514\u2500\u2500 ", vertical: "\u2502   ", blank: "    "}
	asciiGlyphs   = glyphSet{branch: "|-- ", last: "`-- ", vertical: "|   ", blank: "    "}
)

// TreeGlyphs picks the glyphs to draw with.
//
// Box-drawing characters need a UTF-8 console, which Windows does not
// guarantee: cmd.exe on a legacy code page renders them as mojibake, which is
// worse than the ASCII it replaced. Detection is by platform rather than by
// locale on purpose -- a locale-sensitive default would render differently on
// a developer's machine and in CI, and the goldens assert every byte.
//
// DPM_TRACE_ASCII overrides in both directions, so a Windows terminal that does
// support UTF-8 can opt in, and a pipeline that cannot can opt out anywhere.
func TreeGlyphs() glyphSet {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DPM_TRACE_ASCII"))) {
	case "1", "true", "yes":
		return asciiGlyphs
	case "0", "false", "no":
		return unicodeGlyphs
	}
	if runtime.GOOS == "windows" {
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// eventTree writes one event and its descendants. Ports print_event_tree.
func eventTree(w io.Writer, trace *model.Trace, eventID, prefix string, isLast bool, color Color, ctx *Context, index *source.Index) {
	ev, ok := trace.Event(eventID)
	if !ok {
		return
	}

	glyphs := TreeGlyphs()
	connector := glyphs.branch
	childPrefix := glyphs.vertical
	if isLast {
		connector = glyphs.last
		childPrefix = glyphs.blank
	}
	fmt.Fprintln(w, prefix+connector+EventTitle(ev, color))

	details := EventDetailLines(ev, color, ctx, index)
	for i, line := range details {
		detailConnector := glyphs.branch
		if i == len(details)-1 && len(ev.ChildEventIDs) == 0 {
			detailConnector = glyphs.last
		}
		fmt.Fprintln(w, prefix+childPrefix+detailConnector+line)
	}

	for i, childID := range ev.ChildEventIDs {
		eventTree(w, trace, childID, prefix+childPrefix, i == len(ev.ChildEventIDs)-1, color, ctx, index)
	}
}

// EventTitle renders "[id] KIND Template.Choice". Ports event_title.
func EventTitle(ev *model.Event, color Color) string {
	return color.Apply("["+ev.EventID+"]", "gray") + " " +
		color.Apply(EventKindLabel(ev.Kind), EventColor(ev.Kind), "bold") + " " +
		color.Apply(EventTarget(ev), "bold")
}

// EventTarget is the template, with the choice appended when there is one.
func EventTarget(ev *model.Event) string {
	template := ShortTemplate(ev.Template)
	if template == "" {
		template = "<unknown>"
	}
	if ev.Choice != "" {
		return template + "." + ev.Choice
	}
	return template
}

// EventDetailLines renders the indented detail block under an event.
// Ports event_detail_lines.
func EventDetailLines(ev *model.Event, color Color, ctx *Context, index *source.Index) []string {
	var lines []string
	// The source line names the file and line only: the entity label already
	// says which template or choice it is.
	if index != nil {
		if loc := index.LocationForEvent(ev); loc != nil {
			lines = append(lines, LabelValue("source",
				fmt.Sprintf("%s:%d (%s)", filepath.Base(loc.Path), loc.Line, loc.Label), color))
		}
	}
	if ev.ContractID != "" {
		lines = append(lines, LabelValue("contract", Short(ev.ContractID, 66), color))
	}
	if ev.Consuming != nil && ev.Kind == model.KindExercise {
		lines = append(lines, LabelValue("consuming", strconv.FormatBool(*ev.Consuming), color))
	}
	lines = append(lines, ReassignmentDetailLines(ev, color, ctx)...)
	for _, group := range []struct {
		label   string
		parties []string
	}{
		{"actors", ev.ActingParties},
		{"signatories", ev.Signatories},
		{"observers", ev.Observers},
		{"witnesses", ev.Witnesses},
	} {
		if len(group.parties) == 0 {
			continue
		}
		rendered := make([]string, 0, len(group.parties))
		for _, party := range group.parties {
			rendered = append(rendered, ctx.Party(party))
		}
		lines = append(lines, LabelValue(group.label, strings.Join(rendered, ", "), color))
	}
	if ev.Argument != nil {
		lines = append(lines, BlockLines("argument", ev.Argument, color, ctx)...)
	}
	if ev.Payload != nil {
		lines = append(lines, BlockLines("payload", ev.Payload, color, ctx)...)
	}
	if ev.Result != nil {
		lines = append(lines, BlockLines("result", ev.Result, color, ctx)...)
	}
	return lines
}

// ReassignmentDetailLines renders where a contract moved. Emitted for any event
// carrying the metadata, so a partial reassignment payload still shows it.
// Ports reassignment_detail_lines.
func ReassignmentDetailLines(ev *model.Event, color Color, ctx *Context) []string {
	var lines []string
	if ev.SourceSynchronizer != "" || ev.TargetSynchronizer != "" {
		lines = append(lines, LabelValue("reassignment",
			Short(ev.SourceSynchronizer, 40)+" -> "+Short(ev.TargetSynchronizer, 40), color))
	}
	if ev.ReassignmentID != "" {
		lines = append(lines, LabelValue("reassignment id", Short(ev.ReassignmentID, 66), color))
	}
	if ev.ReassignmentCounter != nil {
		lines = append(lines, LabelValue("counter", strconv.FormatInt(*ev.ReassignmentCounter, 10), color))
	}
	if ev.Submitter != "" {
		lines = append(lines, LabelValue("submitter", ctx.Party(ev.Submitter), color))
	}
	return lines
}

// LabelValue renders "label: value" with the label styled.
func LabelValue(label, value string, color Color) string {
	return color.Apply(label+":", "cyan") + " " + value
}

// BlockLines renders a value inline when short, or as an indented block.
// Ports block_lines.
func BlockLines(label string, value any, color Color, ctx *Context) []string {
	rendered := RenderPrettyValue(value, ctx)
	if !strings.Contains(rendered, "\n") {
		return []string{LabelValue(label, Short(rendered, 120), color)}
	}
	lines := []string{color.Apply(label+":", "cyan")}
	for _, line := range strings.Split(rendered, "\n") {
		lines = append(lines, "  "+line)
	}
	return lines
}

// StateDiffSummary renders what the transaction did, per event kind.
//
// The counts describe effects rather than partitioning the event list, so they
// can total more than the event count: a consuming exercise both exercises and
// archives, and is counted in each. A transaction tree never carries a separate
// archived event -- the Ledger API reports the archive as `consuming: true` on
// the exercise, and only synthesizes ArchivedEvent in the flat/ACS views -- so
// counting kinds alone reports "x0 archive" for a transaction that plainly
// archived a contract.
func StateDiffSummary(trace *model.Trace, color Color) string {
	counts := map[string]int{}
	for _, ev := range trace.EventsByID {
		switch ev.Kind {
		case model.KindCreate, model.KindExercise, model.KindArchive, model.KindAssign, model.KindUnassign:
			counts[ev.Kind]++
		default:
			counts[model.KindEvent]++
		}
		if isConsumingExercise(ev) {
			counts[model.KindArchive]++
		}
	}
	parts := []string{
		color.Apply(fmt.Sprintf("+%d create", counts[model.KindCreate]), "green"),
		color.Apply(fmt.Sprintf(">%d exercise", counts[model.KindExercise]), "yellow"),
		color.Apply(fmt.Sprintf("x%d archive", counts[model.KindArchive]), "red"),
	}
	if counts[model.KindUnassign] > 0 {
		parts = append(parts, color.Apply(fmt.Sprintf("<%d unassign", counts[model.KindUnassign]), "magenta"))
	}
	if counts[model.KindAssign] > 0 {
		parts = append(parts, color.Apply(fmt.Sprintf(">%d assign", counts[model.KindAssign]), "magenta"))
	}
	if counts[model.KindEvent] > 0 {
		parts = append(parts, color.Apply(fmt.Sprintf("%d other", counts[model.KindEvent]), "blue"))
	}
	return strings.Join(parts, ", ")
}

// isConsumingExercise reports whether an event archives its contract.
//
// Consuming is a *bool: nil means the field was absent, which is not the same
// as false, so an event that never carried the flag is not treated as an
// archive. The kind check guards against a consuming flag on something that is
// not an exercise, where it would carry no meaning.
func isConsumingExercise(ev *model.Event) bool {
	return ev.Kind == model.KindExercise && ev.Consuming != nil && *ev.Consuming
}

// EventKindLabel is the uppercase marker shown in the tree.
func EventKindLabel(kind string) string {
	switch kind {
	case model.KindCreate:
		return "CREATE"
	case model.KindExercise:
		return "EXERCISE"
	case model.KindArchive:
		return "ARCHIVE"
	case model.KindAssign:
		return "ASSIGN"
	case model.KindUnassign:
		return "UNASSIGN"
	}
	return strings.ToUpper(kind)
}

// EventColor is the style name for an event kind.
func EventColor(kind string) string {
	switch kind {
	case model.KindCreate:
		return "green"
	case model.KindExercise:
		return "yellow"
	case model.KindArchive:
		return "red"
	case model.KindAssign, model.KindUnassign:
		return "magenta"
	}
	return "blue"
}

// TraceArtifactSummary renders the header shown when opening an artifact.
// Ports trace_artifact_summary.
func TraceArtifactSummary(artifact *model.Object) string {
	trace, _ := model.ObjectField(artifact, "trace")
	participant, _ := model.ObjectField(artifact, "participant")
	events, _ := model.ObjectField(trace, "eventsById")

	readAs := model.ObjectStrings(participant, "readAs")
	readAsText := "-"
	if len(readAs) > 0 {
		readAsText = strings.Join(readAs, ", ")
	}

	return strings.Join([]string{
		"Trace artifact",
		"  schema:       " + model.ObjectString(artifact, "schema"),
		"  update:       " + orDashValue(model.ObjectString(trace, "updateId")),
		"  kind:         " + orDashValue(model.ObjectString(artifact, "kind")),
		"  source:       " + orDashValue(model.ObjectString(trace, "source")),
		"  read-as:      " + readAsText,
		"  events:       " + strconv.Itoa(events.Len()),
	}, "\n")
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func orDashValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// PreparedArtifactSummary renders the header shown after a prepare.
// Ports prepared_artifact_summary. The "committed: no" line and the closing
// sentence are not decoration: prepared data must never read as committed.
func PreparedArtifactSummary(artifact map[string]any) string {
	// Nested values are plain maps when `prepare` builds the artifact and
	// Objects when `open` loads it.
	request := asFieldMap(artifact["request"])
	response, _ := artifact["response"].(*model.Object)

	commands, _ := request["commands"].([]any)
	lines := []string{
		"Prepared command",
		"  schema:       " + stringField(artifact, "schema"),
		"  endpoint:     " + orDash(stringField(artifact, "sourceUrl")),
		"  committed:    no",
		"  command id:   " + orDash(stringField(request, "commandId")),
		"  act-as:       " + orDash(joinField(request, "actAs")),
		"  read-as:      " + orDash(joinField(request, "readAs")),
		fmt.Sprintf("  commands:     %d", len(commands)),
	}
	if response != nil {
		if hash := model.ObjectString(response, "preparedTransactionHash"); hash != "" {
			lines = append(lines, "  prepared hash:"+Short(hash, 80))
		}
		if version := model.ObjectString(response, "hashingSchemeVersion"); version != "" {
			lines = append(lines, "  hashing:      "+version)
		}
		// Presence is not enough: a null costEstimation means none was
		// returned, and printing the line then is misleading.
		if value, present := response.Get("costEstimation"); present && value != nil {
			lines = append(lines, "  cost:         "+summarizeCost(value))
		}
	}
	if len(commands) > 0 {
		lines = append(lines, "", "Commands")
		for i, command := range commands {
			if normalized := asFieldMap(command); normalized != nil {
				command = normalized
			}
			lines = append(lines, fmt.Sprintf("  [%d] %s", i, formatCommandSummary(command)))
		}
	}

	if response != nil {
		if tx, err := model.PreparedTransactionFromResponse(response); err == nil {
			lines = append(lines, "", "Transaction (as the participant would commit it)")
			lines = append(lines, preparedTreeLines(tx)...)
		}
	}

	lines = append(lines, "", "This is prepared transaction data from a non-committing prepare call.")
	return strings.Join(lines, "\n")
}

// preparedTreeLines draws a prepared transaction with the tree `dpm trace`
// draws a committed one, so the two read the same way.
func preparedTreeLines(tx *model.PreparedTransaction) []string {
	trace := tx.Trace(nil)
	if len(trace.RootEventIDs) == 0 {
		return []string{"  no events"}
	}
	var b strings.Builder
	ctx := NewContext(trace)
	for i, eventID := range trace.RootEventIDs {
		eventTree(&b, trace, eventID, "  ", i == len(trace.RootEventIDs)-1, Color{}, ctx, nil)
	}
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

func stringField(obj map[string]any, key string) string {
	if obj == nil {
		return ""
	}
	value, ok := obj[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func joinField(obj map[string]any, key string) string {
	if obj == nil {
		return ""
	}
	// []string when built in memory, []any when decoded from disk.
	switch values := obj[key].(type) {
	case []string:
		return strings.Join(values, ", ")
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := fieldText(value); text != "" {
				out = append(out, text)
			}
		}
		return strings.Join(out, ", ")
	}
	return ""
}

// PrintSummary writes the header the visualizer opens with.
//
// Parties are rendered through a Context, as everywhere else: the visualizer
// printing a raw 70-character party id while the trace view printed
// "Issuer (Issuer::122036f5...7213a4)" was one tool with two opinions about
// how a party looks. Ports print_summary.
func PrintSummary(w io.Writer, trace *model.Trace) {
	ctx := NewContext(trace)
	fmt.Fprintf(w, "update:      %s\n", trace.UpdateID)
	fmt.Fprintf(w, "source:      %s (%s)\n", trace.Source, orDash(trace.SourceURL))
	fmt.Fprintf(w, "record time: %s\n", orDash(trace.RecordTime))
	fmt.Fprintf(w, "offset:      %s\n", orDash(trace.Offset))
	fmt.Fprintf(w, "synchronizer:%s\n", orDash(trace.SynchronizerID))
	fmt.Fprintf(w, "projection:  %s\n", trace.Projection.Note)
	if len(trace.Projection.ReadAs) > 0 {
		rendered := make([]string, 0, len(trace.Projection.ReadAs))
		for _, party := range trace.Projection.ReadAs {
			rendered = append(rendered, ctx.PartyWithFull(party))
		}
		fmt.Fprintf(w, "read-as:     %s\n", strings.Join(rendered, ", "))
	}
	fmt.Fprintf(w, "events:      %d\n", len(trace.EventsByID))
}

// DebugContextReport states what a trace does and does not contain.
//
// The "not present" half is the point: an artifact is a participant projection,
// and a reader must not mistake absence for evidence. Ports debug_context_report.
func DebugContextReport(trace *model.Trace, index *source.Index) string {
	seen := map[string]bool{}
	var packageIDs []string
	for _, ev := range trace.EventsByID {
		if id := PackageFromTemplate(ev.Template); id != "" && !seen[id] {
			seen[id] = true
			packageIDs = append(packageIDs, id)
		}
	}
	sort.Strings(packageIDs)

	structure := "flat event list"
	if len(trace.RootEventIDs) > 0 {
		structure = "event order and parent/child links"
	}
	present := []string{
		"participant-visible transaction tree",
		structure,
		"choice arguments and create payloads where exposed",
		"party/witness labels where exposed",
	}
	if len(packageIDs) > 0 {
		shown := packageIDs
		if len(shown) > 5 {
			shown = shown[:5]
		}
		present = append(present, "package ids referenced by events: "+strings.Join(shown, ", "))
	}

	missing := []string{
		"full original command envelope unless captured separately",
		"private subtransactions outside this projection",
		"operator logs unless attached separately",
	}

	// Source metadata is the one item here that can be supplied from outside
	// the artifact. Listing it as absent while `s` was rendering a snippet
	// read as a contradiction, so say which of the two is true.
	if index != nil && index.HasSources() {
		present = append(present, "source metadata, loaded from "+strings.Join(index.Roots, ", "))
	} else {
		missing = append([]string{
			"source metadata, unless supplied with --debug-info",
		}, missing...)
	}

	var b strings.Builder
	b.WriteString("\nTrace context assessment\n------------------------\nPresent in this trace:\n")
	for i, item := range present {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- " + item)
	}
	b.WriteString("\n\nNot present in this trace artifact:\n")
	for i, item := range missing {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- " + item)
	}
	return b.String()
}

// asFieldMap reads a nested artifact value as a plain map, whether it arrived
// as one or as an order-preserving Object.
func asFieldMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case *model.Object:
		return typed.ToMap()
	}
	return nil
}

// summarizeCost renders the traffic cost Canton estimated for a prepared
// command. The total is what a reader is deciding on; the request and response
// halves are shown because a surprising total is usually one of the two.
func summarizeCost(value any) string {
	cost := asFieldMap(value)
	if cost == nil {
		return "returned"
	}
	total := fieldText(cost["totalTrafficCostEstimation"])
	if total == "" {
		return "returned"
	}
	request := fieldText(cost["confirmationRequestTrafficCostEstimation"])
	response := fieldText(cost["confirmationResponseTrafficCostEstimation"])
	if request == "" && response == "" {
		return total + " traffic"
	}
	return fmt.Sprintf("%s traffic (request %s, response %s)", total, orDash(request), orDash(response))
}
