// Package rename puts a finished file in place of another by renaming it over
// the old one, which is how the catalog's snapshot, the index's base and each
// file of a model are installed: written whole under a name of their own, and
// then given the name they are read by in one step.
//
// The step is one call wherever a file that is open can be renamed over. On
// Windows it cannot. A file somebody has open keeps its name until they close
// it, whatever sharing they opened it with, and the rename is refused. The
// processes that share a vault keep out of each other's way with a lock, but
// they are not the only readers a file has there: a virus scanner, a search
// indexer or a backup opens a file for a moment after it changes, and a rename
// that lands in that moment would fail a write that had nothing wrong with it.
// So there the rename is tried again for a short while before the refusal is
// taken for an answer.
package rename

// Over renames from to to, replacing a file already there.
//
// It is as atomic as the platform's rename: a reader finds the old file or the
// new one under the name, and never part of either.
func Over(from, to string) error { return over(from, to) }
