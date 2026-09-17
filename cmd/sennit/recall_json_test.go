package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
)

func at(t *testing.T, stamp string) record.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	return record.At(parsed)
}

func id(t *testing.T, hex string) record.ID {
	t.Helper()
	parsed, err := record.ParseID(hex)
	if err != nil {
		t.Fatalf("parse id %q: %v", hex, err)
	}
	return parsed
}

// --json carries every result in full, in the shape approved for it.
func TestTheJSONAnswer(t *testing.T) {
	memory := func(row hit, created string, score, similarity float32) recall.Hit {
		return recall.Hit{
			Found: recall.Found{Memory: &record.Memory{
				ID: id(t, row.id), Kind: record.KindMemory, Type: record.Type(row.typ),
				Statement: row.text, Context: row.context, Tags: row.tags, CreatedAt: at(t, created),
			}},
			Score: score, Similarity: similarity, Tier: recall.TierLocal,
		}
	}
	hits := []recall.Hit{
		memory(progressHit, "2026-09-16T21:50:03.114Z", 0.0328, 0.5642),
		memory(grantHit, "2026-09-02T10:11:45.902Z", 0.0323, 0.4607),
		memory(tmpdirHit, "2026-09-12T08:31:20.377Z", 0.0317, 0.4357),
	}
	encoded, err := recallJSON(recall.Result{
		Hits: hits, Searched: 1284, Considered: 100, LexicalHits: 0,
		EmbedFor: 200 * time.Millisecond, SearchFor: 8 * time.Millisecond,
	}, hits)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got, want := string(encoded)+"\n", fixture(t, "recall-json.txt"); got != want {
		t.Errorf("json:\n got %q\nwant %q", got, want)
	}
}

// One contract, not two: the JSON a script reads here and the answer an agent
// gets over the protocol have the same field names, types and options. They are
// separate types because the command line does not link a protocol server into
// itself, and this is what keeps them from drifting.
func TestTheJSONShapeMatchesTheProtocolAnswer(t *testing.T) {
	for _, pair := range []struct{ mine, theirs any }{
		{recallOut{}, mcp.RecallOut{}},
		{hitOut{}, mcp.HitOut{}},
		{memoryDetail{}, mcp.MemoryDetail{}},
	} {
		compareShapes(t, reflect.TypeOf(pair.mine), reflect.TypeOf(pair.theirs))
	}
	// A conversation carries more over the protocol than a search result needs,
	// so the fields shared with it must still agree.
	mineBySession := fieldsOf(reflect.TypeOf(sessionDetail{}))
	theirs := fieldsOf(reflect.TypeOf(mcp.SessionDetail{}))
	for name, tag := range mineBySession {
		if theirs[name] != tag {
			t.Errorf("a conversation's %s field is %q here and %q over the protocol", name, tag, theirs[name])
		}
	}
	if uri := uriOf(record.KindMemory, id(t, "4083dbdd9e9f0f816c797080eed61fba")); uri != mcp.URI(record.KindMemory, id(t, "4083dbdd9e9f0f816c797080eed61fba")) {
		t.Errorf("a record is addressed as %q here and differently over the protocol", uri)
	}
}

func compareShapes(t *testing.T, mine, theirs reflect.Type) {
	t.Helper()
	if mine.NumField() != theirs.NumField() {
		t.Errorf("%s has %d fields and %s has %d", mine.Name(), mine.NumField(), theirs.Name(), theirs.NumField())
		return
	}
	for i := range mine.NumField() {
		a, b := mine.Field(i), theirs.Field(i)
		if a.Tag.Get("json") != b.Tag.Get("json") {
			t.Errorf("%s field %d: %q here, %q over the protocol", mine.Name(), i, a.Tag.Get("json"), b.Tag.Get("json"))
		}
		if a.Type.Kind() != b.Type.Kind() {
			t.Errorf("%s.%s is %s here and %s over the protocol", mine.Name(), a.Name, a.Type, b.Type)
		}
	}
}

func fieldsOf(t reflect.Type) map[string]string {
	out := make(map[string]string, t.NumField())
	for i := range t.NumField() {
		out[t.Field(i).Name] = t.Field(i).Tag.Get("json")
	}
	return out
}
