// Package index holds vectors and returns the nearest ones.
package index

import (
	"errors"
	"fmt"
	"sync"

	"github.com/steven3002/sennit/record"
)

// An Index is a flat matrix of unit vectors with the record ids beside them.
//
// It is owned rather than depended upon. The two candidate libraries were
// unsuitable for opposite reasons, one abandoned, the other requiring the
// native toolchain the build deliberately avoids, and the structure a flat
// search needs is a slice and a loop.
type Index struct {
	mu      sync.RWMutex
	dim     int
	model   string
	ids     []record.ID
	vectors []float32
	at      map[record.ID]int
}

// New builds an empty index for vectors of the given width.
func New(model string, dim int) *Index {
	return &Index{dim: dim, model: model, at: make(map[record.ID]int)}
}

// Dim reports the vector width.
func (i *Index) Dim() int { return i.dim }

// Model reports which model produced the vectors held here.
func (i *Index) Model() string { return i.model }

// Len reports how many vectors are held.
func (i *Index) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.ids)
}

// ErrForeignModel reports a vector produced by a model other than this index's.
//
// Two models embed into different spaces, so the cosine between their vectors is
// a number with no meaning, and, being a number, it ranks perfectly happily.
// That is the failure this guards: not a crash, but a silently wrong order.
var ErrForeignModel = errors.New("vector was produced by a different embedding model")

// Add stores a vector, replacing any vector already held for the record.
//
// The model is a required argument rather than something the caller is trusted
// to have checked. Every path that puts a vector into an index has to name the
// model that produced it, and an index accepts exactly one, so mixing two is not
// something a caller can do by forgetting.
func (i *Index) Add(id record.ID, model string, vector []float32) error {
	if model != i.model {
		return fmt.Errorf("%w: index holds %q, got %q for %s", ErrForeignModel, i.model, model, id)
	}
	if len(vector) != i.dim {
		return fmt.Errorf("index holds %d-dimension vectors, got %d for %s", i.dim, len(vector), id)
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	if position, ok := i.at[id]; ok {
		copy(i.vectors[position*i.dim:(position+1)*i.dim], vector)
		return nil
	}
	i.at[id] = len(i.ids)
	i.ids = append(i.ids, id)
	i.vectors = append(i.vectors, vector...)
	return nil
}

// Has reports whether a record has a vector in the index.
func (i *Index) Has(id record.ID) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	_, ok := i.at[id]
	return ok
}
