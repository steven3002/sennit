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

// compactVectorsIfDue folds the deltas into a new base once they have outgrown
// it, at the ratio the catalog uses.
//
// The base is written from the in-memory index rather than by re-reading the
// files, because the in-memory index is what hydration would have produced and
// is already correct. Vectors removed since the last base are simply not in it.
func (v *Vault) compactVectorsIfDue() error {
	if !v.vectors.DueForCompaction() {
		return nil
	}
	entries, err := v.vectors.Hydrate()
	if err != nil {
		return fmt.Errorf("compact index: %w", err)
	}
	return v.vectors.Compact(entries)
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
	entries, err := v.vectors.Hydrate()
	if err != nil {
		return err
	}
	return v.vectors.Compact(entries)
}
