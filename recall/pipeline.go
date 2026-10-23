package recall

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// An Embedder turns a query into a vector.
//
// Ranking asks for one vector per recall and nothing else, so this is stated as
// the one method rather than as the whole embedder: it keeps the pipeline
// buildable, and testable, without a model resident in the process.
type Embedder interface {
	EmbedOne(ctx context.Context, text string) ([]float32, error)
}

// A Fetcher resolves a record id to its decrypted body.
//
// Ranking asks for records by id, so everything about where a body actually
// comes from, this device, a cached location, or the network, stays behind
// this one method.
type Fetcher interface {
	Fetch(ctx context.Context, id record.ID) (Found, Tier, error)
}

// A Catalog answers the questions ranking must settle before it fetches
// anything.
//
// It exists because the order is decided while only ids are in hand: by the time
// a body has been read the ranking has already happened.
//
// It takes the whole candidate pool at once rather than one id at a time. That
// is not only cheaper, it is what lets the answer be read from the device on
// every recall instead of from a cache built when the process started. Another
// process sharing this vault can supersede a record at any moment, and a cached
// answer would keep returning the replaced version until something reopened.
type Catalog interface {
	Describe(ids []record.ID) (Described, error)
}

// Described is what the catalog knows about a candidate pool.
type Described struct {
	// Meta is keyed by record id. A record that is absent is ranked on
	// similarity alone rather than dropped: not knowing a record's tags is a
	// gap in this device's metadata, never evidence about the record.
	Meta map[record.ID]Meta
	// Superseded holds the ids some later record replaces.
	Superseded map[record.ID]bool
}

// A Selector is a catalog that can also say which records a scope selects.
//
// A scoped recall asks it before anything is searched, so that the pool is drawn
// from the records the caller is addressing. Drawing the pool from every record
// and applying the scope afterwards loses the answer whenever a pool's worth of
// records outside the scope sit nearer the query: they fill the pool, the scope
// removes them all, and the record that was asked for was never drawn.
//
// It is asked on every scoped recall rather than once, for the reason Describe
// is: another process sharing this vault can add or forget a record at any
// moment.
//
// A catalog that is not a Selector is still a catalog. Its scoped recalls draw
// the pool from every record and apply the scope to it, which can miss a record
// in scope but never returns one outside it.
type Selector interface {
	// Select returns the ids of every record of the given classes. A record
	// this device holds no metadata for is of no class, and is never selected.
	Select(scope []record.Kind) (map[record.ID]bool, error)
}

// A Pipeline runs a query from text to ranked records.
//
// Three signals decide the order, and they compose in this order and no other:
//
//  1. the vector pass retrieves the candidate pool and ranks it by meaning;
//  2. the lexical pass reranks that pool by the words the query used, fused with
//     the vector ranking by weighted reciprocal rank fusion;
//  3. the metadata filter boosts what the caller said it was looking for.
//
// Fusion sits beneath the filter because the two answer different questions.
// Fusion decides which records best match the query as asked; the filter
// expresses a preference about the answer that the query text does not carry.
// Applying the filter first would mean fusing a ranking the caller had already
// perturbed, and the lexical pass would be reranking a pool ordered partly by
// something it knows nothing about.
//
// A scope is not a fourth signal, because it orders nothing. It decides which
// records both passes draw their candidates from, before either of them runs.
type Pipeline struct {
	embedder Embedder
	index    *index.Index
	fetcher  Fetcher
	catalog  Catalog
	lexical  Lexical
}

// New builds a pipeline over an embedder, an index, a fetcher, a catalog and a
// lexical index.
//
// A nil lexical index is allowed and leaves the ranking dense-only. That is a
// worse ranking, not a broken one, and it is what a vault opened before the
// lexical index existed has until it is rebuilt.
func New(embedder Embedder, idx *index.Index, fetcher Fetcher, catalog Catalog, lexical Lexical) *Pipeline {
	return &Pipeline{embedder: embedder, index: idx, fetcher: fetcher, catalog: catalog, lexical: lexical}
}

