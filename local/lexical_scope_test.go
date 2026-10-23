package local

import (
	"slices"
	"strings"
	"testing"

	"github.com/steven3002/sennit/record"
)

const harbourQuery = "harbour gauge survey"

// putTerms stores one record's class and the words it is found by.
func putTerms(t *testing.T, store *Store, kind record.Kind, text string) record.ID {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	meta := RankingMeta{ID: id, Kind: kind, Text: text}
	if kind == record.KindMemory {
		meta.Type = record.TypeFact
	}
	if err := store.PutRankingMeta(meta); err != nil {
		t.Fatalf("store %s: %v", id, err)
	}
	return id
}

// A term search confined to one class of record returns the best matches of
// that class, however many records of another class match better.
//
// The best k of every record here are all memories, so a search that took
// those and kept the conversations among them would find none. Confined to
// conversations, the search reaches the one conversation past all of them.
func TestATermSearchConfinedToAClassReachesPastBetterMatchesOfAnother(t *testing.T) {
	store := openTemp(t)
	const k = 5
	// Each memory is a word longer than the one before, so it matches a little
	// worse, and every one of them matches better than the conversation.
	for n := range k + 1 {
		putTerms(t, store, record.KindMemory, "Harbour gauge survey reading"+strings.Repeat(" again", n))
	}
	conversation := putTerms(t, store, record.KindSession, "Pier walk\nRead the gauge on the north pier.")

	everything, err := store.SearchLexical(harbourQuery, k)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(everything) != k || slices.Contains(everything, conversation) {
		t.Fatalf("the whole search found %d record(s), the conversation among them: %v; "+
			"want %d memories and no conversation", len(everything), slices.Contains(everything, conversation), k)
	}

	conversations, err := store.SearchLexical(harbourQuery, k, record.KindSession)
	if err != nil {
		t.Fatalf("search conversations: %v", err)
	}
	if !slices.Equal(conversations, []record.ID{conversation}) {
		t.Fatalf("a search confined to conversations found %v, want the one conversation %s",
			conversations, conversation)
	}

	// Confined to memories, it is the whole search, since the whole search's
	// best are all memories.
	memories, err := store.SearchLexical(harbourQuery, k, record.KindMemory)
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if !slices.Equal(memories, everything) {
		t.Errorf("a search confined to memories found %v, the whole search %v", memories, everything)
	}
}

// A confined search orders the records of its classes as the whole search
// orders them, because each is scored the same way: BM25 weighs a word by how
// rare it is across every record, not across the records of one class.
func TestATermSearchConfinedToAClassOrdersItAsTheWholeSearchDoes(t *testing.T) {
	store := openTemp(t)
	for n := range 12 {
		kind := record.KindMemory
		if n%3 == 1 {
			kind = record.KindSession
		}
		putTerms(t, store, kind, "Harbour gauge survey"+strings.Repeat(" again", n))
	}
	everything, err := store.SearchLexical(harbourQuery, 12)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	kindOf := map[record.ID]record.Kind{}
	for _, kind := range []record.Kind{record.KindMemory, record.KindSession} {
		ids, err := store.RankingMetaIDs(kind)
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		for _, id := range ids {
			kindOf[id] = kind
		}
	}

	for _, kind := range []record.Kind{record.KindMemory, record.KindSession} {
		var want []record.ID
		for _, id := range everything {
			if kindOf[id] == kind {
				want = append(want, id)
			}
		}
		confined, err := store.SearchLexical(harbourQuery, 12, kind)
		if err != nil {
			t.Fatalf("search %s: %v", kind, err)
		}
		if !slices.Equal(confined, want) {
			t.Errorf("a search confined to %s found %v, want the whole search's %s in its order: %v",
				kind, confined, kind, want)
		}
	}

	both, err := store.SearchLexical(harbourQuery, 12, record.KindMemory, record.KindSession)
	if err != nil {
		t.Fatalf("search both: %v", err)
	}
	if !slices.Equal(both, everything) {
		t.Errorf("a search confined to both classes found %v, the whole search %v", both, everything)
	}
}

// A record that carries terms but no metadata is of no class, so a search
// confined to any class never returns it, though the whole search still does.
// Nothing writes the one without the other, so this is damage rather than an
// ordinary state, and a scope that cannot tell what a record is cannot honour
// a request for one class.
func TestATermSearchConfinedToAClassNeverReturnsARecordOfNoClass(t *testing.T) {
	store := openTemp(t)
	orphan, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO record_lexical (record_id, terms) VALUES (?, ?)`,
		orphan.String(), "harbour gauge survey"); err != nil {
		t.Fatalf("store terms with no metadata: %v", err)
	}
	putTerms(t, store, record.KindMemory, "Harbour gauge.")
	putTerms(t, store, record.KindSession, "Gauge survey")

	everything, err := store.SearchLexical(harbourQuery, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !slices.Contains(everything, orphan) {
		t.Fatalf("the whole search found %v, without the record that has no metadata", everything)
	}
	for _, kinds := range [][]record.Kind{
		{record.KindMemory}, {record.KindSession}, {record.KindMemory, record.KindSession},
	} {
		confined, err := store.SearchLexical(harbourQuery, 5, kinds...)
		if err != nil {
			t.Fatalf("search %v: %v", kinds, err)
		}
		if slices.Contains(confined, orphan) || len(confined) != len(kinds) {
			t.Errorf("a search confined to %v found %v, want one record of each class and not %s",
				kinds, confined, orphan)
		}
	}
}
