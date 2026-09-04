package recall_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
)

// This is the one package that measures the embedding model, so it is one of
// only two that load it, the numbers here are properties of that model and a
// stub would not be measuring anything. The harness never downloads it: an
// ordinary `go test ./...` has to touch no network, so a machine without the
// model gets a skip and a green run.

// evalKs are the depths reported. Five is the product-relevant one: it is what
// an agent is handed and has to find the answer in.
var evalKs = []int{1, 3, 5, 10}

// harness is the corpus loaded, embedded and ready to query.
type harness struct {
	fixture  fixture
	pipeline *recall.Pipeline
	byHandle map[string]fixtureRecord
	// density is how many non-gold records compete with a typical correct
	// answer to each query, the axis that exposes what an aggregate hides.
	density map[string]int
	// embedFor is what embedding the whole corpus cost, which is the figure
	// recovery is bounded by.
	embedFor time.Duration
	perDoc   time.Duration
	// lexical is the shipped term index over the same corpus.
	lexical *local.Store
}

// staticCatalog answers the ranking pipeline's questions from the fixture. The
// harness measures ranking, so it deliberately has no device and no network in
// the path: a change in hit@5 here can only have come from the ranker.
type staticCatalog struct {
	meta       map[record.ID]recall.Meta
	superseded map[record.ID]bool
}

func (c staticCatalog) Describe([]record.ID) (recall.Described, error) {
	return recall.Described{Meta: c.meta, Superseded: c.superseded}, nil
}

// staticFetcher returns the record the ranker asked for, without storage.
type staticFetcher struct{ bodies map[record.ID]*record.Memory }

func (f staticFetcher) Fetch(_ context.Context, id record.ID) (recall.Found, recall.Tier, error) {
	return recall.Found{Memory: f.bodies[id]}, recall.TierLocal, nil
}

// The lexical half of the ranking is measured through the shipped index rather
// than through a reimplementation of it.
//
// Everything else in this harness is deliberately static, because a change in
// hit@5 must be attributable to the ranker. The term index is the exception: it
// *is* part of the ranker, and a second BM25 written for the harness would let
// the product's own tokenizer, stopword policy and scoring drift without any
// number here moving. It runs on a temporary database, so this still touches no
// network and no vault.
var lexicalDir string

// TestMain owns the temporary database the lexical index lives in. The harness
// is built once for the whole package, so its store has to outlive the first
// test that asks for it, which rules out t.TempDir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sennit-recall-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall harness: %v\n", err)
		os.Exit(1)
	}
	lexicalDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// The corpus is embedded once for the whole package.
//
// Every test here wants the same 82 vectors, and producing them costs about
// thirty seconds on the pure-Go backend. Rebuilding per test pushed the package
// past the default test timeout, which would have made the harness unrunnable in
// exactly the place it is meant to run.
var (
	sharedOnce    sync.Once
	sharedHarness *harness
	sharedSkip    string
	sharedFatal   error
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	sharedOnce.Do(func() { sharedHarness, sharedSkip, sharedFatal = buildHarness() })
	switch {
	case sharedSkip != "":
		t.Skip(sharedSkip)
	case sharedFatal != nil:
		t.Fatalf("build harness: %v", sharedFatal)
	}
	return sharedHarness
}

