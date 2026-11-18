package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
)

// changedFiles reports which of a vault's shared files it would read again.
func changedFiles(v *Vault) (catalog, vectors bool) {
	return v.manifest.Changed(), v.vectors.Changed()
}

// A change a vault makes itself is never taken for one another process made, so
// a server that refreshes before every answer reads nothing again after its own
// writes, whatever they were.
func TestAVaultReadsNothingAgainAfterItsOwnChanges(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	t.Cleanup(func() { v.Close() })
	quiet := func(after string) {
		t.Helper()
		if catalog, vectors := changedFiles(v); catalog || vectors {
			t.Errorf("after %s, the catalog reads as changed %v and the index %v, want neither", after, catalog, vectors)
		}
	}

	quiet("opening")
	harbour := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	quiet("two remembers")
	saved := saveExchange(t, v, "Tidepool gauge survey", "survey")
	quiet("a save")
	flushQueue(t, v)
	quiet("a flush that catalogued them")
	if err := v.Forget(harbour); err != nil {
		t.Fatalf("forget: %v", err)
	}
	quiet("a forget")
	if err := v.CompactIndex(); err != nil {
		t.Fatalf("compact the index: %v", err)
	}
	quiet("a compaction of the index")
	if err := v.manifest.Compact(); err != nil {
		t.Fatalf("compact the catalog: %v", err)
	}
	quiet("a compaction of the catalog")
	if err := v.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget the conversation: %v", err)
	}
	quiet("a forget of the conversation")
	if err := v.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	quiet("a refresh")
}

// A change another process made goes on being reported through every change
// this process makes after it, an append or a compaction, until a refresh has
// read it, and the refresh brings in both.
//
// A write of this process's own lands on top of the other's, and counting the
// files as seen once it is written would count the other's change as seen too,
// without anything having read it.
func TestAChangeAnotherProcessMadeIsNotHiddenByOneThisProcessMakes(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)
	t.Cleanup(func() { v.Close() })
	other := ledgerVault(t, home)
	t.Cleanup(func() { other.Close() })

	for _, step := range []struct {
		name string
		// theirs is the other process's change, and ours this process's own
		// after it, each returning the record it is about.
		theirs, ours func(t *testing.T) record.ID
		// held reports whether this process holds the record a change was
		// about, once it has refreshed.
		held func(id record.ID) bool
	}{
		{
			name:   "a vector, then one of this process's own",
			theirs: func(t *testing.T) record.ID { return rememberFact(t, other, "The weir gauge reads in metres.", "weir") },
			ours:   func(t *testing.T) record.ID { return rememberFact(t, v, "The lock gauge reads in feet.", "lock") },
			held:   v.index.Has,
		},
		{
			name:   "a vector, then a compaction of this process's",
			theirs: func(t *testing.T) record.ID { return rememberFact(t, other, "The dam gauge reads in inches.", "dam") },
			ours: func(t *testing.T) record.ID {
				if err := v.CompactIndex(); err != nil {
					t.Fatalf("compact the index: %v", err)
				}
				return record.ID{}
			},
			held: v.index.Has,
		},
		{
			name:   "a catalog entry, then one of this process's own",
			theirs: func(t *testing.T) record.ID { return catalogue(t, other) },
			ours:   func(t *testing.T) record.ID { return catalogue(t, v) },
			held:   inCatalog(v),
		},
		{
			name:   "a catalog entry, then a compaction of this process's",
			theirs: func(t *testing.T) record.ID { return catalogue(t, other) },
			ours: func(t *testing.T) record.ID {
				if err := v.manifest.Compact(); err != nil {
					t.Fatalf("compact the catalog: %v", err)
				}
				return record.ID{}
			},
			held: inCatalog(v),
		},
	} {
		t.Run(step.name, func(t *testing.T) {
			if err := v.Refresh(); err != nil {
				t.Fatalf("refresh before: %v", err)
			}
			theirs := step.theirs(t)
			ours := step.ours(t)
			if catalog, vectors := changedFiles(v); !catalog && !vectors {
				t.Fatal("this process's own change hid the other process's from it")
			}
			if err := v.Refresh(); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if !step.held(theirs) {
				t.Error("the refresh did not bring in the other process's change")
			}
			if ours != (record.ID{}) && !step.held(ours) {
				t.Error("the refresh lost this process's own change")
			}
			if catalog, vectors := changedFiles(v); catalog || vectors {
				t.Errorf("after the refresh the catalog reads as changed %v and the index %v", catalog, vectors)
			}
		})
	}
}

