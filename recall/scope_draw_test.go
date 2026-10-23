package recall_test

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
)

// A shelf is a vault cut down to what ranking reads: the shipped vector index
// and the device's own metadata and term tables, with the bodies held here.
//
// Vectors are placed by hand rather than embedded, so how near each record sits
// to the query is set by the test and not by a model. The query always embeds
// to the direction every vector is measured from.
type shelf struct {
	t      *testing.T
	index  *index.Index
	store  *local.Store
	bodies map[record.ID]recall.Found
}

func newShelf(t *testing.T) *shelf {
	t.Helper()
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open the device tables: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &shelf{t: t, index: index.New("hand-placed", 2), store: store, bodies: map[record.ID]recall.Found{}}
}

// towards is the direction the query embeds to.
var towards = []float32{1, 0}

// nearer is the nth of a run of vectors close to the query, each a little
// further from it than the one before.
func nearer(n int) []float32 { return direction(1, 0.001*float64(n+1)) }

// far is a vector well away from the query, further than any nearer one.
var far = direction(0.1, 1)

func direction(x, y float64) []float32 {
	norm := math.Hypot(x, y)
	return []float32{float32(x / norm), float32(y / norm)}
}

// memory stores a memory found by the words of its statement and, unless its
// vector is nil, by meaning.
func (s *shelf) memory(statement string, vector []float32, tags ...string) record.ID {
	s.t.Helper()
	return s.memoryReplacing(nil, statement, vector, tags...)
}

// memoryReplacing stores a memory that supersedes another.
func (s *shelf) memoryReplacing(replaces *record.ID, statement string, vector []float32, tags ...string) record.ID {
	s.t.Helper()
	id := s.newID()
	memory := &record.Memory{
		ID: id, Kind: record.KindMemory, Type: record.TypeFact,
		SchemaVersion: record.SchemaVersion, Version: 1,
		Statement: statement, Tags: tags, Supersedes: replaces,
		CreatedAt: record.Now(), UpdatedAt: record.Now(),
	}
	s.put(local.RankingMeta{
		ID: id, Kind: record.KindMemory, Type: memory.Type, Tags: tags,
		Supersedes: replaces, Text: local.LexicalText(memory),
	}, vector)
	s.bodies[id] = recall.Found{Memory: memory}
	return id
}

// conversation stores a conversation found by the words of its title and
// summary and, unless its vector is nil, by meaning.
func (s *shelf) conversation(title, summary string, vector []float32, tags ...string) record.ID {
	s.t.Helper()
	id := s.newID()
	session := &record.Session{ID: id, Version: 1, Title: title, Summary: summary, Tags: tags}
	s.put(local.RankingMeta{
		ID: id, Kind: record.KindSession, Tags: tags, Text: local.SessionLexicalText(session),
	}, vector)
	s.bodies[id] = recall.Found{Session: session}
	return id
}

func (s *shelf) newID() record.ID {
	s.t.Helper()
	id, err := record.NewID()
	if err != nil {
		s.t.Fatalf("new id: %v", err)
	}
	return id
}

func (s *shelf) put(meta local.RankingMeta, vector []float32) {
	s.t.Helper()
	if err := s.store.PutRankingMeta(meta); err != nil {
		s.t.Fatalf("store the metadata of %s: %v", meta.ID, err)
	}
	if vector == nil {
		return
	}
	if err := s.index.Add(meta.ID, "hand-placed", vector); err != nil {
		s.t.Fatalf("index %s: %v", meta.ID, err)
	}
}

// Describe and Select answer the pipeline's questions from the device tables,
// as the vault does.
func (s *shelf) Describe(ids []record.ID) (recall.Described, error) {
	described, err := s.store.Describe(ids)
	if err != nil {
		return recall.Described{}, err
	}
	out := recall.Described{Meta: map[record.ID]recall.Meta{}, Superseded: described.Superseded}
	for id, meta := range described.Meta {
		out.Meta[id] = recall.Meta{Kind: meta.Kind, Type: meta.Type, Tags: meta.Tags}
	}
	return out, nil
}

func (s *shelf) Select(scope []record.Kind) (map[record.ID]bool, error) {
	selected := map[record.ID]bool{}
	for _, kind := range scope {
		ids, err := s.store.RankingMetaIDs(kind)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			selected[id] = true
		}
	}
	return selected, nil
}

func (s *shelf) Fetch(_ context.Context, id record.ID) (recall.Found, recall.Tier, error) {
	found, ok := s.bodies[id]
	if !ok {
		return recall.Found{}, "", fmt.Errorf("no body for %s", id)
	}
	return found, recall.TierLocal, nil
}

