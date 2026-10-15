package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/walnuthq/dpm-trace/internal/model"
)

// UpdateComparison writes an update-vs-update comparison.
// Ports print_update_comparison; compact is the default, --full inverts it.
func UpdateComparison(w io.Writer, c *model.Comparison, color Color, compact bool) {
	if !compact {
		updateComparisonFull(w, c, color)
		return
	}

	nValue, nOnlyA, nOnlyB := countEventDiffs(c.LeftRoots, c.RightRoots)
	if total := nValue + nOnlyA + nOnlyB; total > 0 {
		var parts []string
		if nValue > 0 {
			parts = append(parts, fmt.Sprintf("%d value", nValue))
		}
		if nOnlyA > 0 {
			parts = append(parts, fmt.Sprintf("%d only-in-A", nOnlyA))
		}
		if nOnlyB > 0 {
			parts = append(parts, fmt.Sprintf("%d only-in-B", nOnlyB))
		}
		word := "differences"
		if total == 1 {
			word = "difference"
		}
		fmt.Fprintf(w, "%s %d %s (%s)     kind: %s\n",
			color.Apply("✗", "red", "bold"), total, word, strings.Join(parts, ", "), c.Kind)
	} else {
		fmt.Fprintf(w, "%s no differences     kind: %s\n", color.Apply("✓", "green", "bold"), c.Kind)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "  A  update %s  @ offset %s   (baseline)\n",
		Short(orDashValue(c.Left.UpdateID), 12), orDash(c.Left.Offset))
	fmt.Fprintf(w, "  B  update %s  @ offset %s   (candidate)\n",
		Short(orDashValue(c.Right.UpdateID), 12), orDash(c.Right.Offset))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Events")
	printEventRowsCompact(w, c.LeftRoots, c.RightRoots, color, "    ", 50)
	fmt.Fprintln(w)

	readAs := c.Left.ReadAs
	if len(readAs) == 0 {
		readAs = c.Right.ReadAs
	}
	sync := c.Left.RecordTime
	if sync == "" {
		sync = c.Right.RecordTime
	}
	var ctxParts []string
	if len(readAs) > 0 {
		limit := readAs
		if len(limit) > 2 {
			limit = limit[:2]
		}
		rendered := make([]string, 0, len(limit))
		for _, party := range limit {
			rendered = append(rendered, ShortParty(party))
		}
		ctxParts = append(ctxParts, "read-as "+strings.Join(rendered, ", "))
	}
	if sync != "" {
		ctxParts = append(ctxParts, "sync "+sync)
	}
	if len(ctxParts) > 0 {
		fmt.Fprintf(w, "  Context   %s\n", strings.Join(ctxParts, "   "))
	}
}

func updateComparisonFull(w io.Writer, c *model.Comparison, color Color) {
	leftKeys := exactKeys(c.LeftRoots)
	rightKeys := exactKeys(c.RightRoots)
	// Keys carry kind, template, choice and contract id, so two events that
	// match on all four but hold different values compared equal: a changed
	// payload was reported as no visible differences.
	nValue, nOnlyA, nOnlyB := countEventDiffs(c.LeftRoots, c.RightRoots)
	hasDifferences := !equalCounts(c.LeftCounts, c.RightCounts) ||
		!equalStringSlices(leftKeys, rightKeys) ||
		len(c.TemplatesOnlyInLeft) > 0 || len(c.TemplatesOnlyInRight) > 0 ||
		nValue+nOnlyA+nOnlyB > 0

	fmt.Fprintln(w, color.Apply("DPM trace comparison", "bold"))
	fmt.Fprintln(w, "  kind:   "+c.Kind)
	fmt.Fprintf(w, "  result: %s\n", comparisonResult(hasDifferences, color))
	fmt.Fprintln(w)
	fmt.Fprintln(w, color.Apply("Updates", "cyan", "bold"))
	fmt.Fprintf(w, "  baseline:  %s\n", traceCompareSummary(c.Left))
	fmt.Fprintf(w, "  candidate: %s\n", traceCompareSummary(c.Right))
	fmt.Fprintln(w)
	fmt.Fprintln(w, color.Apply("Event counts", "cyan", "bold"))
	PrintCountDiff(w, c.LeftCounts, c.RightCounts, color)
	fmt.Fprintln(w)
	fmt.Fprintln(w, color.Apply("Root events", "cyan", "bold"))
	printEventDiff(w, c.LeftRoots, c.RightRoots, updateSides, color)
	if len(c.TemplatesOnlyInLeft) > 0 || len(c.TemplatesOnlyInRight) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, color.Apply("Template differences", "cyan", "bold"))
		printTemplateList(w, "only in baseline", c.TemplatesOnlyInLeft)
		printTemplateList(w, "only in candidate", c.TemplatesOnlyInRight)
	}
}

