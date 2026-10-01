package vault

import (
	"fmt"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// putVector adds a vector to the searchable index and to the durable one.
//
// The order matters in one direction only: the on-disk append happens first, so
// a crash between the two leaves a vector that is durable but not yet loaded,
// which the next open fixes for free. The reverse order would leave a vault that
// searches a record this process can find and no later process can.
func (v *Vault) putVector(id record.ID, vector []float32) error {
	entry := index.Entry{
		ID:     id,
		Model:  v.opts.Model.Name,
		Dim:    v.opts.Model.Dim,
		Vector: vector,
	}
	if err := v.vectors.Append(entry); err != nil {
		return err
	}
	if err := v.index.Add(id, entry.Model, vector); err != nil {
		return err
	}
	v.health.Indexed++
	return v.compactVectorsIfDue()
}

// removeVector takes a record out of the durable index and then out of the
// searchable one.
//
// Both halves are needed. The searchable index is loaded once, when the vault
// opens, so a vector removed only from disk goes on answering queries in this
// process for a record nothing else holds any more, until something reopens.
//
// The durable half goes first, as it does in putVector and for the same reason:
// a removal that fails to reach disk leaves the record searchable here and in
// every later process alike. The other order would hide it from this process
// alone, and the next open would bring it back.
func (v *Vault) removeVector(id record.ID) error {
	if err := v.vectors.Remove(id); err != nil {
		return err
	}
	if v.index.Remove(id) {
		v.health.Indexed--
	}
	return nil
}

// compactVectorsIfDue folds the deltas into a new base once they have outgrown
// it, at the ratio the catalog uses.
//
// The base is written from the files, which is what the next open would
// hydrate, and not from the in-memory index, which leaves out any vector from
// another model. Reading them and writing it is one call to the store, because
// a vector added or removed by another call in between would otherwise be lost
// with the delta. See index.Store.CompactIfDue.
func (v *Vault) compactVectorsIfDue() error {
	if _, err := v.vectors.CompactIfDue(); err != nil {
		return fmt.Errorf("compact index: %w", err)
	}
	return nil
}

// VectorStats reports what the persisted index has cost on disk.
func (v *Vault) VectorStats() index.Stats {
	if v.vectors == nil {
		return index.Stats{}
	}
	return v.vectors.Stats()
}

// CompactIndex folds the index deltas into a fresh base, whether or not they
// have reached the ratio that would trigger it on their own.
func (v *Vault) CompactIndex() error {
	if v.vectors == nil {
		return nil
	}
	return v.vectors.Fold()
}