// alwaysTowards embeds every query to the same direction.
type alwaysTowards struct{}

func (alwaysTowards) EmbedOne(context.Context, string) ([]float32, error) { return towards, nil }

func (s *shelf) recall(req recall.Request) recall.Result {
	s.t.Helper()
	return s.recallThrough(s, req)
}

func (s *shelf) recallThrough(catalog recall.Catalog, req recall.Request) recall.Result {
	s.t.Helper()
	if req.Query == "" {
		req.Query = "harbour gauge survey"
	}
	result, err := recall.New(alwaysTowards{}, s.index, s, catalog, s.store).Run(context.Background(), req)
	if err != nil {
		s.t.Fatalf("recall %+v: %v", req, err)
	}
	return result
}

// kinds says what a result holds, for a failure message.
func kinds(result recall.Result) string {
	memories, conversations := 0, 0
	for _, hit := range result.Hits {
		switch hit.Kind() {
		case record.KindMemory:
			memories++
		case record.KindSession:
			conversations++
		}
	}
	return fmt.Sprintf("%d memories and %d conversations", memories, conversations)
}

// The two scopes a caller can set.
var (
	onlyConversations = []record.Kind{record.KindSession}
	onlyMemories      = []record.Kind{record.KindMemory}
)

// A recall scoped to conversations finds the vault's one conversation past a
// pool's worth of memories nearer the query.
//
// The pool used to be drawn from every record and scoped afterwards. With more
// memories nearer the query than the pool holds, they filled it, the scope
// removed all of them, and the recall answered that there was no conversation
// though the vault held one. Both passes now draw from the records in scope.
//
// What the scope removed is still counted on the pool an unscoped recall draws,
// which here is that recall's whole pool, since none of it is a conversation.
func TestAScopedRecallFindsAConversationPastAPoolOfNearerMemories(t *testing.T) {
	s := newShelf(t)
	for n := range recall.DefaultCandidates + 1 {
		s.memory(fmt.Sprintf("Reading %d was taken at dawn.", n), nearer(n))
	}
	wanted := s.conversation("Pier walk", "Walked the north pier at low water.", far)

	unscoped := s.recall(recall.Request{Limit: recall.DefaultCandidates})
	for _, hit := range unscoped.Hits {
		if hit.Session != nil {
			t.Fatal("the conversation is in an unscoped recall's pool, so nothing here crowds it out")
		}
	}
	if unscoped.Considered != recall.DefaultCandidates {
		t.Fatalf("an unscoped recall drew %d candidates, want a full pool of %d", unscoped.Considered,
			recall.DefaultCandidates)
	}

	scoped := s.recall(recall.Request{Scope: onlyConversations})
	if len(scoped.Hits) != 1 || scoped.Hits[0].Session == nil || scoped.Hits[0].Session.ID != wanted {
		t.Fatalf("a recall scoped to conversations answered with %s, want the one conversation", kinds(scoped))
	}
	if scoped.ScopeExcluded != unscoped.Considered {
		t.Errorf("the scope reports removing %d candidate(s), want the %d an unscoped recall draws, "+
			"all of them memories", scoped.ScopeExcluded, unscoped.Considered)
	}
	if scoped.Considered != 1 {
		t.Errorf("the scoped recall ranked %d candidate(s), want the 1 conversation", scoped.Considered)
	}
	if scoped.Searched != unscoped.Searched {
		t.Errorf("the scoped recall searched %d vectors and the unscoped one %d; both score every vector",
			scoped.Searched, unscoped.Searched)
	}
}

// A recall scoped to memories finds the vault's one memory past a pool's worth
// of conversations nearer the query, the same crowding the other way round.
func TestAScopedRecallFindsAMemoryPastAPoolOfNearerConversations(t *testing.T) {
	s := newShelf(t)
	for n := range recall.DefaultCandidates + 1 {
		s.conversation(fmt.Sprintf("Standup %d", n), "Went through the week's open items.", nearer(n))
	}
	wanted := s.memory("The pier light is switched on at dusk.", far)

	unscoped := s.recall(recall.Request{Limit: recall.DefaultCandidates})
	for _, hit := range unscoped.Hits {
		if hit.Memory != nil {
			t.Fatal("the memory is in an unscoped recall's pool, so nothing here crowds it out")
		}
	}

	scoped := s.recall(recall.Request{Scope: onlyMemories})
	if len(scoped.Hits) != 1 || scoped.Hits[0].Memory == nil || scoped.Hits[0].Memory.ID != wanted {
		t.Fatalf("a recall scoped to memories answered with %s, want the one memory", kinds(scoped))
	}
	if scoped.ScopeExcluded != unscoped.Considered {
		t.Errorf("the scope reports removing %d candidate(s), want the %d an unscoped recall draws, "+
			"all of them conversations", scoped.ScopeExcluded, unscoped.Considered)
	}
}