// compactEventLabel ports _compact_event_label.
func compactEventLabel(row model.CompareRow) string {
	kind := row.Kind
	if kind == "" {
		kind = model.KindEvent
	}
	template := ShortTemplate(row.Template)
	if template == "" {
		template = "-"
	}
	if row.Choice != "" {
		return kind + " " + template + "." + row.Choice
	}
	return kind + " " + template
}

// changedFields lists every field whose value differs, as "key: a → b". A
// payload that changed in three places was reported as one, because reading
// only the first difference makes the rest invisible -- and which fields moved
// is the question a comparison exists to answer.
func changedFields(lv, rv any) []string {
	left, leftOK := lv.(*model.Object)
	right, rightOK := rv.(*model.Object)
	if !leftOK || !rightOK {
		return nil
	}
	keys := map[string]bool{}
	for _, key := range left.Keys() {
		keys[key] = true
	}
	for _, key := range right.Keys() {
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)

	var changed []string
	for _, key := range sorted {
		a, aOK := left.Get(key)
		b, bOK := right.Get(key)
		if comparableEqual(a, b) {
			continue
		}
		changed = append(changed, fmt.Sprintf("%s: %s → %s",
			key, compactValueOrMissing(a, aOK), compactValueOrMissing(b, bOK)))
	}
	return changed
}

// changedRowFields lists every difference between two matched events: the
// value, and the fields CompareRow has always carried but nothing compared --
// the exercise result, the stakeholders, and reassignment metadata. Carrying a
// field without diffing it means a transaction can change in ways the
// comparison calls identical.
func changedRowFields(left, right model.CompareRow) []string {
	changed := changedFields(left.Value, right.Value)

	if !comparableEqual(left.Result, right.Result) {
		changed = append(changed, fmt.Sprintf("result: %s → %s",
			compactValue(left.Result), compactValue(right.Result)))
	}
	for _, party := range []struct {
		label       string
		left, right []string
	}{
		{"signatories", left.Signatories, right.Signatories},
		{"observers", left.Observers, right.Observers},
		{"witnesses", left.Witnesses, right.Witnesses},
		{"acting", left.ActingParties, right.ActingParties},
	} {
		if !equalStringSlices(party.left, party.right) {
			changed = append(changed, fmt.Sprintf("%s: %s → %s",
				party.label, partyListSummary(party.left), partyListSummary(party.right)))
		}
	}
	for _, field := range []struct {
		label       string
		left, right string
	}{
		{"source synchronizer", left.SourceSynchronizer, right.SourceSynchronizer},
		{"target synchronizer", left.TargetSynchronizer, right.TargetSynchronizer},
	} {
		if field.left != field.right {
			changed = append(changed, fmt.Sprintf("%s: %s → %s",
				field.label, orDash(Short(field.left, 40)), orDash(Short(field.right, 40))))
		}
	}
	if !equalCounters(left.ReassignmentCounter, right.ReassignmentCounter) {
		changed = append(changed, fmt.Sprintf("reassignment counter: %s → %s",
			counterText(left.ReassignmentCounter), counterText(right.ReassignmentCounter)))
	}
	return changed
}

