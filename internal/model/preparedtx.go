package model

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// PreparedTransaction is the transaction a participant computed for a prepare
// request: every node it would commit, children included. It is decoded from
// the artifact's response.preparedTransaction, a base64 protobuf
// com.daml.ledger.api.v2.interactive.PreparedTransaction. Only the fields a
// comparison needs are read; the rest are skipped.
type PreparedTransaction struct {
	Version        string
	Roots          []string
	Nodes          map[string]*PreparedNode
	CommandID      string
	ActAs          []string
	SynchronizerID string
}

// PreparedNode is one transaction.v1.Node.
type PreparedNode struct {
	ID              string
	Kind            string
	Template        string
	ContractID      string
	Choice          string
	Consuming       bool
	Argument        any
	Result          any
	Signatories     []string
	Stakeholders    []string
	ActingParties   []string
	ChoiceObservers []string
	Children        []string
}

// Node kinds a prepared transaction has and a committed one does not report.
const (
	PreparedFetch      = "fetch"
	PreparedRollback   = "rollback"
	PreparedQueryByKey = "query-by-key"
)

// maxValueDepth bounds recursion on a hostile or corrupt payload. Daml-LF
// itself caps value nesting at 100.
const maxValueDepth = 128

// PreparedTransactionOf decodes the prepared transaction a prepared artifact
// carries.
func PreparedTransactionOf(artifact *Object) (*PreparedTransaction, error) {
	response, _ := pickObject(artifact, "response")
	return PreparedTransactionFromResponse(response)
}

// PreparedTransactionFromResponse decodes the prepared transaction in a
// prepare response.
func PreparedTransactionFromResponse(response *Object) (*PreparedTransaction, error) {
	encoded := pickString(response, "preparedTransaction")
	if encoded == "" {
		return nil, errors.New("the prepared artifact carries no preparedTransaction")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("preparedTransaction is not base64: %w", err)
	}
	tx, err := DecodePreparedTransaction(data)
	if err != nil {
		return nil, fmt.Errorf("cannot decode preparedTransaction: %w", err)
	}
	return tx, nil
}

// DecodePreparedTransaction decodes an encoded PreparedTransaction.
func DecodePreparedTransaction(data []byte) (*PreparedTransaction, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	tx := &PreparedTransaction{Nodes: map[string]*PreparedNode{}}
	haveTransaction := false
	for _, f := range fields {
		switch {
		case f.Num == 1 && f.Type == wireBytes:
			haveTransaction = true
			if err := tx.decodeTransaction(f.Bytes); err != nil {
				return nil, err
			}
		case f.Num == 2 && f.Type == wireBytes:
			if err := tx.decodeMetadata(f.Bytes); err != nil {
				return nil, err
			}
		}
	}
	if !haveTransaction {
		return nil, errors.New("no transaction in the prepared transaction")
	}
	for _, root := range tx.Roots {
		if _, ok := tx.Nodes[root]; !ok {
			return nil, fmt.Errorf("root node %s is missing", root)
		}
	}
	return tx, nil
}

func (tx *PreparedTransaction) decodeTransaction(data []byte) error {
	fields, err := wireFields(data)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 1:
			tx.Version = string(f.Bytes)
		case 2:
			tx.Roots = append(tx.Roots, string(f.Bytes))
		case 3:
			node, err := decodeTransactionNode(f.Bytes)
			if err != nil {
				return err
			}
			if node == nil {
				continue
			}
			if _, duplicate := tx.Nodes[node.ID]; duplicate {
				return fmt.Errorf("node %s appears twice", node.ID)
			}
			tx.Nodes[node.ID] = node
		}
	}
	return nil
}

func (tx *PreparedTransaction) decodeMetadata(data []byte) error {
	fields, err := wireFields(data)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 2:
			info, err := wireFields(f.Bytes)
			if err != nil {
				return err
			}
			for _, g := range info {
				switch {
				case g.Num == 1 && g.Type == wireBytes:
					tx.ActAs = append(tx.ActAs, string(g.Bytes))
				case g.Num == 2 && g.Type == wireBytes:
					tx.CommandID = string(g.Bytes)
				}
			}
		case 3:
			tx.SynchronizerID = string(f.Bytes)
		}
	}
	return nil
}

// decodeTransactionNode decodes a DamlTransaction.Node. A node in a version
// this reader does not know returns nil, and the caller skips it.
func decodeTransactionNode(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	id := ""
	var v1 []byte
	for _, f := range fields {
		switch {
		case f.Num == 1 && f.Type == wireBytes:
			id = string(f.Bytes)
		case f.Num == 1000 && f.Type == wireBytes:
			v1 = f.Bytes
		}
	}
	if v1 == nil {
		return nil, nil
	}
	node, err := decodeNodeV1(v1)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", id, err)
	}
	node.ID = id
	return node, nil
}