// A Request is one recall.
type Request struct {
	Query string
	Limit int
	// Scope selects which classes of record may answer. Empty means all of
	// them, which is the default and is what makes one ranked list possible.
	//
	// This is a hard selector and it is deliberately not the same mechanism as
	// Filter. The two answer different questions. A filter is a guess about
	// what the answer will be *about*, an agent infers tags and types from the
	// wording of a question, gets some of them wrong, and must never lose an
	// answer for it; that is why filtering only ever prefers. A scope is a
	// statement about which container is being addressed, made explicitly by a
	// caller who is not guessing: "list my sessions" is a different operation
	// from a question whose phrasing happens to suggest a type, and answering
	// the first with memories is simply the wrong answer.
	//
	// Nothing derives a scope from the query text. The moment it were inferred
	// it would be a guess, and a hard selector applied to a guess is exactly the
	// mechanism that was measured returning nothing at all for a sixth of
	// queries.
	//
	// It is applied before the candidates are drawn, never to the pool once it
	// has been. Both passes draw their candidates from the records in scope, so
	// a scoped recall finds a record in scope however many records outside it
	// sit nearer the query. A pool drawn from every record and scoped afterwards
	// fills with those nearer records whenever there are a pool's worth of
	// them, and the scope then removes all of it.
	Scope []record.Kind
	// Filter prefers some records over others. It never removes any.
	Filter Filter
	// Weights override how far the filter may move a record. The zero value
	// means the shipped defaults.
	Weights Weights
	// LexicalWeight overrides how much say the lexical pass gets. The zero
	// value means DefaultLexicalWeight; a negative value disables the lexical
	// pass and ranks on meaning alone.
	LexicalWeight float32
	// IncludeSuperseded returns replaced versions alongside current ones.
	//
	// Default recall answers with the vault's current state, which is what
	// makes an append-only store usable: otherwise every correction comes back
	// beside the thing it corrected and the caller has to work out which is
	// which. History is retained and reachable; it is just not the default.
	IncludeSuperseded bool
	// Candidates is how many records are scored before the filter reorders
	// them. Zero means DefaultCandidates. With a scope, they are drawn from the
	// records in scope.
	Candidates int
}

// A Result is one recall's ranked hits and the time each stage took.
type Result struct {
	Hits []Hit
	// EmbedFor is how long the query took to embed, SearchFor how long the
	// vector scan and ranking took, FetchFor how long resolving and decrypting
	// the hits took.
	EmbedFor  time.Duration
	SearchFor time.Duration
	FetchFor  time.Duration
	// Searched is how many vectors the query was scored against.
	Searched int
	// Considered is how many candidates the filter reordered.
	Considered int
	// LexicalHits is how many records the term pass matched. Zero means the
	// query shared no indexed word with anything in the vault, and the ranking
	// is the vector pass's alone.
	//
	// A scoped recall counts what the term pass matched across every record,
	// not only in scope, which is what keeps zero meaning both of those. Counted
	// in scope alone, zero would say no record shares a word with the query
	// when records outside the scope do.
	LexicalHits int
	// SupersededHidden is how many replaced versions were held back.
	SupersededHidden int
	// ScopeExcluded is how many candidates the scope removed. It is reported
	// rather than left silent because a scope is the one thing in this pipeline
	// that can shorten an answer, and a caller who selected the wrong container
	// should be able to see that from the result instead of inferring it from
	// an empty list.
	//
	// The candidates are the ones an unscoped recall would have drawn, because
	// a scoped pool is drawn from the records in scope and holds nothing for the
	// scope to remove. Of those it counts the records outside the scope, less
	// any replaced version held back as well, which dropping the scope alone
	// would not bring back.
	ScopeExcluded int
}

const (
	// DefaultLimit is how many records a recall returns when the caller does
	// not say.
	DefaultLimit = 5

	// DefaultCandidates is how deep the pool is that the filter reorders.
	//
	// A boost can only reorder what similarity already retrieved, so a pool the
	// size of the answer would make the filter decorative, the records it
	// exists to promote would never have been fetched. This is the depth the
	// measured gain was established at.
	DefaultCandidates = 100
)