func equalCounters(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func counterText(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

// eventValueAnnotation is the one-line form, for the compact view.
func eventValueAnnotation(lv, rv any) string {
	changed := changedFields(lv, rv)
	if len(changed) == 0 {
		return "values differ"
	}
	return strings.Join(changed, ", ")
}

func rowAnnotation(left, right model.CompareRow) string {
	changed := changedRowFields(left, right)
	if len(changed) == 0 {
		return "values differ"
	}
	return strings.Join(changed, ", ")
}

func countEventDiffs(left, right []model.CompareRow) (nValue, nOnlyA, nOnlyB int) {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		lr, lok := rowAt(left, i)
		rr, rok := rowAt(right, i)
		switch {
		case lok && rok:
			if compareKey(lr) == compareKey(rr) {
				if len(changedRowFields(lr, rr)) > 0 {
					nValue++
				}
				// Children carry the rest of the transaction. Counting only
				// roots let a consuming exercise whose child create changed be
				// summarised as "no differences" while the printer, which does
				// recurse, listed that very difference underneath.
				cValue, cOnlyA, cOnlyB := countEventDiffs(lr.Children, rr.Children)
				nValue += cValue
				nOnlyA += cOnlyA
				nOnlyB += cOnlyB
			} else {
				nOnlyA++
				nOnlyB++
			}
		case lok:
			nOnlyA++
		default:
			nOnlyB++
		}
	}
	return
}

func printEventRowsCompact(w io.Writer, left, right []model.CompareRow, color Color, indent string, col int) {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		lr, lok := rowAt(left, i)
		rr, rok := rowAt(right, i)
		printEventRowCompact(w, lr, lok, rr, rok, color, indent, col)
	}
}

func printEventRowCompact(w io.Writer, lr model.CompareRow, lok bool, rr model.CompareRow, rok bool, color Color, indent string, col int) {
	switch {
	case lok && rok:
		if compareKey(lr) == compareKey(rr) {
			if len(changedRowFields(lr, rr)) == 0 {
				annotation := ""
				if obj, ok := lr.Value.(*model.Object); ok && obj.Len() > 0 {
					annotation = fieldsMatchLabel(obj.Len())
				}
				fmt.Fprintf(w, "%s%s %-*s %s\n", indent, color.Apply("=", "green"), col, compactEventLabel(lr), annotation)
			} else {
				fmt.Fprintf(w, "%s%s %-*s %s\n", indent, color.Apply("~", "yellow"), col, compactEventLabel(lr), rowAnnotation(lr, rr))
			}
			if len(lr.Children) > 0 || len(rr.Children) > 0 {
				printEventRowsCompact(w, lr.Children, rr.Children, color, indent+"  ", col)
			}
			return
		}
		fmt.Fprintf(w, "%s%s %-*s only in A\n", indent, color.Apply("-", "red"), col, compactEventLabel(lr))
		for _, child := range lr.Children {
			printEventRowCompact(w, child, true, model.CompareRow{}, false, color, indent+"  ", col)
		}
		fmt.Fprintf(w, "%s%s %-*s only in B\n", indent, color.Apply("+", "green"), col, compactEventLabel(rr))
		for _, child := range rr.Children {
			printEventRowCompact(w, model.CompareRow{}, false, child, true, color, indent+"  ", col)
		}
	case lok:
		fmt.Fprintf(w, "%s%s %-*s only in A\n", indent, color.Apply("-", "red"), col, compactEventLabel(lr))
		for _, child := range lr.Children {
			printEventRowCompact(w, child, true, model.CompareRow{}, false, color, indent+"  ", col)
		}
	case rok:
		fmt.Fprintf(w, "%s%s %-*s only in B\n", indent, color.Apply("+", "green"), col, compactEventLabel(rr))
		for _, child := range rr.Children {
			printEventRowCompact(w, model.CompareRow{}, false, child, true, color, indent+"  ", col)
		}
	}
}

// diffSides names the two sides of a tree diff. The names are padded to the
// same width so the rows they label stay aligned.
type diffSides struct{ a, b string }

var (
	updateSides   = diffSides{"baseline", "candidate"}
	preparedSides = diffSides{"prepared", "committed"}
)

func (s diffSides) width() int {
	if len(s.a) > len(s.b) {
		return len(s.a)
	}
	return len(s.b)
}

func (s diffSides) label(side, suffix string) string {
	return fmt.Sprintf("%-*s", s.width()+len(suffix)+1, side+suffix+":")
}