func decodeNodeV1(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 1:
			return decodeCreate(f.Bytes)
		case 2:
			return decodeFetch(f.Bytes)
		case 3:
			return decodeExercise(f.Bytes)
		case 4:
			return decodeRollback(f.Bytes)
		case 5:
			return decodeQueryByKey(f.Bytes)
		}
	}
	return nil, errors.New("node has no recognised type")
}

func decodeCreate(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	node := &PreparedNode{Kind: KindCreate}
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 2:
			node.ContractID = string(f.Bytes)
		case 4:
			if node.Template, err = decodeIdentifier(f.Bytes); err != nil {
				return nil, err
			}
		case 5:
			if node.Argument, err = decodeLFValue(f.Bytes, 0); err != nil {
				return nil, err
			}
		case 6:
			node.Signatories = append(node.Signatories, string(f.Bytes))
		case 7:
			node.Stakeholders = append(node.Stakeholders, string(f.Bytes))
		}
	}
	return node, nil
}

func decodeFetch(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	node := &PreparedNode{Kind: PreparedFetch}
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 2:
			node.ContractID = string(f.Bytes)
		case 4:
			if node.Template, err = decodeIdentifier(f.Bytes); err != nil {
				return nil, err
			}
		case 5:
			node.Signatories = append(node.Signatories, string(f.Bytes))
		case 6:
			node.Stakeholders = append(node.Stakeholders, string(f.Bytes))
		case 7:
			node.ActingParties = append(node.ActingParties, string(f.Bytes))
		}
	}
	return node, nil
}

func decodeExercise(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	node := &PreparedNode{Kind: KindExercise}
	for _, f := range fields {
		if f.Num == 11 && f.Type == wireVarint {
			node.Consuming = f.Number != 0
			continue
		}
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 2:
			node.ContractID = string(f.Bytes)
		case 4:
			if node.Template, err = decodeIdentifier(f.Bytes); err != nil {
				return nil, err
			}
		case 5:
			node.Signatories = append(node.Signatories, string(f.Bytes))
		case 6:
			node.Stakeholders = append(node.Stakeholders, string(f.Bytes))
		case 7:
			node.ActingParties = append(node.ActingParties, string(f.Bytes))
		case 9:
			node.Choice = string(f.Bytes)
		case 10:
			if node.Argument, err = decodeLFValue(f.Bytes, 0); err != nil {
				return nil, err
			}
		case 12:
			node.Children = append(node.Children, string(f.Bytes))
		case 13:
			if node.Result, err = decodeLFValue(f.Bytes, 0); err != nil {
				return nil, err
			}
		case 14:
			node.ChoiceObservers = append(node.ChoiceObservers, string(f.Bytes))
		}
	}
	return node, nil
}

func decodeRollback(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	node := &PreparedNode{Kind: PreparedRollback}
	for _, f := range fields {
		if f.Num == 1 && f.Type == wireBytes {
			node.Children = append(node.Children, string(f.Bytes))
		}
	}
	return node, nil
}

func decodeQueryByKey(data []byte) (*PreparedNode, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	node := &PreparedNode{Kind: PreparedQueryByKey}
	for _, f := range fields {
		if f.Num == 3 && f.Type == wireBytes {
			if node.Template, err = decodeIdentifier(f.Bytes); err != nil {
				return nil, err
			}
		}
	}
	return node, nil
}

// decodeIdentifier renders an Identifier the way the JSON Ledger API spells a
// template id: package-id:Module:Entity.
func decodeIdentifier(data []byte) (string, error) {
	fields, err := wireFields(data)
	if err != nil {
		return "", err
	}
	var packageID, module, entity string
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 1:
			packageID = string(f.Bytes)
		case 2:
			module = string(f.Bytes)
		case 3:
			entity = string(f.Bytes)
		}
	}
	return packageID + ":" + module + ":" + entity, nil
}