// buildHarness loads the corpus, embeds it and wires a pipeline over it.
//
// The embedder is deliberately never closed: it lives for the package's whole
// run and the process exit releases it. Closing it in a t.Cleanup would free it
// underneath whichever test happened to finish first.
func buildHarness() (*harness, string, error) {
	raw, err := os.ReadFile(filepath.FromSlash(corpusPath))
	if err != nil {
		return nil, fmt.Sprintf("regression corpus is absent (%v); skipping", err), nil
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", corpusPath, err)
	}

	model := embed.BGESmallEN
	ctx := context.Background()
	// The model slot is taken for the whole package run and released by the
	// process exiting, which is the same lifetime the embedder itself has: this
	// harness is built once and every test in the package queries it. Releasing
	// it earlier would let a second test binary load a model while this one is
	// still holding its own.
	embedder, _, err := embedtest.OpenModel(ctx)
	if errors.Is(err, embedtest.ErrNoModel) {
		return nil, fmt.Sprintf("%v; skipping the recall regression", err), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("open embedder: %w", err)
	}

	h := &harness{
		fixture:  f,
		byHandle: make(map[string]fixtureRecord, len(f.Records)),
		density:  make(map[string]int, len(f.Queries)),
	}
	idx := index.New(model.Name, embedder.Dim())
	catalog := staticCatalog{
		meta:       make(map[record.ID]recall.Meta, len(f.Records)),
		superseded: make(map[record.ID]bool),
	}
	fetcher := staticFetcher{bodies: make(map[record.ID]*record.Memory, len(f.Records))}

	store, err := local.Open(filepath.Join(lexicalDir, "lexical.db"))
	if err != nil {
		return nil, "", fmt.Errorf("open lexical store: %w", err)
	}

	texts := make([]string, len(f.Records))
	for i, r := range f.Records {
		h.byHandle[r.ID] = r
		memory := &record.Memory{
			ID: handleID(r.ID), Kind: record.KindMemory, Type: r.Type,
			SchemaVersion: record.SchemaVersion, Version: 1,
			Statement: r.Statement, Context: r.Context, Tags: r.Tags,
			CreatedAt: record.Now(), UpdatedAt: record.Now(),
		}
		texts[i] = memory.IndexText()
		fetcher.bodies[memory.ID] = memory
		catalog.meta[memory.ID] = recall.Meta{Type: r.Type, Tags: r.Tags}
		if r.Supersedes != "" {
			catalog.superseded[handleID(r.Supersedes)] = true
		}
		if err := store.PutRankingMeta(local.RankingMeta{
			ID: memory.ID, Type: memory.Type, Tags: memory.Tags,
			Text: local.LexicalText(memory),
		}); err != nil {
			return nil, "", fmt.Errorf("index %s lexically: %w", r.ID, err)
		}
	}
	h.lexical = store

	start := time.Now()
	vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return nil, "", fmt.Errorf("embed corpus: %w", err)
	}
	h.embedFor = time.Since(start)
	h.perDoc = h.embedFor / time.Duration(len(texts))

	docVector := make(map[string][]float32, len(f.Records))
	for i, r := range f.Records {
		if err := idx.Add(handleID(r.ID), model.Name, vectors[i]); err != nil {
			return nil, "", fmt.Errorf("index %s: %w", r.ID, err)
		}
		docVector[r.ID] = vectors[i]
	}
	h.pipeline = recall.New(embedder, idx, fetcher, catalog, store)
	if err := h.measureDensity(ctx, embedder, docVector); err != nil {
		return nil, "", err
	}
	return h, "", nil
}

// measureDensity counts, for each query, how many non-gold records score at
// least as well as a typical correct answer does.
//
// Anchoring on the query's own gold is what makes the number comparable across
// queries of different intrinsic difficulty; a fixed similarity threshold is
// not, because an easy query's answers sit higher than a hard one's.
func (h *harness) measureDensity(ctx context.Context, embedder *embed.Embedder, docVector map[string][]float32) error {
	for _, q := range h.fixture.Queries {
		if !q.Answerable() {
			continue
		}
		queryVector, err := embedder.EmbedOne(ctx, q.Query)
		if err != nil {
			return fmt.Errorf("embed query %s: %w", q.ID, err)
		}
		gold := make(map[string]bool, len(q.Gold))
		similarities := make([]float64, 0, len(q.Gold))
		for _, handle := range q.Gold {
			gold[handle] = true
			similarities = append(similarities, cosine(queryVector, docVector[handle]))
		}
		sort.Float64s(similarities)
		median := similarities[len(similarities)/2]

		var near int
		for handle, vector := range docVector {
			if gold[handle] {
				continue
			}
			if cosine(queryVector, vector) >= median {
				near++
			}
		}
		h.density[q.ID] = near
	}
	return nil
}

func cosine(a, b []float32) float64 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return float64(sum)
}

// An outcome is one query's result under one arm.
type outcome struct {
	query    fixtureQuery
	firstAt  int // rank of the first gold, 0 if none in the returned depth
	returned int
	density  int
	shape    string
}

// metrics are what the harness reports on every run.
type metrics struct {
	Queries int
	HitAt   map[int]int
	MRR     float64
}

func (m metrics) hit(k int) float64 {
	if m.Queries == 0 {
		return 0
	}
	return float64(m.HitAt[k]) / float64(m.Queries)
}