// printEventDiff ports print_event_diff (the --full root event list).
func printEventDiff(w io.Writer, left, right []model.CompareRow, sides diffSides, color Color) {
	if len(left) == 0 && len(right) == 0 {
		fmt.Fprintln(w, "  no root events")
		return
	}
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		lr, lok := rowAt(left, i)
		rr, rok := rowAt(right, i)
		switch {
		case lok && rok && exactKey(lr) == exactKey(rr):
			// "same" is about this event; a subtree below it can still differ,
			// and saying nothing about that reported a changed transaction as
			// unchanged.
			label := "same"
			if childValue, childOnlyA, childOnlyB := countEventDiffs(lr.Children, rr.Children); childValue+childOnlyA+childOnlyB > 0 {
				label = "same, children differ"
			}
			fmt.Fprintf(w, "  [%d] %-72s %s\n", i, eventRowText(lr), color.Apply(label, "green"))
			printChildDiff(w, lr.Children, rr.Children, sides, color)
		case lok && rok && compareKey(lr) == compareKey(rr):
			fmt.Fprintf(w, "  [%d] %-72s %s\n", i, eventRowText(lr), color.Apply("same shape", "yellow"))
			for _, change := range changedRowFields(lr, rr) {
				fmt.Fprintf(w, "      %s\n", color.Apply(change, "yellow"))
			}
			printChildDiff(w, lr.Children, rr.Children, sides, color)
		case lok && rok:
			fmt.Fprintf(w, "  [%d] %s %s\n", i, sides.label(sides.a, ""), eventRowText(lr))
			fmt.Fprintf(w, "      %s %s\n", sides.label(sides.b, ""), eventRowText(rr))
		case lok:
			fmt.Fprintf(w, "  [%d] %s %s\n", i, sides.label(sides.a, " only"), eventRowText(lr))
		case rok:
			fmt.Fprintf(w, "  [%d] %s %s\n", i, sides.label(sides.b, " only"), eventRowText(rr))
		}
	}
}

func printTemplateList(w io.Writer, label string, templates []string) {
	if len(templates) == 0 {
		fmt.Fprintf(w, "  %s: -\n", label)
		return
	}
	fmt.Fprintf(w, "  %s:\n", label)
	for _, template := range templates {
		short := ShortTemplate(template)
		if short == "" {
			short = template
		}
		fmt.Fprintf(w, "    - %s\n", short)
	}
}

// PrintCountDiff ports print_count_diff. Reassignment rows are suppressed when
// neither side has any, so transaction output is unchanged.
func PrintCountDiff(w io.Writer, left, right map[string]int, color Color) {
	for _, key := range []string{"create", "exercise", "archive", "assign", "unassign", "other"} {
		leftValue := left[key]
		rightValue := 0
		if right != nil {
			rightValue = right[key]
		}
		if (key == "assign" || key == "unassign") && leftValue == 0 && rightValue == 0 {
			continue
		}
		if right == nil {
			fmt.Fprintf(w, "  %-8s %d\n", key, leftValue)
			continue
		}
		fmt.Fprintf(w, "  %-8s %3d -> %-3d %s\n", key, leftValue, rightValue, countMarker(leftValue, rightValue, color))
	}
}

func countMarker(left, right int, color Color) string {
	if left == right {
		return color.Apply("same", "green")
	}
	delta := right - left
	if delta > 0 {
		return color.Apply(fmt.Sprintf("+%d", delta), "yellow")
	}
	return color.Apply(fmt.Sprintf("%d", delta), "yellow")
}

func comparisonResult(hasDifferences bool, color Color) string {
	if hasDifferences {
		return color.Apply("visible differences found", "yellow", "bold")
	}
	return color.Apply("no visible differences", "green", "bold")
}

func traceCompareSummary(s model.TraceSummary) string {
	pieces := []string{Short(orDashValue(s.UpdateID), 36)}
	if s.Offset != "" {
		pieces = append(pieces, "offset "+s.Offset)
	}
	pieces = append(pieces, fmt.Sprintf("%d events", s.Events))
	if len(s.ReadAs) > 0 {
		rendered := make([]string, 0, len(s.ReadAs))
		for _, party := range s.ReadAs {
			rendered = append(rendered, ShortParty(party))
		}
		pieces = append(pieces, "read-as "+strings.Join(rendered, ", "))
	}
	return strings.Join(pieces, ", ")
}

