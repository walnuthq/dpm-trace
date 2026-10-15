package model

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

const (
	issuer = "Issuer::122036f58f09b1879fdc99a950478166fd73076d1aab38de51f9aec4282dc17213a4"
	alice  = "Alice::122036f58f09b1879fdc99a950478166fd73076d1aab38de51f9aec4282dc17213a4"
	bob    = "Bob::1220bc0a0061582e9c8b49216c664d89319f440129fd8b2b0304ddcdd5e7f98dd640"
)

// Minimal protobuf encoders, to build messages the participant fixture does
// not cover.
func pbKey(num, wireType int) []byte {
	return binary.AppendUvarint(nil, uint64(num<<3|wireType))
}

func pbBytes(num int, payload []byte) []byte {
	out := append(pbKey(num, wireBytes), binary.AppendUvarint(nil, uint64(len(payload)))...)
	return append(out, payload...)
}

func pbString(num int, s string) []byte { return pbBytes(num, []byte(s)) }

func pbVarint(num int, value uint64) []byte {
	return append(pbKey(num, wireVarint), binary.AppendUvarint(nil, value)...)
}

func pbFixed64(num int, value uint64) []byte {
	return append(pbKey(num, wireFixed64), binary.LittleEndian.AppendUint64(nil, value)...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func loadPreparedFixture(t *testing.T) *PreparedTransaction {
	t.Helper()
	artifact, err := LoadPreparedArtifact("../../examples/transfer.prepared.json")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := PreparedTransactionOf(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// The fixture is a real participant's response to preparing a Transfer, which
// creates the asset anew for the new owner.
func TestDecodePreparedTransaction(t *testing.T) {
	tx := loadPreparedFixture(t)

	if tx.Version != "2.1" || tx.CommandID != "dpm-trace-prepare-04427c1b4565" {
		t.Errorf("version %q, command id %q", tx.Version, tx.CommandID)
	}
	if len(tx.ActAs) != 1 || tx.ActAs[0] != alice {
		t.Errorf("act-as = %v", tx.ActAs)
	}
	if len(tx.Roots) != 1 || tx.Roots[0] != "0" {
		t.Fatalf("roots = %v", tx.Roots)
	}

	exercise := tx.Nodes["0"]
	if exercise.Kind != KindExercise || exercise.Choice != "Transfer" || !exercise.Consuming {
		t.Errorf("root = %+v", exercise)
	}
	if !strings.HasSuffix(exercise.Template, ":Asset:Asset") {
		t.Errorf("template = %q", exercise.Template)
	}
	if len(exercise.Children) != 1 {
		t.Fatalf("children = %v", exercise.Children)
	}

	create := tx.Nodes[exercise.Children[0]]
	if create.Kind != KindCreate {
		t.Fatalf("child = %+v", create)
	}
	payload, ok := create.Argument.(*Object)
	if !ok {
		t.Fatalf("payload = %#v", create.Argument)
	}
	for key, want := range map[string]string{"issuer": issuer, "owner": bob, "name": "SILVER", "quantity": "100"} {
		if got := ObjectString(payload, key); got != want {
			t.Errorf("payload %s = %q, want %q", key, got, want)
		}
	}
	if exercise.Result != create.ContractID {
		t.Errorf("result %v is not the created contract %s", exercise.Result, create.ContractID)
	}
}

// A committed transaction is one party's projection: a reader sees the events
// it witnesses, and the visible descendants of an event it does not see take
// that event's place.
func TestPreparedTraceProjection(t *testing.T) {
	tx := loadPreparedFixture(t)

	full := tx.Trace(nil)
	if len(full.RootEventIDs) != 1 || len(full.EventsByID) != 2 {
		t.Errorf("unprojected: roots %v, %d events", full.RootEventIDs, len(full.EventsByID))
	}

	asIssuer := tx.Trace([]string{issuer})
	root := asIssuer.EventsByID[asIssuer.RootEventIDs[0]]
	if root.Kind != KindExercise || len(root.ChildEventIDs) != 1 {
		t.Fatalf("issuer root = %+v", root)
	}
	if len(root.Witnesses) != 1 || root.Witnesses[0] != issuer {
		t.Errorf("issuer witnesses = %v", root.Witnesses)
	}

	asBob := tx.Trace([]string{bob})
	if len(asBob.RootEventIDs) != 1 {
		t.Fatalf("bob roots = %v", asBob.RootEventIDs)
	}
	if only := asBob.EventsByID[asBob.RootEventIDs[0]]; only.Kind != KindCreate {
		t.Errorf("bob sees %s, want only the create he observes", only.Kind)
	}

	outsider := tx.Trace([]string{"Mallory::1220ff"})
	if len(outsider.RootEventIDs) != 0 {
		t.Errorf("a non-informee sees %v", outsider.RootEventIDs)
	}
}

// Fetches and key lookups are not events in a committed transaction, and a
// rolled-back subtree never took effect.
func TestPreparedTraceDropsWhatIsNeverCommitted(t *testing.T) {
	template := pbBytes(4, concat(pbString(1, "pkg"), pbString(2, "M"), pbString(3, "T")))
	create := func(cid string) []byte {
		return pbBytes(1, concat(pbString(2, cid), template, pbString(6, alice), pbString(7, alice)))
	}
	node := func(id string, v1 []byte) []byte {
		return pbBytes(3, concat(pbString(1, id), pbBytes(1000, v1)))
	}
	transaction := concat(
		pbString(1, "2.1"),
		pbString(2, "fetch"), pbString(2, "rollback"), pbString(2, "kept"),
		node("fetch", pbBytes(2, concat(pbString(2, "00aa"), template, pbString(5, alice)))),
		node("rollback", pbBytes(4, pbString(1, "undone"))),
		node("undone", create("00bb")),
		node("kept", create("00cc")),
	)
	tx, err := DecodePreparedTransaction(pbBytes(1, transaction))
	if err != nil {
		t.Fatal(err)
	}

	trace := tx.Trace([]string{alice})
	if len(trace.RootEventIDs) != 1 || trace.RootEventIDs[0] != "kept" {
		t.Errorf("roots = %v, want only the create that took effect", trace.RootEventIDs)
	}
	if _, ok := trace.EventsByID["undone"]; ok {
		t.Error("a rolled-back create is reported")
	}
}

func TestDecodeLFValueMatchesTheJSONLedgerAPI(t *testing.T) {
	value := func(parts ...[]byte) []byte { return concat(parts...) }
	optional := func(inner []byte) []byte {
		if inner == nil {
			return pbBytes(10, nil)
		}
		return pbBytes(10, pbBytes(1, inner))
	}
	int64Value := func(n int64) []byte { return pbVarint(3, uint64(n<<1)^uint64(n>>63)) }
	record := pbBytes(14, concat(
		pbBytes(2, concat(pbString(1, "flag"), pbBytes(2, pbVarint(2, 1)))),
		pbBytes(2, concat(pbString(1, "count"), pbBytes(2, int64Value(-3)))),
	))

	cases := []struct {
		name  string
		value []byte
		want  string
	}{
		{"unit", pbBytes(1, nil), `{}`},
		{"negative int64", int64Value(-42), `"-42"`},
		{"date", pbVarint(4, 19000), `"2022-01-08"`},
		{"timestamp", pbFixed64(5, 1_700_000_000_123_456), `"2023-11-14T22:13:20.123456Z"`},
		{"numeric", pbString(6, "1.5000000000"), `"1.5000000000"`},
		{"none", optional(nil), `null`},
		{"some", optional(pbString(8, "x")), `"x"`},
		{"some none", optional(optional(nil)), `[[]]`},
		{"some some", optional(optional(pbString(8, "x"))), `[["x"]]`},
		{"some some some", optional(optional(optional(pbString(8, "x")))), `[[["x"]]]`},
		{"list", pbBytes(11, concat(pbBytes(1, pbString(8, "a")), pbBytes(1, pbString(8, "b")))), `["a","b"]`},
		{"text map", pbBytes(12, pbBytes(1, concat(pbString(1, "k"), pbBytes(2, pbString(8, "v"))))), `{"k":"v"}`},
		{"gen map", pbBytes(13, pbBytes(1, concat(pbBytes(1, pbString(8, "k")), pbBytes(2, pbString(8, "v"))))), `[["k","v"]]`},
		{"record", record, `{"count":"-3","flag":true}`},
		{"variant", pbBytes(15, concat(pbString(2, "Some"), pbBytes(3, pbString(8, "v")))), `{"tag":"Some","value":"v"}`},
		{"enum", pbBytes(16, pbString(2, "Red")), `"Red"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeLFValue(value(tc.value), 0)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := EncodeCompact(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tc.want {
				t.Errorf("got %s, want %s", encoded, tc.want)
			}
		})
	}
}

func TestPreparedTransactionRejectsWhatItCannotRead(t *testing.T) {
	artifact := func(response string) *Object {
		obj, err := Decode([]byte(`{"response": ` + response + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return obj
	}
	nested := pbBytes(10, nil)
	for i := 0; i < maxValueDepth+2; i++ {
		nested = pbBytes(10, pbBytes(1, nested))
	}
	deep := pbBytes(1, concat(
		pbString(2, "0"),
		pbBytes(3, concat(pbString(1, "0"), pbBytes(1000, pbBytes(1, pbBytes(5, nested))))),
	))

	cases := []struct {
		name     string
		response string
		want     string
	}{
		{"absent", `{}`, "carries no preparedTransaction"},
		{"not base64", `{"preparedTransaction": "%%%"}`, "not base64"},
		{"truncated", `{"preparedTransaction": "` + base64.StdEncoding.EncodeToString([]byte{0x0a, 0x10, 0x01}) + `"}`, "truncated"},
		{"no transaction", `{"preparedTransaction": "` + base64.StdEncoding.EncodeToString(pbString(2, "")) + `"}`, "no transaction"},
		{"missing root", `{"preparedTransaction": "` + base64.StdEncoding.EncodeToString(pbBytes(1, pbString(2, "7"))) + `"}`, "root node 7 is missing"},
		{"nested too deeply", `{"preparedTransaction": "` + base64.StdEncoding.EncodeToString(deep) + `"}`, "nested too deeply"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PreparedTransactionOf(artifact(tc.response))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// The committed update was captured from the same Transfer; the tampered copy
// changes only the child create's quantity. Comparing commands to root events
// could not see that.
func TestComparePreparedTreeFindsAChildDifference(t *testing.T) {
	prepared, err := LoadPreparedArtifact("../../tests/fixtures/child-diff/prepared.json")
	if err != nil {
		t.Fatal(err)
	}
	load := func(name string) *Trace {
		artifact, err := LoadTraceArtifact("../../tests/fixtures/child-diff/" + name)
		if err != nil {
			t.Fatal(err)
		}
		trace, err := TraceFromArtifact(artifact)
		if err != nil {
			t.Fatal(err)
		}
		return trace
	}

	expected := ComparePreparedToTrace(prepared, load("update-expected.json"))
	if !expected.TreeCompared {
		t.Fatalf("tree not compared: %s", expected.TreeSkipped)
	}
	prepRoot, commRoot := expected.PreparedTree[0], expected.CommittedTree[0]
	if prepRoot.Result != "<created #1>" || commRoot.Result != "<created #1>" {
		t.Errorf("created ids not matched by position: %v vs %v", prepRoot.Result, commRoot.Result)
	}
	if len(prepRoot.Children) != 1 || len(commRoot.Children) != 1 {
		t.Fatalf("children: %d prepared, %d committed", len(prepRoot.Children), len(commRoot.Children))
	}
	for _, field := range []struct {
		name string
		a, b []string
	}{
		{"witnesses", prepRoot.Witnesses, commRoot.Witnesses},
		{"child observers", prepRoot.Children[0].Observers, commRoot.Children[0].Observers},
		{"child signatories", prepRoot.Children[0].Signatories, commRoot.Children[0].Signatories},
	} {
		if strings.Join(field.a, ",") != strings.Join(field.b, ",") {
			t.Errorf("%s: prepared %v, committed %v", field.name, field.a, field.b)
		}
	}

	tampered := ComparePreparedToTrace(prepared, load("update-tampered.json"))
	prepChild := tampered.PreparedTree[0].Children[0].Value.(*Object)
	commChild := tampered.CommittedTree[0].Children[0].Value.(*Object)
	if ObjectString(prepChild, "quantity") != "100" || ObjectString(commChild, "quantity") != "1" {
		t.Errorf("quantity: prepared %q, committed %q", ObjectString(prepChild, "quantity"), ObjectString(commChild, "quantity"))
	}
}

func TestComparePreparedFallsBackWithoutATransaction(t *testing.T) {
	prepared, err := Decode([]byte(`{"request": {"commandId": "c1", "commands": []}, "response": {}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := ComparePreparedToTrace(prepared, &Trace{EventsByID: map[string]*Event{}})
	if c.TreeCompared || !strings.Contains(c.TreeSkipped, "no preparedTransaction") {
		t.Errorf("compared %v, skipped %q", c.TreeCompared, c.TreeSkipped)
	}
}

// An artifact is an arbitrary file read from disk, so no input may panic.
func FuzzDecodePreparedTransaction(f *testing.F) {
	artifact, err := LoadPreparedArtifact("../../examples/transfer.prepared.json")
	if err != nil {
		f.Fatal(err)
	}
	response, _ := pickObject(artifact, "response")
	seed, err := base64.StdEncoding.DecodeString(pickString(response, "preparedTransaction"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if tx, err := DecodePreparedTransaction(data); err == nil {
			tx.Trace([]string{alice})
		}
	})
}
