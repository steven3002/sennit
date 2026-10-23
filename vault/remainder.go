package vault

import (
	"errors"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A Remainder is what this device holds under one record id besides a catalog
// entry and a place in the upload queue.
//
// Those two are what say a record is stored, and a record that has neither has
// usually been forgotten. Not always: a remember that stops part way leaves what
// it wrote before it queued the record, the body and then the vector and the
// ranking rows, and a forget that stops part way leaves whatever it had not
// reached once the catalog entry had gone. What either leaves can still be read,
// listed or recalled. So a caller that means to say nothing of a record is here,
// such as one answering a forget the vault refused with the catalog's
// ErrNotFound, asks here first.
type Remainder struct {
	// Kind is the class of record what is held belongs to, as it was stored: a
	// conversation's when its head is here, and otherwise the class its ranking
	// rows or its body were stored under. It is empty when nothing is held, and
	// when a vector is all there is, because a vector does not say what it was
	// made from.
	Kind record.Kind
	// Body is the record's body, and Rows its ranking rows, which list it and
	// match it by its words. Vector is its vector in the searchable index. Head
	// is a conversation's head.
	Body, Rows, Vector, Head bool
}

// Empty reports whether nothing at all is held under the id.
func (r Remainder) Empty() bool { return !r.Body && !r.Rows && !r.Vector && !r.Head }

// Remainder reports what this device holds under id besides a catalog entry and
// a place in the upload queue. It changes nothing.
//
// The vector is looked for in the searchable index, which is what recall ranks
// from, and not in the files that persist it, which can answer for one record
// only by being read whole. The index is loaded from those files when the vault
// opens and follows every vector this process writes or removes. A vector from
// another model stays in the files alone, cannot be searched, and is not counted
// here.
func (v *Vault) Remainder(id record.ID) (Remainder, error) {
	var held Remainder
	if _, err := v.local.GetSessionHead(id); err == nil {
		held.Head, held.Kind = true, record.KindSession
	} else if !errors.Is(err, local.ErrNotFound) {
		return Remainder{}, err
	}
	if meta, err := v.local.RankingMetaFor(id); err == nil {
		held.Rows = true
		if held.Kind == "" {
			held.Kind = meta.Kind
		}
	} else if !errors.Is(err, local.ErrNotFound) {
		return Remainder{}, err
	}
	if kind, _, err := v.local.GetRecord(id); err == nil {
		held.Body = true
		if held.Kind == "" {
			held.Kind = kind
		}
	} else if !errors.Is(err, local.ErrNotFound) {
		return Remainder{}, err
	}
	held.Vector = v.index.Has(id)
	return held, nil
}