func (m metrics) mrr() float64 {
	if m.Queries == 0 {
		return 0
	}
	return m.MRR / float64(m.Queries)
}

func aggregate(outcomes []outcome) metrics {
	m := metrics{HitAt: map[int]int{}}
	for _, o := range outcomes {
		m.Queries++
		for _, k := range evalKs {
			if o.firstAt > 0 && o.firstAt <= k {
				m.HitAt[k]++
			}
		}
		if o.firstAt > 0 {
			m.MRR += 1 / float64(o.firstAt)
		}
	}
	return m
}

// run executes every answerable query under one request shape.
func (h *harness) run(t *testing.T, name string, build func(fixtureQuery) recall.Request) []outcome {
	t.Helper()
	ctx := context.Background()
	out := make([]outcome, 0, len(h.fixture.Queries))

	for _, q := range h.fixture.Queries {
		if !q.Answerable() {
			continue
		}
		req := build(q)
		req.Query = q.Query
		if req.Limit == 0 {
			req.Limit = 10
		}
		result, err := h.pipeline.Run(ctx, req)
		if err != nil {
			t.Fatalf("%s: query %s: %v", name, q.ID, err)
		}
		gold := make(map[string]bool, len(q.Gold))
		for _, handle := range q.Gold {
			gold[handle] = true
		}
		o := outcome{query: q, returned: len(result.Hits), density: h.density[q.ID]}
		for rank, hit := range result.Hits {
			if gold[fixtureHandle(hit.Memory.ID)] {
				o.firstAt = rank + 1
				break
			}
		}
		// A query's shape is the shape of the sub-vault its answers live in.
		o.shape = h.byHandle[q.Gold[0]].Shape
		out = append(out, o)
	}
	return out
}

func filterOf(q fixtureQuery) recall.Filter {
	return recall.Filter{Tags: q.Filter.Tags, Types: q.Filter.Types}
}

// TestRecallRegression is the number that has to be watched.
//
// Recall is the part of this system that degrades without anything failing: a
// model change, a field-policy change or a change to the filter weights breaks
// no test and can still make the right record stop coming back. This reports
// hit@k and MRR on every run so that shows up.
//
// It reports two axes rather than one aggregate, because one is not enough.
// Per-query near-neighbour density is what exposes the unevenness an aggregate
// conceals, the crowded half of a vault scores far below the sparse half, and
// vault shape is what density does not account for, since a same-subject
// neighbour carries the answer's own tags and the filter cannot separate them
// while a neighbour from elsewhere at the same similarity is removed.
func TestRecallRegression(t *testing.T) {
	h := newHarness(t)

	unfiltered := h.run(t, "unfiltered", func(fixtureQuery) recall.Request { return recall.Request{} })
	filtered := h.run(t, "filtered", func(q fixtureQuery) recall.Request {
		return recall.Request{Filter: filterOf(q)}
	})

	base, withFilter := aggregate(unfiltered), aggregate(filtered)

	t.Logf("corpus %d records, %d answerable queries; one query is worth %.3f of hit@5",
		len(h.fixture.Records), base.Queries, 1/float64(base.Queries))
	t.Logf("embedding: %v for %d records, %v per record",
		h.embedFor.Round(time.Millisecond), len(h.fixture.Records), h.perDoc.Round(time.Millisecond))
	t.Logf("%-12s | %6s %6s %6s %6s | %6s", "arm", "hit@1", "hit@3", "hit@5", "hit@10", "MRR")
	for _, arm := range []struct {
		name string
		m    metrics
	}{{"unfiltered", base}, {"soft filter", withFilter}} {
		t.Logf("%-12s | %6.3f %6.3f %6.3f %6.3f | %6.3f", arm.name,
			arm.m.hit(1), arm.m.hit(3), arm.m.hit(5), arm.m.hit(10), arm.m.mrr())
	}

	reportByDensity(t, unfiltered, filtered)
	reportByShape(t, unfiltered, filtered)

	// The bar the harness enforces is a floor, not the measured level. It exists
	// to catch a regression; the corpus is 20 queries, so it cannot defend a
	// number to three decimals and must not pretend to.
	const floor = 0.70
	if got := withFilter.hit(5); got < floor {
		t.Errorf("hit@5 with soft filtering is %.3f, below the %.2f floor", got, floor)
	}
	if withFilter.hit(5) < base.hit(5) {
		t.Errorf("soft filtering made hit@5 worse: %.3f filtered against %.3f unfiltered",
			withFilter.hit(5), base.hit(5))
	}
}

