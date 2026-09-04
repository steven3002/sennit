package record_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steven3002/sennit/record"
)

func valid(t *testing.T) *record.Memory {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	now := record.Now()
	return &record.Memory{
		ID:            id,
		Kind:          record.KindMemory,
		Type:          record.TypeFact,
		SchemaVersion: record.SchemaVersion,
		Version:       1,
		Statement:     "A Sia slab bills a full forty mebibytes however little it holds.",
		Context:       "Measured against live indexer accounting.",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func TestValidateAcceptsAWellFormedMemory(t *testing.T) {
	if err := valid(t).Validate(); err != nil {
		t.Fatalf("valid memory rejected: %v", err)
	}
}

func TestValidateRejectsAnUnknownType(t *testing.T) {
	memory := valid(t)
	memory.Type = "decision"
	if err := memory.Validate(); err == nil {
		t.Fatal("a type outside the vocabulary was accepted")
	}
}

// The vocabulary is closed and settled. If this fails, a stored record's type
// may no longer be one the filter that makes recall work can reach.
func TestTypeVocabularyIsComplete(t *testing.T) {
	want := []string{"fact", "preference", "insight", "doc", "profile", "correction"}
	got := record.TypeNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("vocabulary is %v, want %v", got, want)
	}
	for _, name := range want {
		if !record.Type(name).Valid() {
			t.Fatalf("%q is not accepted as a type", name)
		}
	}
}

func TestValidateRejectsAnEmptyStatement(t *testing.T) {
	memory := valid(t)
	memory.Statement = "   "
	if err := memory.Validate(); err == nil {
		t.Fatal("a blank statement was accepted")
	}
}

// Context is rejected at validation rather than defaulted. It is the single
// largest measured contributor to recall quality, and a record is immutable once
// written, so a missing context is unrepairable after the fact.
func TestValidateRejectsAMissingContext(t *testing.T) {
	for _, context := range []string{"", "   ", "\n\t "} {
		memory := valid(t)
		memory.Context = context
		if err := memory.Validate(); err == nil {
			t.Fatalf("a memory with context %q was accepted", context)
		} else if !errors.Is(err, record.ErrInvalid) {
			t.Fatalf("context %q rejected with %v, want an ErrInvalid", context, err)
		}
	}
}

func TestValidateRejectsAnOutOfRangeConfidence(t *testing.T) {
	memory := valid(t)
	memory.Confidence = 1.5
	if err := memory.Validate(); err == nil {
		t.Fatal("a confidence above 1 was accepted")
	}
}

// Context contributes more to retrieval than any model choice, so it must be
// carried into what gets embedded rather than dropped.
func TestIndexTextCarriesContext(t *testing.T) {
	memory := valid(t)
	text := memory.IndexText()
	if !strings.Contains(text, memory.Statement) || !strings.Contains(text, memory.Context) {
		t.Fatalf("index text omits a field: %q", text)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	memory := valid(t)
	memory.Tags = []string{"sia", "storage"}

	encoded, err := record.Marshal(memory)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := record.Unmarshal(encoded)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != memory.ID || decoded.Statement != memory.Statement || decoded.Type != memory.Type {
		t.Fatal("round trip lost a field")
	}
	if !decoded.CreatedAt.Equal(memory.CreatedAt.Time) {
		t.Fatalf("timestamp round-tripped as %s, want %s", decoded.CreatedAt, memory.CreatedAt)
	}
}

// Readers must tolerate fields they do not know, or a record written by a newer
// build becomes unreadable by an older one.
func TestUnmarshalIgnoresUnknownFields(t *testing.T) {
	encoded := []byte(`{"id":"0102030405060708090a0b0c0d0e0f10","kind":"memory","type":"fact",
		"schemaVersion":99,"statement":"s","context":"c","unknownField":{"nested":true}}`)
	decoded, err := record.Unmarshal(encoded)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Statement != "s" {
		t.Fatalf("statement is %q", decoded.Statement)
	}
}