// Run executes a recall.
//
// Returning nothing is a legitimate answer, not a failure: a vault that holds no
// relevant record should say so rather than return its least-bad guess.
func (p *Pipeline) Run(ctx context.Context, req Request) (Result, error) {
	if req.Query == "" {
		return Result{}, fmt.Errorf("recall: empty query")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	pool := req.Candidates
	if pool <= 0 {
		pool = DefaultCandidates
	}
	if pool < limit {
		pool = limit
	}

	start := time.Now()
	query, err := p.embedder.EmbedOne(ctx, req.Query)
	if err != nil {
		return Result{}, err
	}
	result := Result{EmbedFor: time.Since(start), Searched: p.index.Len()}

	start = time.Now()
	weight := lexicalWeight(req)
	drawn, err := p.draw(query, req, pool, weight)
	if err != nil {
		return Result{}, err
	}
	known, err := p.describe(drawn.pool)
	if err != nil {
		return Result{}, err
	}
	ranked, dropped := rank(drawn.pool, known, req, limit)
	result.Considered = len(drawn.pool)
	result.LexicalHits = drawn.lexicalHits
	result.SupersededHidden = dropped.superseded
	result.ScopeExcluded = dropped.outOfScope
	if drawn.scoped {
		// The pool holds only records in scope, so what the scope removed is
		// counted on the pool it would otherwise have been applied to, which
		// the catalog is asked about as well.
		around, err := p.describe(drawn.unscoped)
		if err != nil {
			return Result{}, err
		}
		result.ScopeExcluded = outsideScope(drawn.unscoped, around, req)
	}
	result.SearchFor = time.Since(start)

	start = time.Now()
	for _, match := range ranked {
		fetchedAt := time.Now()
		found, tier, err := p.fetcher.Fetch(ctx, match.ID)
		if err != nil {
			return Result{}, fmt.Errorf("recall %s: %w", match.ID, err)
		}
		result.Hits = append(result.Hits, Hit{
			Found:      found,
			Score:      match.Score,
			Similarity: match.Similarity,
			Lexical:    match.Lexical,
			Boost:      match.Boost,
			Tier:       tier,
			FetchedIn:  time.Since(fetchedAt),
		})
	}
	result.FetchFor = time.Since(start)
	return result, nil
}

// A candidate is one record on its way to being ranked.
type candidate struct {
	ID record.ID
	// Score is what the order is decided on: the fused rank score plus whatever
	// the filter added.
	Score float32
	// Similarity is the vector pass's own cosine, carried through unchanged so a
	// caller can still see how close the record sits to the query. It is
	// reported, never ranked on, once fusion has run.
	Similarity float32
	// Fused is the rank-fusion score before the filter touched it, and Lexical
	// is the part of it the lexical pass contributed.
	Fused   float32
	Lexical float32
	Boost   float32
}

// lexicalWeight resolves how much say the lexical pass gets for one request.
//
// A negative weight is how a caller asks for the vector pass alone; zero is the
// unset value and means the shipped default, because a Request literal that does
// not mention fusion must get the shipped ranking.
func lexicalWeight(req Request) float32 {
	if req.LexicalWeight == 0 {
		return DefaultLexicalWeight
	}
	if req.LexicalWeight < 0 {
		return 0
	}
	return req.LexicalWeight
}

// A draw is the candidate pool a recall ranks, and what was found on the way.
type draw struct {
	// pool is what the ranking orders: the candidates of both passes, fused.
	pool []candidate
	// lexicalHits is how many records the query's words matched, in scope or
	// not.
	lexicalHits int
	// scoped reports that the pool was drawn from the records in scope, and
	// unscoped is then the pool an unscoped recall would have drawn, kept so
	// the result can say what the scope removed.
	scoped   bool
	unscoped []candidate
}

// draw runs both passes and fuses what they find into the candidate pool.
//
// Without a scope the pool is each pass's k nearest of every record, as it has
// always been. With one, each pass draws its k from the records in scope, and
// the pool an unscoped recall would have drawn is found beside it for counting
// what the scope removed. That costs the catalog's list of the records in
// scope, one pass over the vectors as before with a second heap kept in it, and
// the term pass run twice, with the scope and without.
//
// A catalog that cannot list the records in scope leaves the scope to rank,
// which applies it to a pool drawn from every record. That can miss a record in
// scope, but it returns nothing outside it.
func (p *Pipeline) draw(query []float32, req Request, k int, weight float32) (draw, error) {
	selector, selects := p.catalog.(Selector)
	if len(req.Scope) == 0 || !selects {
		matches, err := p.index.Search(query, k)
		if err != nil {
			return draw{}, err
		}
		lexical, err := p.searchLexical(req.Query, k, weight)
		if err != nil {
			return draw{}, err
		}
		return draw{pool: fuse(matches, lexical, weight), lexicalHits: len(lexical)}, nil
	}

	selected, err := selector.Select(req.Scope)
	if err != nil {
		return draw{}, err
	}
	among, all, err := p.index.SearchWithin(query, k, func(id record.ID) bool { return selected[id] })
	if err != nil {
		return draw{}, err
	}
	lexical, err := p.searchLexical(req.Query, k, weight, req.Scope...)
	if err != nil {
		return draw{}, err
	}
	everywhere, err := p.searchLexical(req.Query, k, weight)
	if err != nil {
		return draw{}, err
	}
	return draw{
		pool:        fuse(among, lexical, weight),
		lexicalHits: len(everywhere),
		scoped:      true,
		unscoped:    fuse(all, everywhere, weight),
	}, nil
}

// searchLexical runs the term pass, tolerating its absence.
//
// A vault whose lexical index is missing or empty ranks on meaning alone rather
// than failing: half a ranking is a worse answer, and no answer at all is not
// one. A caller who has asked for no lexical contribution is not charged for the
// query either, so ranking by meaning alone costs what it did before there was
// a second pass.
func (p *Pipeline) searchLexical(query string, k int, weight float32, scope ...record.Kind) ([]record.ID, error) {
	if p.lexical == nil || weight == 0 {
		return nil, nil
	}
	return p.lexical.SearchLexical(query, k, scope...)
}

// describe asks the catalog about the candidate pool, tolerating its absence.
func (p *Pipeline) describe(fused []candidate) (Described, error) {
	if p.catalog == nil || len(fused) == 0 {
		return Described{}, nil
	}
	ids := make([]record.ID, len(fused))
	for i, entry := range fused {
		ids[i] = entry.ID
	}
	return p.catalog.Describe(ids)
}

// dropped counts the candidates that did not reach the ranking, by reason.
type dropped struct {
	superseded int
	outOfScope int
}

// rank applies the filter to the fused pool and cuts it to k.
//
// The filter is applied here and nowhere else, as an addition to a score. The
// length of what this returns is min(k, len(fused)) whatever the filter says,
// that is the property which makes a wrong filter cost ranking quality instead
// of the answer, and it is the one thing about this function that must not
// change.
//
// Exactly two things drop a candidate, and neither is a guess about relevance.
// Supersession is a statement about which version of a record is current,
// decided by the vault's own history. Scope is a statement about which class of
// record the caller is addressing, made explicitly rather than inferred. Both
// are counted.
//
// A scoped pool is normally drawn from the records in scope already, and then
// the scope finds nothing here to drop. It still checks every candidate, so a
// pool drawn from every record, by a catalog that cannot list the records in
// scope, is scoped here as it always was, and a record whose metadata has gone
// since the pool was drawn never answers a scope it can no longer be shown to
// be in.
//
// It is a free function rather than a method so that it can be exercised without
// an embedder, an index or a network.
func rank(fused []candidate, known Described, req Request, k int) ([]candidate, dropped) {
	weights := req.Weights.orDefault()
	scored := make([]candidate, 0, len(fused))
	var counts dropped

	for _, entry := range fused {
		if !req.IncludeSuperseded && known.Superseded[entry.ID] {
			counts.superseded++
			continue
		}
		meta, described := known.Meta[entry.ID]
		if len(req.Scope) > 0 && !inScope(req.Scope, meta.Kind, described) {
			counts.outOfScope++
			continue
		}
		if !req.Filter.Empty() && described {
			entry.Boost = req.Filter.boost(weights, meta)
			entry.Score += entry.Boost
		}
		scored = append(scored, entry)
	}

	sort.SliceStable(scored, func(a, b int) bool {
		if scored[a].Score != scored[b].Score {
			return scored[a].Score > scored[b].Score
		}
		// Ties break on the id so two runs over one vault agree.
		return scored[a].ID.String() < scored[b].ID.String()
	})
	if len(scored) > k {
		scored = scored[:k]
	}
	return scored, counts
}

// outsideScope counts the candidates of a pool drawn from every record that the
// request's scope does not select.
//
// It asks rank's two questions in rank's order, so it counts exactly what rank
// would drop from that pool for the scope. A replaced version held back is
// counted as that and not as outside the scope too, since dropping the scope
// alone would not bring it back.
func outsideScope(pool []candidate, known Described, req Request) int {
	var outside int
	for _, entry := range pool {
		if !req.IncludeSuperseded && known.Superseded[entry.ID] {
			continue
		}
		meta, described := known.Meta[entry.ID]
		if !inScope(req.Scope, meta.Kind, described) {
			outside++
		}
	}
	return outside
}

// inScope reports whether a candidate belongs to one of the selected classes.
//
// A candidate this device holds no metadata for is out of scope rather than in
// it. That is the opposite of how the soft filter treats the same gap, and
// deliberately: a filter that cannot read a record's tags has no evidence and
// must not act, whereas a scope that cannot read a record's class cannot honour
// what the caller explicitly asked for, and returning the wrong class of record
// is a worse answer than returning one fewer. Every candidate reaches the index
// and the metadata in one transaction, so this is a corrupted invariant rather
// than an ordinary state, and it is counted where an ordinary state would not
// be.
func inScope(scope []record.Kind, kind record.Kind, described bool) bool {
	if !described {
		return false
	}
	for _, want := range scope {
		if want == kind {
			return true
		}
	}
	return false
}