// reportByDensity is the axis an aggregate hides. Queries are split at the
// corpus's median so the two populations are directly comparable.
func reportByDensity(t *testing.T, unfiltered, filtered []outcome) {
	t.Helper()
	ranked := append([]outcome(nil), filtered...)
	sort.Slice(ranked, func(a, b int) bool { return ranked[a].density > ranked[b].density })
	if len(ranked) < 4 {
		return
	}
	half := len(ranked) / 2
	byQuery := map[string]outcome{}
	for _, o := range unfiltered {
		byQuery[o.query.ID] = o
	}

	t.Logf("%-14s | %5s %9s | %11s %11s", "density half", "n", "range", "hit@5 unfilt", "hit@5 filt")
	for _, part := range []struct {
		name string
		set  []outcome
	}{{"crowded", ranked[:half]}, {"sparse", ranked[half:]}} {
		var base []outcome
		for _, o := range part.set {
			base = append(base, byQuery[o.query.ID])
		}
		lo, hi := part.set[len(part.set)-1].density, part.set[0].density
		t.Logf("%-14s | %5d %4d-%4d | %11.3f %11.3f",
			part.name, len(part.set), lo, hi,
			aggregate(base).hit(5), aggregate(part.set).hit(5))
	}
}

// reportByShape is the axis density does not account for.
func reportByShape(t *testing.T, unfiltered, filtered []outcome) {
	t.Helper()
	byQuery := map[string]outcome{}
	for _, o := range unfiltered {
		byQuery[o.query.ID] = o
	}
	t.Logf("%-14s | %5s %9s | %11s %11s", "vault shape", "n", "mean dens", "hit@5 unfilt", "hit@5 filt")
	for _, shape := range []string{shapeDominant, shapeLongTail} {
		var part, base []outcome
		var density int
		for _, o := range filtered {
			if o.shape != shape {
				continue
			}
			part = append(part, o)
			base = append(base, byQuery[o.query.ID])
			density += o.density
		}
		if len(part) == 0 {
			continue
		}
		t.Logf("%-14s | %5d %9.1f | %11.3f %11.3f", shape, len(part),
			float64(density)/float64(len(part)), aggregate(base).hit(5), aggregate(part).hit(5))
	}
}

// A wrong filter must cost ranking quality and never the answer. This is the
// same property the unit tests lock, measured end to end against the corpus and
// a real embedder rather than against a synthetic candidate pool.
func TestAWrongFilterNeverEmptiesResultsAgainstTheCorpus(t *testing.T) {
	h := newHarness(t)

	wrong := recall.Filter{
		Tags:  []string{"aviation", "knitting", "mycology"},
		Types: []record.Type{record.TypeDoc},
	}
	unfiltered := h.run(t, "unfiltered", func(fixtureQuery) recall.Request { return recall.Request{} })
	misfiltered := h.run(t, "wrong filter", func(fixtureQuery) recall.Request {
		return recall.Request{Filter: wrong}
	})

	for i := range misfiltered {
		if misfiltered[i].returned != unfiltered[i].returned {
			t.Fatalf("query %s returned %d results under a wrong filter and %d without one",
				misfiltered[i].query.ID, misfiltered[i].returned, unfiltered[i].returned)
		}
		if misfiltered[i].returned == 0 {
			t.Fatalf("query %s returned nothing", misfiltered[i].query.ID)
		}
	}

	base, wrongArm := aggregate(unfiltered), aggregate(misfiltered)
	t.Logf("hit@5 unfiltered %.3f, under a wholly wrong filter %.3f (%s of queries kept an answer)",
		base.hit(5), wrongArm.hit(5), percent(wrongArm.HitAt[5], wrongArm.Queries))
	if wrongArm.hit(5) < base.hit(5) {
		t.Errorf("a wrong filter cost hit@5: %.3f against %.3f unfiltered", wrongArm.hit(5), base.hit(5))
	}
}