// catalogue appends a catalog entry for a new record to v's catalog, as a flush
// does, and returns the record's id.
func catalogue(t testing.TB, v *Vault) record.ID {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	var ref sia.ObjectRef
	copy(ref.ID[:], id[:])
	cid, err := v.sealer.CID([]byte(id.String()))
	if err != nil {
		t.Fatalf("cid: %v", err)
	}
	if err := v.manifest.Append(manifest.Entry{
		ID:        id,
		Kind:      record.KindMemory,
		Version:   1,
		CID:       cid,
		ObjectRef: ref.String(),
		SlabID:    string(probeSlab),
		WrittenAt: record.Now(),
	}); err != nil {
		t.Fatalf("catalogue %s: %v", id, err)
	}
	return id
}

// inCatalog reports whether v's copy of the catalog names a record as held.
func inCatalog(v *Vault) func(record.ID) bool {
	return func(id record.ID) bool {
		_, err := v.manifest.Lookup(id)
		return err == nil
	}
}

// BenchmarkRefresh is what a refresh costs: when nobody else has written, which
// is what a server pays before every answer, and when another process has added
// one record, which reads the catalog and the index again.
func BenchmarkRefresh(b *testing.B) {
	for _, records := range []int{100, 1000} {
		home := b.TempDir()
		v, other := benchVault(b, home), benchVault(b, home)
		fill(b, v, records)

		b.Run(fmt.Sprintf("nothing changed/%d records", records), func(b *testing.B) {
			if err := v.Refresh(); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				if err := v.Refresh(); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("another process added a record/%d records", records), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				fill(b, other, 1)
				b.StartTimer()
				if err := v.Refresh(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// benchVault opens an offline vault at home for a benchmark, and closes it when
// the benchmark ends.
func benchVault(b *testing.B, home string) *Vault {
	b.Helper()
	v, err := Open(context.Background(), Options{
		Home: home, Phrase: ledgerPhrase, Embedder: embedtest.NewStub(), Offline: true,
	})
	if err != nil {
		b.Fatalf("open vault: %v", err)
	}
	b.Cleanup(func() { v.Close() })
	return v
}

// fill gives v n records' worth of catalog entries and vectors, written as a
// flush and an embedding would write them, without a body behind any of them.
func fill(b *testing.B, v *Vault, n int) {
	b.Helper()
	vectors := make([]index.Entry, 0, n)
	for range n {
		id := catalogue(b, v)
		vector, err := v.embedder.EmbedOne(context.Background(), "a record of "+id.String())
		if err != nil {
			b.Fatal(err)
		}
		vectors = append(vectors, index.Entry{ID: id, Model: v.opts.Model.Name, Dim: v.opts.Model.Dim, Vector: vector})
	}
	if err := v.vectors.Append(vectors...); err != nil {
		b.Fatal(err)
	}
	for _, vector := range vectors {
		if err := v.index.Add(vector.ID, vector.Model, vector.Vector); err != nil {
			b.Fatal(err)
		}
	}
}

// A compaction another process made is seen though it leaves the log, or the
// delta, exactly as long as this process last saw it.
//
// This process compacted last, so it saw an empty log. The other process then
// appended and compacted, which empties the log again: its length says nothing
// has changed, and only the snapshot, or the base, put in place since says what
// has.
func TestACompactionAnotherProcessMadeIsSeenWhenTheLogIsAsLongAsItWas(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)
	t.Cleanup(func() { v.Close() })
	other := ledgerVault(t, home)
	t.Cleanup(func() { other.Close() })

	ours := catalogue(t, v)
	vectorOf(t, v, ours)
	if err := v.manifest.Compact(); err != nil {
		t.Fatalf("compact the catalog: %v", err)
	}
	if err := v.CompactIndex(); err != nil {
		t.Fatalf("compact the index: %v", err)
	}
	if catalog, vectors := changedFiles(v); catalog || vectors {
		t.Fatalf("after its own compactions the catalog reads as changed %v and the index %v", catalog, vectors)
	}

	theirs := catalogue(t, other)
	vectorOf(t, other, theirs)
	if err := other.manifest.Compact(); err != nil {
		t.Fatalf("compact the catalog from the other process: %v", err)
	}
	if err := other.CompactIndex(); err != nil {
		t.Fatalf("compact the index from the other process: %v", err)
	}
	for name, file := range map[string]string{"manifest.log": manifest.LogName, "index.delta": index.DeltaName} {
		if size := sizeOn(t, home, file); size != 0 {
			t.Fatalf("%s holds %d bytes after the compaction, want it empty, as this process last saw it", name, size)
		}
	}

	catalog, vectors := changedFiles(v)
	if !catalog || !vectors {
		t.Fatalf("after the other process compacted, the catalog reads as changed %v and the index %v, want both",
			catalog, vectors)
	}
	if err := v.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := v.manifest.Lookup(theirs); err != nil || !v.index.Has(theirs) {
		t.Errorf("the refresh did not bring in what the other process compacted: catalogued %v, indexed %v",
			err == nil, v.index.Has(theirs))
	}
}

// A refresh that meets a change another process is part way through writing
// waits for it, and then reads the whole of it, and neither fails nor takes
// the part written for one a crash cut off.
//
// The other process holds the file lock while it writes, as every append does,
// and has written half of what it is appending when the refresh starts.
func TestARefreshWaitsForAChangeAnotherProcessIsPartWayThroughWriting(t *testing.T) {
	for _, c := range []struct {
		name, file, lock string
		// write makes one change in a vault, so its bytes can be taken from
		// that vault's file, and returns the record it is about.
		write func(t *testing.T, v *Vault) record.ID
		// held reports whether the refreshed vault holds the record.
		held func(v *Vault, id record.ID) bool
	}{
		{
			name: "a catalog line", file: manifest.LogName, lock: manifest.LockName,
			write: func(t *testing.T, v *Vault) record.ID { return catalogue(t, v) },
			held: func(v *Vault, id record.ID) bool {
				_, err := v.manifest.Lookup(id)
				return err == nil
			},
		},
		{
			name: "an index record", file: index.DeltaName, lock: index.LockName,
			write: func(t *testing.T, v *Vault) record.ID {
				id, err := record.NewID()
				if err != nil {
					t.Fatalf("record id: %v", err)
				}
				vectorOf(t, v, id)
				return id
			},
			held: func(v *Vault, id record.ID) bool { return v.index.Has(id) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The bytes the other process appends are what a vault with the
			// same phrase writes, made in a directory of their own.
			elsewhere := t.TempDir()
			writer := ledgerVault(t, elsewhere)
			id := c.write(t, writer)
			writer.Close()
			change, err := os.ReadFile(filepath.Join(elsewhere, c.file))
			if err != nil || len(change) < 2 {
				t.Fatalf("read the change from %s: %d bytes, %v", c.file, len(change), err)
			}

			home := t.TempDir()
			v := ledgerVault(t, home)
			t.Cleanup(func() { v.Close() })

			lock, err := filelock.Open(filepath.Join(home, c.lock))
			if err != nil {
				t.Fatalf("open the lock: %v", err)
			}
			defer lock.Close()
			if err := lock.Lock(); err != nil {
				t.Fatalf("take the lock as the other process: %v", err)
			}
			file, err := os.OpenFile(filepath.Join(home, c.file), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatalf("open %s as the other process: %v", c.file, err)
			}
			defer file.Close()
			half := len(change) / 2
			if _, err := file.Write(change[:half]); err != nil {
				t.Fatalf("write the first half: %v", err)
			}

			done := make(chan error, 1)
			go func() { done <- v.Refresh() }()
			select {
			case err := <-done:
				lock.Unlock()
				t.Fatalf("the refresh finished while the other process was part way through writing: %v", err)
			case <-time.After(100 * time.Millisecond):
			}

			if _, err := file.Write(change[half:]); err != nil {
				t.Fatalf("write the second half: %v", err)
			}
			if err := lock.Unlock(); err != nil {
				t.Fatalf("release the lock: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("the refresh failed on what the other process finished writing: %v", err)
			}
			if !c.held(v, id) {
				t.Error("the refresh did not read the change the other process finished writing")
			}
			if dropped := v.DroppedCatalogChanges(); dropped != 0 {
				t.Errorf("the refresh dropped %d change(s) as cut off by a crash", dropped)
			}
			if catalog, vectors := changedFiles(v); catalog || vectors {
				t.Errorf("after the refresh the catalog reads as changed %v and the index %v", catalog, vectors)
			}
		})
	}
}

// vectorOf gives a record a vector, as an embedding does: on the disk, then in
// the vault's searchable index.
func vectorOf(t *testing.T, v *Vault, id record.ID) {
	t.Helper()
	vector, err := v.embedder.EmbedOne(context.Background(), "a record of "+id.String())
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if err := v.putVector(id, vector); err != nil {
		t.Fatalf("put the vector of %s: %v", id, err)
	}
}

// sizeOn reports how long one of the vault's files in home is.
func sizeOn(t *testing.T, home, name string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(home, name))
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return info.Size()
}