// The lexical half of a scoped recall draws from the records in scope too.
//
// The conversation here has no search vector, as on a device that has not
// restored one, so only its words can find it, and more memories than the pool
// holds match the query's words better. Drawn from every record, the term
// pass's pool was all memories and the conversation was never in it.
//
// LexicalHits still counts what the query's words matched across every record,
// so it is not zero while records outside the scope share them.
func TestAScopedRecallFindsARecordByItsWordsPastAPoolOfBetterMatchesOutsideTheScope(t *testing.T) {
	s := newShelf(t)
	for n := range recall.DefaultCandidates + 1 {
		s.memory(fmt.Sprintf("Harbour gauge survey reading %d.", n), nearer(n))
	}
	wanted := s.conversation("Pier walk", "Read the gauge on the north pier.", nil)

	scoped := s.recall(recall.Request{Scope: onlyConversations})
	if len(scoped.Hits) != 1 || scoped.Hits[0].Session == nil || scoped.Hits[0].Session.ID != wanted {
		t.Fatalf("a recall scoped to conversations answered with %s, want the conversation its words find",
			kinds(scoped))
	}
	if scoped.Hits[0].Lexical == 0 || scoped.Hits[0].Similarity != 0 {
		t.Errorf("the conversation was found with lexical %v and similarity %v, want by its words alone",
			scoped.Hits[0].Lexical, scoped.Hits[0].Similarity)
	}
	if scoped.LexicalHits != recall.DefaultCandidates {
		t.Errorf("the query's words matched %d record(s) by the count, want the term pass's whole pool of %d",
			scoped.LexicalHits, recall.DefaultCandidates)
	}
}

// Inside a scope the filter still only reorders. It moves a matching record up
// the pool drawn from the records in scope, and a filter nothing matches leaves
// that pool in the order and at the length it had.
func TestAFilterOnlyReordersThePoolAScopeDraws(t *testing.T) {
	s := newShelf(t)
	for n := range recall.DefaultCandidates + 1 {
		s.memory(fmt.Sprintf("Reading %d was taken at dawn.", n), nearer(n))
	}
	first := s.conversation("Pier walk", "Walked the north pier.", direction(0.30, 1))
	second := s.conversation("Buoy check", "Checked the outer buoys.", direction(0.20, 1))
	third := s.conversation("Light test", "Tested the pier light.", direction(0.10, 1), "lighthouse")

	plain := s.recall(recall.Request{Scope: onlyConversations})
	order := func(result recall.Result) []record.ID {
		var out []record.ID
		for _, hit := range result.Hits {
			out = append(out, hit.ID())
		}
		return out
	}
	if got := order(plain); fmt.Sprint(got) != fmt.Sprint([]record.ID{first, second, third}) {
		t.Fatalf("the scoped recall answered %v, want the three conversations nearest first", got)
	}

	wrong := s.recall(recall.Request{Scope: onlyConversations, Filter: recall.Filter{Tags: []string{"knitting"}}})
	if fmt.Sprint(order(wrong)) != fmt.Sprint(order(plain)) {
		t.Errorf("a filter nothing matches changed the scoped answer from %v to %v", order(plain), order(wrong))
	}

	preferred := s.recall(recall.Request{Scope: onlyConversations, Filter: recall.Filter{Tags: []string{"lighthouse"}}})
	if len(preferred.Hits) != 3 {
		t.Fatalf("a filter changed how many conversations came back, from 3 to %d", len(preferred.Hits))
	}
	if preferred.Hits[0].ID() != third || preferred.Hits[0].Boost == 0 {
		t.Errorf("a filter matching the third conversation left it at %v, want it first and boosted", order(preferred))
	}
}