// A superseded record is not the current answer, and the record that replaced it
// is. Measured against the corpus, where one record supersedes another.
func TestSupersededRecordsAreNotTheCurrentAnswer(t *testing.T) {
	h := newHarness(t)

	var replaced, replacement string
	for _, r := range h.fixture.Records {
		if r.Supersedes != "" {
			replaced, replacement = r.Supersedes, r.ID
			break
		}
	}
	if replaced == "" {
		t.Skip("the corpus holds no supersession")
	}

	ctx := context.Background()
	query := "how many application servers are we running"

	current, err := h.pipeline.Run(ctx, recall.Request{Query: query, Limit: 10})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, hit := range current.Hits {
		if fixtureHandle(hit.Memory.ID) == replaced {
			t.Fatalf("the superseded record %s came back as a current answer", replaced)
		}
	}
	if current.SupersededHidden == 0 {
		t.Fatal("nothing was reported as held back")
	}

	history, err := h.pipeline.Run(ctx, recall.Request{Query: query, Limit: 10, IncludeSuperseded: true})
	if err != nil {
		t.Fatalf("recall history: %v", err)
	}
	var foundOld, foundNew bool
	for _, hit := range history.Hits {
		switch fixtureHandle(hit.Memory.ID) {
		case replaced:
			foundOld = true
		case replacement:
			foundNew = true
		}
	}
	if !foundOld {
		t.Fatalf("the superseded record %s was not retrievable as history", replaced)
	}
	if !foundNew {
		t.Fatalf("the replacement %s did not come back either", replacement)
	}
}

// Recall has to stay interactive with the real embedder in the path. The query
// embedding dominates and is the only part that scales with nothing.
func TestRecallLatencyStaysInteractive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var embedFor, searchFor, total time.Duration
	const runs = 5
	for range runs {
		for _, q := range h.fixture.Queries {
			if !q.Answerable() {
				continue
			}
			start := time.Now()
			result, err := h.pipeline.Run(ctx, recall.Request{
				Query: q.Query, Filter: filterOf(q), Limit: 5,
			})
			if err != nil {
				t.Fatalf("query %s: %v", q.ID, err)
			}
			total += time.Since(start)
			embedFor += result.EmbedFor
			searchFor += result.SearchFor
		}
	}
	queries := runs * countAnswerable(h.fixture)
	t.Logf("recall over %d records, mean of %d queries: %v total = %v embed + %v search + fetch",
		len(h.fixture.Records), queries,
		(total / time.Duration(queries)).Round(time.Millisecond),
		(embedFor / time.Duration(queries)).Round(time.Millisecond),
		(searchFor / time.Duration(queries)).Round(time.Microsecond))

	// Interactive, generously bounded. The measured figure is logged above and
	// is what should be quoted; this only fails if something has gone badly
	// wrong, such as the query being embedded more than once.
	const budget = 2 * time.Second
	if mean := total / time.Duration(queries); mean > budget {
		t.Errorf("mean recall took %v, over the %v interactive budget", mean, budget)
	}
}

func countAnswerable(f fixture) int {
	var n int
	for _, q := range f.Queries {
		if q.Answerable() {
			n++
		}
	}
	return n
}