// eventRowText ports event_row_text.
func eventRowText(row model.CompareRow) string {
	label := EventKindLabel(row.Kind)
	template := ShortTemplate(row.Template)
	if template == "" {
		template = "-"
	}
	if row.Choice != "" {
		template = template + "." + row.Choice
	}
	suffix := ""
	if row.ContractID != "" {
		// "-" is the placeholder shortValue stores for an absent id, and it is
		// still printed: a row reading "(-)" says the id was absent, where no
		// suffix at all reads as "not applicable".
		suffix = " (" + row.ContractID + ")"
	}
	return label + " " + template + suffix
}

func compareKey(row model.CompareRow) string {
	return row.Kind + "\x00" + ShortTemplate(row.Template) + "\x00" + row.Choice
}

func exactKey(row model.CompareRow) string {
	return compareKey(row) + "\x00" + row.ContractID
}

func exactKeys(rows []model.CompareRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, exactKey(row))
	}
	return out
}

func rowAt(rows []model.CompareRow, i int) (model.CompareRow, bool) {
	if i < len(rows) {
		return rows[i], true
	}
	return model.CompareRow{}, false
}

// comparableEqual ports comparable_value's equality: numbers and strings
// compare as strings, so 1 and "1" are the same value.
func comparableEqual(a, b any) bool {
	encodedA, errA := model.Encode(comparableValue(a))
	encodedB, errB := model.Encode(comparableValue(b))
	if errA != nil || errB != nil {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	return string(encodedA) == string(encodedB)
}

func comparableValue(value any) any {
	switch v := value.(type) {
	case *model.Object:
		out := model.NewObject()
		keys := v.Keys()
		sort.Strings(keys)
		for _, key := range keys {
			item, _ := v.Get(key)
			out.Set(key, comparableValue(item))
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, comparableValue(item))
		}
		return out
	case nil:
		return nil
	case bool:
		return v
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// compactValue ports compact_value.
func compactValue(value any) string {
	if s, ok := value.(string); ok {
		return Short(s, 80)
	}
	encoded, err := compactSorted(value)
	if err != nil {
		return Short(fmt.Sprint(value), 120)
	}
	return Short(encoded, 120)
}

func compactValueOrMissing(value any, present bool) string {
	if !present {
		return compactValue("<missing>")
	}
	// A party id is the value most likely to appear in a diff and the longest
	// thing that can: printed whole, two of them push the change off the line.
	if text, ok := value.(string); ok {
		if _, _, isParty := SplitPartyID(text); isParty {
			return ShortParty(text)
		}
	}
	return compactValue(value)
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// printChildDiff walks the subtree below a matched event, so a difference that
// lives in a child is visible where the parent is reported.
func printChildDiff(w io.Writer, left, right []model.CompareRow, sides diffSides, color Color) {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		lr, lok := rowAt(left, i)
		rr, rok := rowAt(right, i)
		switch {
		case lok && rok && compareKey(lr) == compareKey(rr):
			for _, change := range changedRowFields(lr, rr) {
				fmt.Fprintf(w, "      %s %s\n", eventRowText(lr), color.Apply(change, "yellow"))
			}
			printChildDiff(w, lr.Children, rr.Children, sides, color)
		case lok && rok:
			fmt.Fprintf(w, "      %s %s\n", sides.label(sides.a, ""), eventRowText(lr))
			fmt.Fprintf(w, "      %s %s\n", sides.label(sides.b, ""), eventRowText(rr))
		case lok:
			fmt.Fprintf(w, "      %s %s\n", eventRowText(lr), color.Apply(sides.a+" only", "yellow"))
		case rok:
			fmt.Fprintf(w, "      %s %s\n", eventRowText(rr), color.Apply(sides.b+" only", "yellow"))
		}
	}
}

func fieldsMatchLabel(n int) string {
	if n == 1 {
		return "(1 field matches)"
	}
	return fmt.Sprintf("(%d fields match)", n)
}