// A replaced version in scope is held back and counted, as it is without a
// scope, and asking for history brings it back.
func TestAScopedRecallHoldsBackAReplacedVersionAndCountsIt(t *testing.T) {
	s := newShelf(t)
	for n := range recall.DefaultCandidates + 1 {
		s.conversation(fmt.Sprintf("Standup %d", n), "Went through the week's open items.", nearer(n))
	}
	old := s.memory("The pier light is switched on at sunset.", direction(0.1, 1))
	current := s.memoryReplacing(&old, "The pier light is switched on at dusk.", direction(0.11, 1))

	scoped := s.recall(recall.Request{Scope: onlyMemories})
	if len(scoped.Hits) != 1 || scoped.Hits[0].ID() != current {
		t.Fatalf("a recall scoped to memories answered with %s, want only the current memory", kinds(scoped))
	}
	if scoped.SupersededHidden != 1 {
		t.Errorf("the scoped recall counts %d replaced version(s) held back, want 1", scoped.SupersededHidden)
	}

	history := s.recall(recall.Request{Scope: onlyMemories, IncludeSuperseded: true})
	if len(history.Hits) != 2 || history.SupersededHidden != 0 {
		t.Errorf("asking for history answered with %s and held back %d, want both memories and nothing held back",
			kinds(history), history.SupersededHidden)
	}
}

// Scoping to every class finds what an unscoped recall finds, scored and
// ordered the same, when every record has metadata. Naming both classes is a
// scope like any other and is drawn as one, so nothing here is special-cased.
//
// Each record is a word longer than the one before, so no two match the query
// equally well and the order compared is the ranking's, not how a tie fell.
func TestAScopeOfEveryClassAnswersAsNoScopeDoes(t *testing.T) {
	s := newShelf(t)
	for n := range 30 {
		padding := strings.Repeat(" again", n)
		if n%3 == 0 {
			s.conversation("Harbour survey", "Surveyed the harbour gauge"+padding+".", nearer(n))
			continue
		}
		s.memory("Harbour gauge reading"+padding+".", nearer(n), "harbour")
	}

	for _, limit := range []int{5, 30} {
		unscoped := s.recall(recall.Request{Limit: limit, Candidates: 12})
		both := s.recall(recall.Request{Limit: limit, Candidates: 12,
			Scope: []record.Kind{record.KindMemory, record.KindSession}})
		if len(both.Hits) != len(unscoped.Hits) || both.ScopeExcluded != 0 {
			t.Fatalf("limit %d: scoping to every class answered %d record(s) and removed %d, "+
				"an unscoped recall %d", limit, len(both.Hits), both.ScopeExcluded, len(unscoped.Hits))
		}
		for i := range both.Hits {
			if both.Hits[i].ID() != unscoped.Hits[i].ID() || both.Hits[i].Score != unscoped.Hits[i].Score {
				t.Fatalf("limit %d: at position %d scoping to every class answered %s scored %v, "+
					"an unscoped recall %s scored %v", limit, i, both.Hits[i].ID(), both.Hits[i].Score,
					unscoped.Hits[i].ID(), unscoped.Hits[i].Score)
			}
		}
		if both.Considered != unscoped.Considered || both.LexicalHits != unscoped.LexicalHits {
			t.Errorf("limit %d: scoping to every class considered %d and matched %d by words, "+
				"an unscoped recall %d and %d", limit, both.Considered, both.LexicalHits,
				unscoped.Considered, unscoped.LexicalHits)
		}
	}
}

// describeOnly is a catalog that cannot say which records a scope selects.
type describeOnly struct{ s *shelf }

func (c describeOnly) Describe(ids []record.ID) (recall.Described, error) { return c.s.Describe(ids) }

// A catalog that cannot select the records in scope still never answers a
// scope with a record outside it. The pool is then drawn from every record and
// the scope applied to it, which is how every catalog was treated before, and
// what it removed is counted.
func TestACatalogThatCannotSelectStillNeverAnswersOutsideTheScope(t *testing.T) {
	s := newShelf(t)
	for n := range 3 {
		s.memory(fmt.Sprintf("Reading %d was taken at dawn.", n), nearer(n))
	}
	for n := range 2 {
		s.conversation(fmt.Sprintf("Pier walk %d", n), "Walked the north pier.", nearer(3+n))
	}

	scoped := s.recallThrough(describeOnly{s}, recall.Request{Scope: onlyConversations})
	if len(scoped.Hits) != 2 {
		t.Fatalf("a recall scoped to conversations answered with %s, want the 2 conversations", kinds(scoped))
	}
	for _, hit := range scoped.Hits {
		if hit.Session == nil {
			t.Fatalf("a recall scoped to conversations answered with a %s", hit.Kind())
		}
	}
	if scoped.ScopeExcluded != 3 {
		t.Errorf("the scope reports removing %d candidate(s), want the 3 memories", scoped.ScopeExcluded)
	}
}