// TestFilterWeightSweep is where the shipped weights come from.
//
// The weights are stated against whatever the ranker scores in, and that has
// changed once already: these constants were an order of magnitude larger when
// ranking was cosine alone, and had to be re-derived when rank fusion replaced
// that scale. A number that means "half again" on one scale means something
// else entirely on another, so the sweep is re-run rather than the constant
// re-used.
//
// The weight is NOT chosen by maximising the score under a correct filter. That
// number rises monotonically with the weight, and it rises because a large
// enough boost swamps similarity entirely, which is hard filtering wearing a
// different name. The whole reason for soft filtering is what happens when the
// filter is wrong, so the sweep reports three agents at every weight and the
// choice is the largest boost that still costs a wrong filter nothing.
func TestFilterWeightSweep(t *testing.T) {
	h := newHarness(t)
	base := aggregate(h.run(t, "unfiltered", func(fixtureQuery) recall.Request {
		return recall.Request{}
	}))

	// Three agents. "Correct" is the filter written from the query text.
	// "One wrong" replaces a single tag with one from elsewhere in the vault,
	// which is the ordinary mistake that turned hard filtering into a cliff.
	// "All wrong" is the pathological case.
	agents := []struct {
		name string
		of   func(fixtureQuery) recall.Filter
	}{
		{"correct", filterOf},
		{"one wrong", func(q fixtureQuery) recall.Filter {
			f := filterOf(q)
			if len(f.Tags) == 0 {
				return f
			}
			tags := append([]string(nil), f.Tags...)
			tags[len(tags)-1] = "mycology"
			return recall.Filter{Tags: tags, Types: f.Types}
		}},
		{"all wrong", func(fixtureQuery) recall.Filter {
			return recall.Filter{
				Tags:  []string{"aviation", "knitting", "mycology"},
				Types: []record.Type{record.TypeDoc},
			}
		}},
	}

	t.Logf("%-16s | %-10s | %6s %6s %6s | %6s", "tag/type weight", "agent", "hit@1", "hit@3", "hit@5", "MRR")
	t.Logf("%-16s | %-10s | %6.3f %6.3f %6.3f | %6.3f", "none", "-",
		base.hit(1), base.hit(3), base.hit(5), base.mrr())

	for _, w := range []recall.Weights{
		{Tag: 0.0005, Type: 0.00017},
		{Tag: 0.001, Type: 0.00033},
		{Tag: 0.002, Type: 0.00067},
		{Tag: 0.004, Type: 0.0013},
		{Tag: 0.008, Type: 0.0027},
		{Tag: 0.02, Type: 0.0067},
	} {
		for _, agent := range agents {
			m := aggregate(h.run(t, agent.name, func(q fixtureQuery) recall.Request {
				return recall.Request{Filter: agent.of(q), Weights: w}
			}))
			t.Logf("%-16s | %-10s | %6.3f %6.3f %6.3f | %6.3f",
				fmt.Sprintf("%.3f / %.3f", w.Tag, w.Type), agent.name,
				m.hit(1), m.hit(3), m.hit(5), m.mrr())
		}
	}
}

// TestLexicalWeightSweep is where the shipped lexical weight comes from, and
// the reason it is not the weight that maximises hit@5.
//
// The queries a term index helps and the queries that prove semantic recall are
// disjoint. A query sharing no content word with its answer cannot be helped by
// BM25 and is actively harmed by it, because BM25 still ranks something and
// fusion still promotes what it ranks. Those queries are a minority of any
// labelled set, so a weight tuned on the aggregate degrades them while the
// average improves, which is the one outcome a memory store cannot accept,
// since retrieving a record that shares no words with the question is the thing
// it exists to do.
//
// So the two populations are reported separately and never as one number. The
// criterion is the largest weight that costs the zero-overlap population
// nothing.
func TestLexicalWeightSweep(t *testing.T) {
	h := newHarness(t)

	zero := h.zeroOverlapQueries()
	t.Logf("%d of %d answerable queries share no indexed term with any of their gold records",
		len(zero), countAnswerable(h.fixture))
	if len(zero) == 0 {
		t.Skip("the corpus has no zero-lexical-overlap query, so the criterion cannot be applied")
	}

	only := func(want bool) func(outcome) bool {
		return func(o outcome) bool { return zero[o.query.ID] == want }
	}
	t.Logf("%-12s | %6s %6s %6s %6s | %6s | %-13s | %-13s",
		"lex weight", "hit@1", "hit@3", "hit@5", "hit@10", "MRR", "zero-overlap", "has-overlap")

	for _, weight := range []float32{-1, 0.01, 0.02, 0.03, 0.05, 0.10, 0.50, 1.00} {
		outcomes := h.run(t, "lexical", func(fixtureQuery) recall.Request {
			return recall.Request{LexicalWeight: weight}
		})
		all := aggregate(outcomes)
		z := aggregate(filterOutcomes(outcomes, only(true)))
		n := aggregate(filterOutcomes(outcomes, only(false)))
		label := fmt.Sprintf("%.3f", weight)
		if weight < 0 {
			label = "dense-only"
		}
		t.Logf("%-12s | %6.3f %6.3f %6.3f %6.3f | %6.3f | %6.3f (n=%2d) | %6.3f (n=%2d)",
			label, all.hit(1), all.hit(3), all.hit(5), all.hit(10), all.mrr(),
			z.hit(5), z.Queries, n.hit(5), n.Queries)
	}
}