// decodeLFValue decodes a Value into the JSON shape the JSON Ledger API uses for
// the same value, so a decoded payload compares equal to a committed one:
// records as objects keyed by field label, 64-bit integers as strings, variants
// as {"tag", "value"}, enums as their constructor, and generic maps as
// [key, value] pairs.
func decodeLFValue(data []byte, depth int) (any, error) {
	if depth > maxValueDepth {
		return nil, errors.New("value nested too deeply")
	}
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		switch f.Num {
		case 1:
			return NewObject(), nil
		case 2:
			return f.Number != 0, nil
		case 3:
			return strconv.FormatInt(zigzag(f.Number), 10), nil
		case 4:
			days := int64(int32(f.Number))
			return time.Unix(days*86400, 0).UTC().Format("2006-01-02"), nil
		case 5:
			return time.UnixMicro(int64(f.Number)).UTC().Format(time.RFC3339Nano), nil
		case 6, 7, 8, 9:
			return string(f.Bytes), nil
		case 10:
			return decodeOptional(f.Bytes, depth+1, false)
		case 11:
			return decodeList(f.Bytes, depth+1)
		case 12:
			return decodeTextMap(f.Bytes, depth+1)
		case 13:
			return decodeGenMap(f.Bytes, depth+1)
		case 14:
			return decodeRecord(f.Bytes, depth+1)
		case 15:
			return decodeVariant(f.Bytes, depth+1)
		case 16:
			return decodeEnum(f.Bytes)
		}
	}
	return nil, nil
}

// decodeOptional follows the JSON Ledger API: None is null and Some(v) is v,
// except in a chain of nested Optionals, where every level takes the array
// form -- None is [] and Some(v) is [v]. Some(Some "x") is [["x"]].
//
// A top-level None carries no type, so it cannot tell whether it heads such a
// chain; it decodes as null where the JSON Ledger API reports [].
func decodeOptional(data []byte, depth int, nested bool) (any, error) {
	if depth > maxValueDepth {
		return nil, errors.New("value nested too deeply")
	}
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	var inner []byte
	present := false
	for _, f := range fields {
		if f.Num == 1 && f.Type == wireBytes {
			inner, present = f.Bytes, true
		}
	}
	if !present {
		if nested {
			return []any{}, nil
		}
		return nil, nil
	}

	var value any
	if optional, isOptional, err := optionalPayload(inner); err != nil {
		return nil, err
	} else if isOptional {
		value, err = decodeOptional(optional, depth+1, true)
		if err != nil {
			return nil, err
		}
		return []any{value}, nil
	} else {
		value, err = decodeLFValue(inner, depth+1)
		if err != nil {
			return nil, err
		}
	}
	if nested {
		return []any{value}, nil
	}
	return value, nil
}

// optionalPayload reports whether an encoded Value is itself an Optional.
func optionalPayload(data []byte) ([]byte, bool, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, false, err
	}
	for _, f := range fields {
		if f.Num == 10 && f.Type == wireBytes {
			return f.Bytes, true, nil
		}
	}
	return nil, false, nil
}

func decodeList(data []byte, depth int) (any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, f := range fields {
		if f.Num == 1 && f.Type == wireBytes {
			value, err := decodeLFValue(f.Bytes, depth)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
	}
	return out, nil
}

func decodeTextMap(data []byte, depth int) (any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	out := NewObject()
	for _, f := range fields {
		if f.Num != 1 || f.Type != wireBytes {
			continue
		}
		key, value, err := decodeEntry(f.Bytes, depth, false)
		if err != nil {
			return nil, err
		}
		out.Set(key.(string), value)
	}
	return out, nil
}

func decodeGenMap(data []byte, depth int) (any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, f := range fields {
		if f.Num != 1 || f.Type != wireBytes {
			continue
		}
		key, value, err := decodeEntry(f.Bytes, depth, true)
		if err != nil {
			return nil, err
		}
		out = append(out, []any{key, value})
	}
	return out, nil
}

// decodeEntry decodes a map entry: a text key for a TextMap, a Value key for a
// GenMap.
func decodeEntry(data []byte, depth int, valueKey bool) (any, any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, nil, err
	}
	var key, value any = "", nil
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 1:
			if !valueKey {
				key = string(f.Bytes)
			} else if key, err = decodeLFValue(f.Bytes, depth); err != nil {
				return nil, nil, err
			}
		case 2:
			if value, err = decodeLFValue(f.Bytes, depth); err != nil {
				return nil, nil, err
			}
		}
	}
	return key, value, nil
}

func decodeRecord(data []byte, depth int) (any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	out := NewObject()
	position := 0
	for _, f := range fields {
		if f.Num != 2 || f.Type != wireBytes {
			continue
		}
		position++
		entry, err := wireFields(f.Bytes)
		if err != nil {
			return nil, err
		}
		label := ""
		var value any
		for _, g := range entry {
			if g.Type != wireBytes {
				continue
			}
			switch g.Num {
			case 1:
				label = string(g.Bytes)
			case 2:
				if value, err = decodeLFValue(g.Bytes, depth); err != nil {
					return nil, err
				}
			}
		}
		if label == "" {
			label = strconv.Itoa(position)
		}
		out.Set(label, value)
	}
	return out, nil
}

