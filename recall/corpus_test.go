package recall_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/record"
)

// corpusPath is the committed regression fixture. It is wholly invented, see
// testdata/README.md, because it is public and must hold no vault content.
const corpusPath = "testdata/corpus.json"

// The two sub-vaults the corpus is built from. Real vaults are imbalanced, and
// the imbalance is the point: recall on the subject a user works in most is
// measurably worse than on their long tail, and an aggregate hides it.
const (
	shapeDominant = "dominant"
	shapeLongTail = "long-tail"
)

type fixture struct {
	Note    string          `json:"note"`
	Records []fixtureRecord `json:"records"`
	Queries []fixtureQuery  `json:"queries"`
}

type fixtureRecord struct {
	ID         string      `json:"id"`
	Type       record.Type `json:"type"`
	Statement  string      `json:"statement"`
	Context    string      `json:"context"`
	Tags       []string    `json:"tags"`
	Shape      string      `json:"shape"`
	Supersedes string      `json:"supersedes,omitempty"`
}

type fixtureQuery struct {
	ID     string        `json:"id"`
	Query  string        `json:"query"`
	Gold   []string      `json:"gold"`
	Filter fixtureFilter `json:"filter"`
}

type fixtureFilter struct {
	Tags  []string      `json:"tags"`
	Types []record.Type `json:"types"`
}

// Answerable reports whether the query has an answer in the corpus. One query
// deliberately does not: returning nothing is a correct response, and a harness
// that only ever measures queries with answers cannot tell that apart from a
// pipeline that always returns its least-bad guess.
func (q fixtureQuery) Answerable() bool { return len(q.Gold) > 0 }

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(corpusPath))
	if err != nil {
		t.Skipf("regression corpus is absent (%v); skipping", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s: %v", corpusPath, err)
	}
	return f
}

// The corpus is a fixture, so it is checked before anything is measured against
// it. A gold id that does not exist would silently depress every score, and a
// record that the schema would reject could never be stored in the first place.
func TestRegressionCorpusIsWellFormed(t *testing.T) {
	f := loadFixture(t)

	if len(f.Records) == 0 || len(f.Queries) == 0 {
		t.Fatal("the corpus is empty")
	}

	known := make(map[string]fixtureRecord, len(f.Records))
	for _, r := range f.Records {
		if _, dup := known[r.ID]; dup {
			t.Fatalf("duplicate record id %q", r.ID)
		}
		known[r.ID] = r
		if len(r.ID) > record.IDSize {
			t.Fatalf("record handle %q is too long to encode as a record id", r.ID)
		}

		if !r.Type.Valid() {
			t.Fatalf("record %s has type %q, outside the vocabulary", r.ID, r.Type)
		}
		if r.Shape != shapeDominant && r.Shape != shapeLongTail {
			t.Fatalf("record %s has shape %q", r.ID, r.Shape)
		}
		// The corpus has to be storable by the thing it is measuring, and
		// context is mandatory at the schema level.
		memory := &record.Memory{
			ID: fixtureID(t, r.ID), Kind: record.KindMemory, Type: r.Type,
			SchemaVersion: record.SchemaVersion, Version: 1,
			Statement: r.Statement, Context: r.Context, Tags: r.Tags,
			CreatedAt: record.Now(), UpdatedAt: record.Now(),
		}
		if err := memory.Validate(); err != nil {
			t.Fatalf("record %s would be rejected by the schema: %v", r.ID, err)
		}
	}

	for _, r := range f.Records {
		if r.Supersedes == "" {
			continue
		}
		if _, ok := known[r.Supersedes]; !ok {
			t.Fatalf("record %s supersedes %q, which is not in the corpus", r.ID, r.Supersedes)
		}
	}

	var answerable int
	seen := make(map[string]bool, len(f.Queries))
	for _, q := range f.Queries {
		if seen[q.ID] {
			t.Fatalf("duplicate query id %q", q.ID)
		}
		seen[q.ID] = true
		if q.Query == "" {
			t.Fatalf("query %s is empty", q.ID)
		}
		for _, gold := range q.Gold {
			if _, ok := known[gold]; !ok {
				t.Fatalf("query %s names gold record %q, which is not in the corpus", q.ID, gold)
			}
		}
		if q.Answerable() {
			answerable++
		}
		// A filter written from the gold set rather than the query text would
		// make the filtered arm measure nothing, so a superseded record must
		// never be gold: the answer is the record that replaced it.
		for _, gold := range q.Gold {
			for _, r := range f.Records {
				if r.Supersedes == gold {
					t.Fatalf("query %s is gold-labelled with %s, which %s supersedes", q.ID, gold, r.ID)
				}
			}
		}
	}
	if answerable == len(f.Queries) {
		t.Fatal("no unanswerable query: returning nothing is a correct answer and has to be measured")
	}

	both := map[string]int{}
	for _, r := range f.Records {
		both[r.Shape]++
	}
	if both[shapeDominant] == 0 || both[shapeLongTail] == 0 {
		t.Fatalf("the corpus is not imbalanced: %v", both)
	}
	t.Logf("corpus: %d records (%d dominant, %d long-tail), %d queries (%d answerable)",
		len(f.Records), both[shapeDominant], both[shapeLongTail], len(f.Queries), answerable)
}

// handleID turns a corpus handle into a record id deterministically, so a run is
// reproducible and a failure names the same record twice.
//
// A handle longer than an id would collide silently, so the corpus check refuses
// one rather than leaving this to truncate.
func handleID(handle string) record.ID {
	var id record.ID
	copy(id[:], handle)
	return id
}

func fixtureID(t *testing.T, handle string) record.ID {
	t.Helper()
	if len(handle) > record.IDSize {
		t.Fatalf("corpus handle %q is too long to encode as a record id", handle)
	}
	return handleID(handle)
}

func fixtureHandle(id record.ID) string {
	return string(trimZeros(id[:]))
}

func trimZeros(b []byte) []byte {
	for n := len(b); n > 0; n-- {
		if b[n-1] != 0 {
			return b[:n]
		}
	}
	return nil
}

func percent(part, whole int) string {
	if whole == 0 {
		return "  n/a"
	}
	return fmt.Sprintf("%5.3f", float64(part)/float64(whole))
}
