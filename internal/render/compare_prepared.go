package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/walnuthq/dpm-trace/internal/model"
)

// PreparedUpdateComparison writes a prepared-vs-update comparison.
// Ports print_prepared_update_comparison.
//
// With the prepared transaction decoded, both sides are event trees and every
// event is compared, children included. Without it, only the commands can be
// compared against the root events, and the output says so.
func PreparedUpdateComparison(w io.Writer, c *model.PreparedComparison, color Color, compact bool) {
	var nValue, nOnlyA, nOnlyB int
	var hasDifferences bool
	if c.TreeCompared {
		nValue, nOnlyA, nOnlyB = countEventDiffs(c.PreparedTree, c.CommittedTree)
		hasDifferences = nValue+nOnlyA+nOnlyB > 0
	} else {
		nValue, nOnlyA, nOnlyB = countCommandEventDiffs(c.Commands, c.RootEvents)
		hasDifferences = preparedUpdateHasDifferences(c.Commands, c.RootEvents)
	}

	if !compact {
		preparedUpdateFull(w, c, color, hasDifferences)
		return
	}

	if hasDifferences {
		total := nValue + nOnlyA + nOnlyB
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

	fmt.Fprintf(w, "  A  command %s   (prepared)\n", Short(orDashValue(c.Left.CommandID), 20))
	fmt.Fprintf(w, "  B  update %s  @ offset %s   (committed)\n",
		Short(orDashValue(c.Right.UpdateID), 12), orDash(c.Right.Offset))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Events")
	if c.TreeCompared {
		printEventRowsCompact(w, c.PreparedTree, c.CommittedTree, color, "    ", 50)
	} else {
		printCommandEventRowsCompact(w, c.Commands, c.RootEvents, color, "    ", 50)
		fmt.Fprintln(w, color.Apply("    child events not compared: "+c.TreeSkipped, "dim"))
	}
	fmt.Fprintln(w)

	var ctxParts []string
	if parties := firstTwoParties(c.Left.ActAs); parties != "" {
		ctxParts = append(ctxParts, "act-as "+parties)
	}
	if parties := firstTwoParties(c.Right.ReadAs); parties != "" {
		ctxParts = append(ctxParts, "read-as "+parties)
	}
	if c.Right.RecordTime != "" {
		ctxParts = append(ctxParts, "sync "+c.Right.RecordTime)
	}
	if len(ctxParts) > 0 {
		fmt.Fprintf(w, "  Context   %s\n", strings.Join(ctxParts, "   "))
	}
}

func preparedUpdateFull(w io.Writer, c *model.PreparedComparison, color Color, hasDifferences bool) {
	fmt.Fprintln(w, color.Apply("DPM trace comparison", "bold"))
	fmt.Fprintln(w, "  kind:   "+c.Kind)
	fmt.Fprintf(w, "  result: %s\n", comparisonResult(hasDifferences, color))
	fmt.Fprintln(w)

	fmt.Fprintln(w, color.Apply("Operation", "cyan", "bold"))
	preparedText, committedText := "-", "-"
	var firstCommand model.CommandRow
	var firstRoot model.CompareRow
	haveCommand, haveRoot := false, false
	if len(c.Commands) > 0 {
		firstCommand, haveCommand = c.Commands[0], true
		preparedText = commandRowText(firstCommand)
	}
	if len(c.RootEvents) > 0 {
		firstRoot, haveRoot = c.RootEvents[0], true
		committedText = eventRowText(firstRoot)
	}
	fmt.Fprintf(w, "  prepared:  %s\n", preparedText)
	fmt.Fprintf(w, "  committed: %s\n", committedText)
	fmt.Fprintf(w, "  shape:     %s\n", operationShapeSummary(firstCommand, haveCommand, firstRoot, haveRoot, color))
	fmt.Fprintln(w)

	if c.TreeCompared {
		fmt.Fprintln(w, color.Apply("Event diff", "cyan", "bold"))
		printEventDiff(w, c.PreparedTree, c.CommittedTree, preparedSides, color)
		fmt.Fprintln(w)
	} else {
		preparedFieldDiff(w, c, color)
	}

	fmt.Fprintln(w, color.Apply("Context", "cyan", "bold"))
	fmt.Fprintf(w, "  command id: %s\n", orDash(c.Left.CommandID))
	if c.Left.PreparedTransactionHash != "" {
		fmt.Fprintf(w, "  prep hash:  %s\n", Short(c.Left.PreparedTransactionHash, 80))
	}
	fmt.Fprintf(w, "  update:     %s\n", Short(orDashValue(c.Right.UpdateID), 80))
	fmt.Fprintf(w, "  offset:     %s\n", orDash(c.Right.Offset))
	fmt.Fprintf(w, "  act-as:     %s\n", partyListSummary(c.Left.ActAs))
	fmt.Fprintf(w, "  read-as:    %s\n", partyListSummary(c.Right.ReadAs))
	fmt.Fprintln(w)

	fmt.Fprintln(w, color.Apply("Committed state diff", "cyan", "bold"))
	PrintCountDiff(w, c.StateDiff, nil, color)
}

// preparedFieldDiff is the --full diff when only the commands can be compared:
// each command against the root event at its position.
func preparedFieldDiff(w io.Writer, c *model.PreparedComparison, color Color) {
	fmt.Fprintln(w, color.Apply("Field diff", "cyan", "bold"))
	if len(c.Commands) == 0 && len(c.RootEvents) == 0 {
		fmt.Fprintln(w, "  no commands")
	} else {
		n := len(c.Commands)
		if len(c.RootEvents) > n {
			n = len(c.RootEvents)
		}
		for i := 0; i < n; i++ {
			cmd, cmdOK := commandAt(c.Commands, i)
			root, rootOK := rowAt(c.RootEvents, i)
			switch {
			case cmdOK && rootOK && commandEventKey(cmd) == compareKey(root):
				fmt.Fprintf(w, "  [%d] %s\n", i, commandRowText(cmd))
				printArgumentFieldDiff(w, cmd, root, color)
				printPartyFields(w, root)
			case cmdOK:
				fmt.Fprintf(w, "  [%d] command only: %s\n", i, commandRowText(cmd))
			default:
				fmt.Fprintf(w, "  [%d] event only:   %s\n", i, eventRowText(root))
			}
		}
	}
	fmt.Fprintln(w, color.Apply("  child events not compared: "+c.TreeSkipped, "dim"))
	fmt.Fprintln(w)
}

// preparedUpdateHasDifferences ports prepared_update_has_differences.
func preparedUpdateHasDifferences(commands []model.CommandRow, roots []model.CompareRow) bool {
	if len(commands) != len(roots) {
		return true
	}
	for i := range commands {
		if commandEventKey(commands[i]) != compareKey(roots[i]) {
			return true
		}
		if !comparableEqual(commands[i].Value, roots[i].Value) {
			return true
		}
	}
	return false
}

func countCommandEventDiffs(commands []model.CommandRow, roots []model.CompareRow) (nValue, nOnlyA, nOnlyB int) {
	n := len(commands)
	if len(roots) > n {
		n = len(roots)
	}
	for i := 0; i < n; i++ {
		cmd, cmdOK := commandAt(commands, i)
		root, rootOK := rowAt(roots, i)
		switch {
		case cmdOK && rootOK:
			if commandEventKey(cmd) == compareKey(root) {
				if !comparableEqual(cmd.Value, root.Value) {
					nValue++
				}
			} else {
				nOnlyA++
				nOnlyB++
			}
		case cmdOK:
			nOnlyA++
		default:
			nOnlyB++
		}
	}
	return
}

func printCommandEventRowsCompact(w io.Writer, commands []model.CommandRow, roots []model.CompareRow, color Color, indent string, col int) {
	n := len(commands)
	if len(roots) > n {
		n = len(roots)
	}
	for i := 0; i < n; i++ {
		cmd, cmdOK := commandAt(commands, i)
		root, rootOK := rowAt(roots, i)
		switch {
		case cmdOK && rootOK:
			if commandEventKey(cmd) == compareKey(root) {
				if comparableEqual(cmd.Value, root.Value) {
					annotation := ""
					if obj, ok := cmd.Value.(*model.Object); ok && obj.Len() > 0 {
						annotation = fmt.Sprintf("(%d fields match)", obj.Len())
					}
					fmt.Fprintf(w, "%s%s %-*s %s\n", indent, color.Apply("=", "green"), col, commandCompactLabel(cmd), annotation)
				} else {
					fmt.Fprintf(w, "%s%s %-*s %s\n", indent, color.Apply("~", "yellow"), col, commandCompactLabel(cmd), eventValueAnnotation(cmd.Value, root.Value))
				}
			} else {
				fmt.Fprintf(w, "%s%s %-*s only in A\n", indent, color.Apply("-", "red"), col, commandCompactLabel(cmd))
				fmt.Fprintf(w, "%s%s %-*s only in B\n", indent, color.Apply("+", "green"), col, compactEventLabel(root))
			}
		case cmdOK:
			fmt.Fprintf(w, "%s%s %-*s only in A\n", indent, color.Apply("-", "red"), col, commandCompactLabel(cmd))
		case rootOK:
			fmt.Fprintf(w, "%s%s %-*s only in B\n", indent, color.Apply("+", "green"), col, compactEventLabel(root))
		}
	}
}

// printArgumentFieldDiff ports print_argument_field_diff.
func printArgumentFieldDiff(w io.Writer, cmd model.CommandRow, root model.CompareRow, color Color) {
	label := cmd.ValueLabel
	if label == "" {
		label = "value"
	}
	if cmd.Value == nil && root.Value == nil {
		return
	}
	commandObj, commandIsObject := cmd.Value.(*model.Object)
	rootObj, rootIsObject := root.Value.(*model.Object)
	if !commandIsObject || !rootIsObject {
		if comparableEqual(cmd.Value, root.Value) {
			fmt.Fprintln(w, color.Apply(fmt.Sprintf("    %s: %s match", label, compactValue(cmd.Value)), "dim"))
		} else {
			fmt.Fprintf(w, "    %s:\n", label)
			fmt.Fprintf(w, "      %s  (prepared)\n", color.Apply("< "+compactValue(cmd.Value), "yellow"))
			fmt.Fprintf(w, "      %s  (committed)\n", color.Apply("> "+compactValue(root.Value), "yellow"))
		}
		return
	}

	keys := unionKeys(commandObj, rootObj)
	if len(keys) == 0 {
		return
	}
	var differ, match []string
	for _, key := range keys {
		a, aOK := commandObj.Get(key)
		b, bOK := rootObj.Get(key)
		if comparableEqual(missingOr(a, aOK), missingOr(b, bOK)) {
			match = append(match, key)
		} else {
			differ = append(differ, key)
		}
	}

	fmt.Fprintf(w, "    %s:\n", label)
	if len(differ) > 0 {
		fmt.Fprintf(w, "      %s\n", color.Apply("(differs)", "red"))
		for _, key := range differ {
			a, aOK := commandObj.Get(key)
			b, bOK := rootObj.Get(key)
			fmt.Fprintf(w, "        %s:\n", key)
			fmt.Fprintf(w, "          prepared:  %s\n", color.Apply(compactValue(missingOr(a, aOK)), "yellow"))
			fmt.Fprintf(w, "          committed: %s\n", color.Apply(compactValue(missingOr(b, bOK)), "yellow"))
		}
	}
	if len(match) > 0 {
		fmt.Fprintln(w, color.Apply("      (matches)", "dim"))
		for _, key := range match {
			a, aOK := commandObj.Get(key)
			fmt.Fprintln(w, color.Apply(fmt.Sprintf("        %s: %s", key, compactValue(missingOr(a, aOK))), "dim"))
		}
	}
}

// printPartyFields ports print_party_fields.
func printPartyFields(w io.Writer, root model.CompareRow) {
	if len(root.ActingParties) > 0 {
		fmt.Fprintf(w, "    acting:      %s\n", partyListSummary(root.ActingParties))
	}
	if len(root.Signatories) > 0 {
		fmt.Fprintf(w, "    signatories: %s\n", partyListSummary(root.Signatories))
	}
	if len(root.Observers) > 0 {
		fmt.Fprintf(w, "    observers:   %s\n", partyListSummary(root.Observers))
	}
	if len(root.Witnesses) > 0 {
		fmt.Fprintf(w, "    witnesses:   %s\n", partyListSummary(root.Witnesses))
	}
	if root.SourceSynchronizer != "" || root.TargetSynchronizer != "" {
		fmt.Fprintf(w, "    reassignment:%s -> %s\n", Short(root.SourceSynchronizer, 40), Short(root.TargetSynchronizer, 40))
		if root.ReassignmentCounter != nil {
			fmt.Fprintf(w, "    counter:     %d\n", *root.ReassignmentCounter)
		}
	}
}

func commandRowText(row model.CommandRow) string {
	kind := row.Kind
	if kind == "" {
		kind = "command"
	}
	label := EventKindLabel(kind)
	template := ShortTemplate(row.Template)
	if template == "" {
		template = "-"
	}
	if row.Choice != "" {
		template = template + "." + row.Choice
	}
	suffix := ""
	if row.ContractID != "" {
		suffix = " (" + row.ContractID + ")"
	}
	return label + " " + template + suffix
}

func commandCompactLabel(row model.CommandRow) string {
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

func commandEventKey(row model.CommandRow) string {
	return row.Kind + "\x00" + ShortTemplate(row.Template) + "\x00" + row.Choice
}

func operationShapeSummary(cmd model.CommandRow, haveCommand bool, root model.CompareRow, haveRoot bool, color Color) string {
	if !haveCommand || !haveRoot {
		return color.Apply("different", "yellow")
	}
	if commandEventKey(cmd) == compareKey(root) {
		return color.Apply("matches", "green")
	}
	return color.Apply("different", "yellow")
}

func partyListSummary(parties []string) string {
	if len(parties) == 0 {
		return "-"
	}
	rendered := make([]string, 0, len(parties))
	for _, party := range parties {
		rendered = append(rendered, ShortParty(party))
	}
	return strings.Join(rendered, ", ")
}

func firstTwoParties(parties []string) string {
	if len(parties) == 0 {
		return ""
	}
	limit := parties
	if len(limit) > 2 {
		limit = limit[:2]
	}
	rendered := make([]string, 0, len(limit))
	for _, party := range limit {
		rendered = append(rendered, ShortParty(party))
	}
	return strings.Join(rendered, ", ")
}

func commandAt(rows []model.CommandRow, i int) (model.CommandRow, bool) {
	if i < len(rows) {
		return rows[i], true
	}
	return model.CommandRow{}, false
}

func unionKeys(a, b *model.Object) []string {
	seen := map[string]bool{}
	var keys []string
	for _, key := range append(a.Keys(), b.Keys()...) {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// missingOr mirrors Python's dict.get(key, "<missing>").
func missingOr(value any, present bool) any {
	if !present {
		return "<missing>"
	}
	return value
}
