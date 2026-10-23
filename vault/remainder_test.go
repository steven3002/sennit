package vault

import (
	"testing"

	"github.com/steven3002/sennit/record"
)

// remainderOf reads what this device holds under id.
func remainderOf(t *testing.T, v *Vault, id record.ID) Remainder {
	t.Helper()
	held, err := v.Remainder(id)
	if err != nil {
		t.Fatalf("read what is left under %s: %v", id, err)
	}
	return held
}

// A remainder names each part of a memory still on this device, at every step a
// remember or a forget can stop at, and is empty once nothing of the memory is
// left.
//
// A remember writes the body, the vector and the ranking rows in that order, and
// a forget removes them in the same order once the catalog entry has gone, so
// stopping between two steps leaves one of the states below. The parts are taken
// away here as that forget takes them. A vector on its own is left when a
// remember stopped between its vector and its ranking rows, and the body went
// afterwards.
func TestARemainderNamesEachPartOfAMemoryStillOnTheDevice(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	never, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	if held := remainderOf(t, v, never); !held.Empty() || held != (Remainder{}) {
		t.Errorf("an id this vault never held leaves %+v, want nothing", held)
	}

	id := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	steps := []struct {
		name string
		take func() error
		want Remainder
	}{
		{name: "stored whole", take: func() error { return nil },
			want: Remainder{Kind: record.KindMemory, Body: true, Rows: true, Vector: true}},
		{name: "the body taken", take: func() error { return v.local.ForgetBody(id) },
			want: Remainder{Kind: record.KindMemory, Rows: true, Vector: true}},
		{name: "the vector taken", take: func() error { return v.removeVector(id) },
			want: Remainder{Kind: record.KindMemory, Rows: true}},
		{name: "the ranking rows taken", take: func() error { return v.local.ForgetRankingMeta(id) },
			want: Remainder{}},
	}
	for _, step := range steps {
		if err := step.take(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		held := remainderOf(t, v, id)
		if held != step.want {
			t.Errorf("%s, what is left is %+v, want %+v", step.name, held, step.want)
		}
		if held.Empty() != (step.want == Remainder{}) {
			t.Errorf("%s, Empty reports %v", step.name, held.Empty())
		}
	}

	alone := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	if err := v.local.ForgetBody(alone); err != nil {
		t.Fatalf("take the body: %v", err)
	}
	if err := v.local.ForgetRankingMeta(alone); err != nil {
		t.Fatalf("take the ranking rows: %v", err)
	}
	if held := remainderOf(t, v, alone); held != (Remainder{Vector: true}) || held.Empty() {
		t.Errorf("a vector on its own leaves %+v, want the vector and no class", held)
	}

	forgotten := rememberFact(t, v, "The east gauge at Tidepool reads in metres.", "east")
	if err := v.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if held := remainderOf(t, v, forgotten); !held.Empty() {
		t.Errorf("a memory Forget removed leaves %+v, want nothing", held)
	}
}

// A remainder says when what is held under an id belongs to a conversation, or
// to a piece of a conversation's transcript, rather than to a memory.
//
// The parts are shared between the classes: a conversation has ranking rows and
// a vector as a memory does, and a transcript chunk has a body. The head is what
// makes a conversation one, so it decides the class while it is here, after the
// forget that stops before it has taken the rest.
func TestARemainderSaysWhenAnIDBelongsToAConversationOrItsTranscript(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	saved := saveExchange(t, v, "Tidepool gauge survey", "survey")
	if held := remainderOf(t, v, saved.ID); held != (Remainder{Kind: record.KindSession, Head: true, Rows: true,
		Vector: true}) {
		t.Errorf("a conversation leaves %+v, want its head, its ranking rows and its vector", held)
	}
	chunk := saved.Chunks[0].ID
	if held := remainderOf(t, v, chunk); held != (Remainder{Kind: record.KindChunk, Body: true}) {
		t.Errorf("a transcript chunk leaves %+v, want its body, as a chunk", held)
	}

	if err := v.removeVector(saved.ID); err != nil {
		t.Fatalf("take the conversation's vector: %v", err)
	}
	if err := v.local.ForgetRankingMeta(saved.ID); err != nil {
		t.Fatalf("take the conversation's ranking rows: %v", err)
	}
	if held := remainderOf(t, v, saved.ID); held != (Remainder{Kind: record.KindSession, Head: true}) {
		t.Errorf("a conversation down to its head leaves %+v, want the head, as a conversation", held)
	}
}
