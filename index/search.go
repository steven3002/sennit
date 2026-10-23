package index

import (
	"container/heap"
	"fmt"

	"github.com/steven3002/sennit/record"
)

// A Match is one record and how close it sits to the query.
type Match struct {
	ID    record.ID
	Score float32
}

// Search returns the k nearest records to the query vector, best first.
//
// The whole search runs here, before anything is fetched. By the time the
// network is touched the exact records are already known, so the indexer never
// learns what was asked for and there is no download-and-check loop.
func (i *Index) Search(query []float32, k int) ([]Match, error) {
	if len(query) != i.dim {
		return nil, fmt.Errorf("index holds %d-dimension vectors, query has %d", i.dim, len(query))
	}
	if k <= 0 {
		return nil, nil
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	// A bounded min-heap keeps the cost of ranking proportional to the number
	// of results asked for rather than to the size of the vault.
	best := make(matchHeap, 0, k+1)
	for n, id := range i.ids {
		score := dot(query, i.vectors[n*i.dim:(n+1)*i.dim])
		if len(best) < k {
			heap.Push(&best, Match{ID: id, Score: score})
			continue
		}
		if score > best[0].Score {
			best[0] = Match{ID: id, Score: score}
			heap.Fix(&best, 0)
		}
	}

	out := make([]Match, len(best))
	for n := len(out) - 1; n >= 0; n-- {
		out[n] = heap.Pop(&best).(Match)
	}
	return out, nil
}

// SearchWithin returns the k nearest records that within accepts, and the k
// nearest of every record, each best first.
//
// A search that only some records may answer has to reach past every nearer
// record that may not. Search cannot do that from outside: once its k nearest
// are all records the caller does not want, nothing the caller does want is
// left to choose from. So the records outside are kept out of the heap rather
// than out of the answer, and the k nearest that remain are found however many
// nearer ones there are.
//
// The k nearest of every record come back with them because a caller who
// confined a search still has to be able to say what the confinement left out,
// and the vectors are scored once for both. Each heap is kept exactly as Search
// keeps its one, so the second answer is what Search returns, and keeping it
// costs a comparison per vector rather than a second pass over the matrix.
func (i *Index) SearchWithin(query []float32, k int, within func(record.ID) bool) (among, all []Match, err error) {
	if len(query) != i.dim {
		return nil, nil, fmt.Errorf("index holds %d-dimension vectors, query has %d", i.dim, len(query))
	}
	if k <= 0 {
		return nil, nil, nil
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	keep := func(best *matchHeap, match Match) {
		if len(*best) < k {
			heap.Push(best, match)
			return
		}
		if match.Score > (*best)[0].Score {
			(*best)[0] = match
			heap.Fix(best, 0)
		}
	}
	inside := make(matchHeap, 0, k+1)
	every := make(matchHeap, 0, k+1)
	for n, id := range i.ids {
		match := Match{ID: id, Score: dot(query, i.vectors[n*i.dim:(n+1)*i.dim])}
		keep(&every, match)
		if within(id) {
			keep(&inside, match)
		}
	}
	return drain(inside), drain(every), nil
}

// drain empties a heap into a slice, best first.
func drain(best matchHeap) []Match {
	out := make([]Match, len(best))
	for n := len(out) - 1; n >= 0; n-- {
		out[n] = heap.Pop(&best).(Match)
	}
	return out
}

// dot is cosine similarity for unit vectors, which is what the embedder emits.
func dot(a, b []float32) float32 {
	var sum float32
	for n := range a {
		sum += a[n] * b[n]
	}
	return sum
}

type matchHeap []Match

func (h matchHeap) Len() int           { return len(h) }
func (h matchHeap) Less(a, b int) bool { return h[a].Score < h[b].Score }
func (h matchHeap) Swap(a, b int)      { h[a], h[b] = h[b], h[a] }
func (h *matchHeap) Push(x any)        { *h = append(*h, x.(Match)) }
func (h *matchHeap) Pop() (popped any) {
	old := *h
	popped, *h = old[len(old)-1], old[:len(old)-1]
	return popped
}