// The property the shipped weight was chosen for, enforced rather than logged.
// A query whose answer shares no word with it must rank no worse under fusion
// than it did under the vector pass alone.
func TestFusionDoesNotDegradeSemanticOnlyQueries(t *testing.T) {
	h := newHarness(t)

	zero := h.zeroOverlapQueries()
	if len(zero) == 0 {
		t.Skip("the corpus has no zero-lexical-overlap query")
	}
	only := func(o outcome) bool { return zero[o.query.ID] }

	dense := filterOutcomes(h.run(t, "dense-only", func(fixtureQuery) recall.Request {
		return recall.Request{LexicalWeight: -1}
	}), only)
	shipped := filterOutcomes(h.run(t, "shipped", func(fixtureQuery) recall.Request {
		return recall.Request{}
	}), only)

	base, fused := aggregate(dense), aggregate(shipped)
	t.Logf("%d zero-overlap queries: hit@5 %.3f dense-only against %.3f fused, MRR %.3f against %.3f",
		base.Queries, base.hit(5), fused.hit(5), base.mrr(), fused.mrr())
	if fused.hit(5) < base.hit(5) {
		t.Errorf("fusion cost the zero-lexical-overlap queries hit@5: %.3f against %.3f dense-only",
			fused.hit(5), base.hit(5))
	}
}

// zeroOverlapQueries names the queries no lexical pass can help: those where no
// gold record shares a single indexed term with the query.
func (h *harness) zeroOverlapQueries() map[string]bool {
	out := map[string]bool{}
	for _, q := range h.fixture.Queries {
		if !q.Answerable() {
			continue
		}
		terms := map[string]bool{}
		for _, term := range local.Terms(q.Query) {
			terms[term] = true
		}
		overlap := false
		for _, handle := range q.Gold {
			gold := h.byHandle[handle]
			memory := &record.Memory{
				Statement: gold.Statement, Context: gold.Context, Tags: gold.Tags,
			}
			for _, term := range local.Terms(local.LexicalText(memory)) {
				if terms[term] {
					overlap = true
					break
				}
			}
			if overlap {
				break
			}
		}
		out[q.ID] = !overlap
	}
	// Only the zero-overlap ones are of interest; the rest are noise here.
	for id, isZero := range out {
		if !isZero {
			delete(out, id)
		}
	}
	return out
}

func filterOutcomes(outcomes []outcome, keep func(outcome) bool) []outcome {
	var out []outcome
	for _, o := range outcomes {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

// The floor soft filtering exists to provide: however wrong the filter, the
// ranking cannot fall below what no filter at all would have produced. Hard
// filtering has no such floor, one wrong tag measured 0.949 down to 0.729,
// below the unfiltered baseline, with a sixth of queries returning nothing.
func TestSoftFilteringHasAFloor(t *testing.T) {
	h := newHarness(t)
	base := aggregate(h.run(t, "unfiltered", func(fixtureQuery) recall.Request {
		return recall.Request{}
	}))

	for _, agent := range []struct {
		name string
		of   func(fixtureQuery) recall.Filter
	}{
		{"one tag wrong", func(q fixtureQuery) recall.Filter {
			f := filterOf(q)
			if len(f.Tags) == 0 {
				return f
			}
			tags := append([]string(nil), f.Tags...)
			tags[len(tags)-1] = "mycology"
			return recall.Filter{Tags: tags, Types: f.Types}
		}},
		{"every tag wrong", func(fixtureQuery) recall.Filter {
			return recall.Filter{Tags: []string{"aviation", "knitting", "mycology"}}
		}},
		{"wrong type", func(fixtureQuery) recall.Filter {
			return recall.Filter{Types: []record.Type{record.TypeDoc}}
		}},
	} {
		m := aggregate(h.run(t, agent.name, func(q fixtureQuery) recall.Request {
			return recall.Request{Filter: agent.of(q)}
		}))
		t.Logf("%-16s hit@5 %.3f, MRR %.3f (unfiltered %.3f / %.3f)",
			agent.name, m.hit(5), m.mrr(), base.hit(5), base.mrr())
		if m.hit(5) < base.hit(5) {
			t.Errorf("%s dropped hit@5 to %.3f, below the unfiltered %.3f",
				agent.name, m.hit(5), base.hit(5))
		}
	}
}
