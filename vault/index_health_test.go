package vault

import (
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

func vectorFrom(t *testing.T, model string, dim int) index.Entry {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	return index.Entry{ID: id, Model: model, Dim: dim, Vector: make([]float32, dim)}
}

// The failure being guarded is silent: a cosine between vectors from two models
// is meaningless but is still a number, so a half-re-embedded vault ranks
// confidently and wrongly. Detection is the whole point, searching only the
// usable subset is already safe, but doing it without saying so is
// indistinguishable from the vault simply having become worse at recall.
func TestAMixedModelIndexIsDetected(t *testing.T) {
	const model, dim = "bge-small-en-v1.5-fp32", 384
	stored := []index.Entry{
		vectorFrom(t, model, dim),
		vectorFrom(t, model, dim),
		vectorFrom(t, "all-MiniLM-L6-v2", dim),
		vectorFrom(t, "nomic-embed-text-v1.5", 768),
	}

	usable, health := classifyVectors(model, dim, stored)

	if len(usable) != 2 {
		t.Fatalf("%d vectors were made searchable, want 2", len(usable))
	}
	if !health.Mixed() {
		t.Fatal("an index built from three models did not report itself as mixed")
	}
	if health.Indexed != 2 {
		t.Fatalf("reported %d indexed, want 2", health.Indexed)
	}
	if health.Stale() != 2 {
		t.Fatalf("reported %d stale, want 2", health.Stale())
	}
	if health.Foreign["all-MiniLM-L6-v2"] != 1 || health.Foreign["nomic-embed-text-v1.5"] != 1 {
		t.Fatalf("foreign models counted as %v", health.Foreign)
	}
	for _, vector := range usable {
		if vector.Model != model {
			t.Fatalf("a %s vector was made searchable in a %s index", vector.Model, model)
		}
	}
}

// A vector carrying the right model name but the wrong width is the same
// failure wearing a different hat, and is refused the same way.
func TestAWrongWidthVectorIsNotSearchable(t *testing.T) {
	const model = "bge-small-en-v1.5-fp32"
	stored := []index.Entry{vectorFrom(t, model, 384), vectorFrom(t, model, 768)}

	usable, health := classifyVectors(model, 384, stored)
	if len(usable) != 1 {
		t.Fatalf("%d vectors were made searchable, want 1", len(usable))
	}
	if !health.Mixed() || health.Stale() != 1 {
		t.Fatalf("a wrong-width vector was not reported: %+v", health)
	}
}

func TestASingleModelIndexIsNotReportedAsMixed(t *testing.T) {
	const model, dim = "bge-small-en-v1.5-fp32", 384
	stored := []index.Entry{vectorFrom(t, model, dim), vectorFrom(t, model, dim)}

	usable, health := classifyVectors(model, dim, stored)
	if len(usable) != 2 || health.Mixed() || health.Stale() != 0 {
		t.Fatalf("a clean index reported %+v", health)
	}
}