func decodeVariant(data []byte, depth int) (any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	out := NewObject()
	var value any
	tag := ""
	for _, f := range fields {
		if f.Type != wireBytes {
			continue
		}
		switch f.Num {
		case 2:
			tag = string(f.Bytes)
		case 3:
			if value, err = decodeLFValue(f.Bytes, depth); err != nil {
				return nil, err
			}
		}
	}
	out.Set("tag", tag)
	out.Set("value", value)
	return out, nil
}

func decodeEnum(data []byte) (any, error) {
	fields, err := wireFields(data)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		if f.Num == 2 && f.Type == wireBytes {
			return string(f.Bytes), nil
		}
	}
	return "", nil
}

// Trace projects the prepared transaction the way the Ledger API projects a
// committed one for readAs, so the two can be compared event for event.
//
// A committed transaction reports creates and exercises only: fetches and
// key lookups are not events, and a rolled-back subtree never took effect. A
// node is visible when a readAs party is one of its witnesses -- an informee of
// the node or of any ancestor -- and a visible node's descendants are visible
// with it. Below an invisible node, the visible descendants move up to take its
// place. With no readAs parties, every node is visible and none has witnesses.
func (tx *PreparedTransaction) Trace(readAs []string) *Trace {
	trace := &Trace{
		Source:         "prepared-transaction",
		Projection:     Projection{Source: "prepared-transaction", ReadAs: readAs},
		RootEventIDs:   []string{},
		EventsByID:     map[string]*Event{},
		SynchronizerID: tx.SynchronizerID,
	}
	visited := map[string]bool{}

	var walk func(ids []string, ancestors map[string]bool) []string
	walk = func(ids []string, ancestors map[string]bool) []string {
		visible := []string{}
		for _, id := range ids {
			node, ok := tx.Nodes[id]
			if !ok || visited[id] {
				continue
			}
			visited[id] = true
			if node.Kind != KindCreate && node.Kind != KindExercise {
				continue
			}

			informees := make(map[string]bool, len(ancestors))
			for party := range ancestors {
				informees[party] = true
			}
			for _, party := range node.informees() {
				informees[party] = true
			}
			children := walk(node.Children, informees)

			var witnesses []string
			for _, party := range readAs {
				if informees[party] {
					witnesses = append(witnesses, party)
				}
			}
			if len(readAs) > 0 && len(witnesses) == 0 {
				visible = append(visible, children...)
				continue
			}
			trace.EventsByID[id] = node.event(witnesses, children)
			visible = append(visible, id)
		}
		return visible
	}
	trace.RootEventIDs = walk(tx.Roots, map[string]bool{})
	return trace
}

// informees are the parties a node is disclosed to: the stakeholders of a
// create; for an exercise, the actors and choice observers plus the
// stakeholders when consuming or the signatories when not.
func (n *PreparedNode) informees() []string {
	switch n.Kind {
	case KindCreate:
		return append(append([]string{}, n.Signatories...), n.Stakeholders...)
	case KindExercise:
		parties := append(append([]string{}, n.ActingParties...), n.ChoiceObservers...)
		if n.Consuming {
			return append(parties, n.Stakeholders...)
		}
		return append(parties, n.Signatories...)
	}
	return nil
}

// event shapes a node as the Ledger API reports it: a create carries its
// signatories and observers, an exercise neither.
func (n *PreparedNode) event(witnesses, children []string) *Event {
	event := &Event{
		EventID:       n.ID,
		Kind:          n.Kind,
		Template:      n.Template,
		ContractID:    n.ContractID,
		Witnesses:     witnesses,
		ChildEventIDs: children,
	}
	switch n.Kind {
	case KindCreate:
		event.Payload = n.Argument
		event.Signatories = n.Signatories
		signatory := map[string]bool{}
		for _, party := range n.Signatories {
			signatory[party] = true
		}
		for _, party := range n.Stakeholders {
			if !signatory[party] {
				event.Observers = append(event.Observers, party)
			}
		}
	case KindExercise:
		consuming := n.Consuming
		event.Choice = n.Choice
		event.Consuming = &consuming
		event.ActingParties = n.ActingParties
		event.Argument = n.Argument
		event.Result = n.Result
	}
	return event
}
