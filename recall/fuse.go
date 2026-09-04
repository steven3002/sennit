package recall

import (
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// A Lexical ranks records by the words they use.
//
// It returns ids in rank order, best first, and nothing else. Fusion consumes
// ranks rather than scores, so a BM25 score never leaves the index that computed
// it, which is what lets the lexical pass be replaced without renegotiating a
// score scale with the ranker.
type Lexical interface {
	SearchLexical(query string, k int) ([]record.ID, error)
}

// RRFk is the rank-fusion constant. It damps the difference between the top few
// ranks, so fusion rewards a record both passes agree on rather than one that
// either pass put first.
const RRFk = 60.0

// DefaultLexicalWeight is how much say the lexical pass gets, relative to the
// vector pass at 1.0.
//
// It is NOT chosen by maximising hit@5. The queries a term index helps and the
// queries that prove semantic recall are disjoint sets: a query that shares no
// content word with its answer, the case a memory store exists to handle,
// cannot be helped by a lexical signal and is actively harmed by one, because
// BM25 still ranks *something* and fusion still promotes it. An aggregate hides
// that, since the queries term matching fixes outnumber the ones it breaks.
//
// The criterion is the largest weight that costs a zero-lexical-overlap query
// nothing, that population measured on its own. Swept over 59 labelled queries,
// four of which share no indexed term with their answer: those four hold their
// ranking unchanged up to 0.035 and lose an answer out of the top five at 0.040.
// Everything at or below 0.035 leaves hit@5 exactly where the vector pass alone
// puts it and buys precision higher up, at this weight, hit@1 rises 0.627 to
// 0.678 and MRR 0.717 to 0.745.
//
// The shipped value is one probe step inside that boundary rather than on it.
// The cliff is established by a single query in a population of four, which is
// too thin to site a constant exactly on; the gain is flat across the safe band
// and the risk is not symmetric, since the cost of being over the line is the
// query class this product exists for.
//
// Full parity with the vector pass, weight 1.0, the unweighted fusion the
// lexical pass is usually specified with, reaches 0.881 hit@5 against 0.847,
// and takes all four zero-overlap queries out of the top five to do it.
const DefaultLexicalWeight = 0.03

// fuse combines the vector ranking with the lexical ranking into one order.
//
// Reciprocal rank fusion, weighted. A record's score is the sum over the passes
// that retrieved it of weight/(RRFk + rank), which needs no normalisation
// between cosine and BM25 because it never compares their scores, only the
// positions each assigned.
//
// Membership is the union of the two passes, not the vector pass alone. That is
// the whole point of a second retriever: the queries a term index rescues are
// the ones where the vector pass ranked the answer too low to be in its pool at
// all, and intersecting instead of uniting would keep exactly the failures the
// lexical pass exists to fix. A record only the lexical pass found still ranks,
// is still described by the catalog and is still fetched by id like any other.
//
// Order is deterministic: candidates come out in the order the vector pass
// returned them, then the lexical-only ones in lexical rank order, and the sort
// downstream is stable with an id tiebreak. Nothing here depends on map order.
func fuse(matches []index.Match, lexical []record.ID, weight float32) []candidate {
	fused := make([]candidate, 0, len(matches)+len(lexical))
	at := make(map[record.ID]int, len(matches)+len(lexical))

	for i, match := range matches {
		at[match.ID] = len(fused)
		fused = append(fused, candidate{
			ID:         match.ID,
			Similarity: match.Score,
			Fused:      1 / (RRFk + float32(i+1)),
		})
	}

	for rank, id := range lexical {
		// A record the lexical pass returned twice keeps its best position.
		contribution := weight / (RRFk + float32(rank+1))
		if i, ok := at[id]; ok {
			if fused[i].Lexical > 0 {
				continue
			}
			fused[i].Lexical = contribution
			fused[i].Fused += contribution
			continue
		}
		at[id] = len(fused)
		// Similarity stays zero: the vector pass never scored this record, and
		// reporting a similarity it did not compute would be an invention.
		fused = append(fused, candidate{ID: id, Lexical: contribution, Fused: contribution})
	}

	for i := range fused {
		fused[i].Score = fused[i].Fused
	}
	return fused
}
