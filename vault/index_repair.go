package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// The search index is two files of sealed records on this device, and a record
// in either that cannot be read loses the vector it held. A catalog entry is the
// only record of where a record is stored, so the catalog cannot be rebuilt on
// the device; a vector is computed from a record the device holds, so what
// damage takes from the index the vault can put back.
//
// A read of the files steps over the damage where it can, and tells the vault
// what it met before anything is done on the strength of it. The vault records
// in the device's store that a rebuild is owed, and rebuilds as it opens: in the
// same open when the damage is found as the vault opens, and in the next one
// when a compaction in a vault already open meets it. The rebuild gives a vector
// to every record held whole that has none, and takes out every vector whose
// record this device no longer holds, since the damage may have hidden the
// removal that forgot one.

// indexRebuildKey is the device setting that says a rebuild of the search index
// is owed.
//
// It is set before anything can take the damage off the disk, and cleared only
// by a read under the index's file lock that finds nothing left to rebuild. A
// rebuild cut short, by a crash, an interrupt or a host that stopped waiting for
// its server, leaves it set, and the next open goes on from where that one
// stopped: what was embedded has its vector, and is not embedded again.
const indexRebuildKey = "indexRebuildOwed"

// rebuildRounds is how many times a rebuild reads the index again to see
// whether it is done.
//
// One read is normally enough. Another process with the vault open can add
// records without a vector while the rebuild runs, which is all a hydrate at
// metadata depth does, and each round embeds what the last one found. A rebuild
// that has not caught up by the last leaves the rest to the next open, rather
// than holding this one for as long as the other process goes on writing.
const rebuildRounds = 3

// An IndexRepair is what a vault found wrong with its search index as it opened,
// and what it rebuilt before Open returned.
type IndexRepair struct {
	// Ran reports that the index was rebuilt as the vault opened. It is set when
	// this open found the index damaged, and when it found a rebuild owed by an
	// earlier run: one that found the damage and was cut short, or a vault open
	// in another process whose compaction met it.
	Ran bool
	// Found is the damage this open's own reads of the index met. It is empty
	// when the rebuild was owed by an earlier run, whose compaction may have
	// taken the damage off the disk since.
	Found index.Damage
	// Embedded counts the records given a vector: every memory and conversation
	// this device holds whole that had none.
	Embedded int
	// Dropped counts the vectors taken out because this device no longer holds
	// their record.
	Dropped int
	// Failed counts the records the model could not embed. They stay without a
	// vector, and a search finds them by their words alone.
	Failed int
	// Unfinished reports that records without a vector were still arriving from
	// another process when this open stopped rebuilding, and that the next open
	// goes on with it.
	Unfinished bool
}

// IndexRepair reports what this vault found wrong with its search index as it
// opened, and what it rebuilt. It is the zero value for a vault whose index had
// nothing wrong with it.
func (v *Vault) IndexRepair() IndexRepair { return v.repair.report }

// indexRepair is a vault's rebuild of its search index.
type indexRepair struct {
	// mu guards opened and report.Found, which the index's store reaches from
	// whichever goroutine is reading its files when it meets damage.
	mu sync.Mutex
	// opened records that the vault's own read of the index as it opened is
	// done. Damage met after that is owed to the next open, and is not counted
	// in this one's report.
	opened bool
	report IndexRepair
}

// indexDamaged is told by the index's store of the damage a read of its files
// met, under the index's file lock and before anything is done on the strength
// of the read.
//
// It records that a rebuild is owed, in the device's store, which every process
// with the vault open shares. That has to happen here and now. A compaction that
// read past the damage writes a new base without it and empties the delta, and
// after that nothing on the disk shows that the vectors it held are gone.
func (v *Vault) indexDamaged(found index.Damage) error {
	v.repair.mu.Lock()
	if !v.repair.opened {
		v.repair.report.Found = v.repair.report.Found.Add(found)
	}
	v.repair.mu.Unlock()
	return v.local.PutMeta(indexRebuildKey, record.Now().String())
}

// keepHeld takes out of what the index read every vector whose record this
// device no longer holds, on the disk as well as here, when a rebuild is owed,
// and leaves what it read alone when none is.
//
// It runs before the searchable index is built from what was read, so a vector
// it takes out is never searched, and never counted as this model's or another
// one's. Damage may have hidden the removal that forgot a record: the read
// stepped over it, and the vector written before it reads as live. Left in, that
// vector ranks a record this device can no longer return, and the recall that
// ranks it fails.
func (v *Vault) keepHeld(stored []index.Entry) ([]index.Entry, error) {
	v.repair.mu.Lock()
	v.repair.opened = true
	v.repair.mu.Unlock()

	switch _, err := v.local.GetMeta(indexRebuildKey); {
	case errors.Is(err, local.ErrNotFound):
		return stored, nil
	case err != nil:
		return nil, err
	}
	v.repair.report.Ran = true

	// What the device holds is read after the index, so a vector the read found
	// belongs to a record whose body or head was written before it was.
	held, err := v.heldRecords()
	if err != nil {
		return nil, err
	}
	kept := make([]index.Entry, 0, len(stored))
	var unheld []record.ID
	for _, vector := range stored {
		if held.mayReturn(vector.ID) {
			kept = append(kept, vector)
			continue
		}
		unheld = append(unheld, vector.ID)
	}
	if err := v.vectors.Remove(unheld...); err != nil {
		return nil, err
	}
	v.repair.report.Dropped += len(unheld)
	return kept, nil
}

// rebuildIndex finishes the rebuild keepHeld began, before Open returns: it
// takes the damage off the disk, gives a vector to every record held whole that
// has none, and clears the setting that said a rebuild was owed once a read
// under the index's lock finds nothing left to do.
//
// It holds Open for as long as the embedding takes, which is hundreds of
// milliseconds a record, and says how far it has got as it goes. A vault handed
// back sooner would search without the records the damage took, and nothing
// would say so.
func (v *Vault) rebuildIndex(ctx context.Context) error {
	if !v.repair.report.Ran {
		return nil
	}
	// The records this rebuild does not embed: one the model would not embed,
	// and one a read of the index found withdrawn. See passOverWithdrawn.
	passOver := make(map[record.ID]bool)
	for range rebuildRounds {
		plan, damaged, err := v.planRebuild(passOver)
		if err != nil {
			return err
		}
		for _, id := range plan.unheld {
			if err := v.removeVector(id); err != nil {
				return err
			}
			v.repair.report.Dropped++
		}
		// What the damage hid cannot be read from the files whatever is done
		// next, so they are folded now, which takes the damage off the disk. The
		// setting stays until the end, so a rebuild cut short after this still
		// leaves the next open owing what is left.
		if damaged {
			if err := v.vectors.Fold(); err != nil {
				return fmt.Errorf("compact index: %w", err)
			}
		}
		if err := v.embedMissing(ctx, plan.missing, passOver); err != nil {
			return err
		}
		if done, err := v.finishRebuild(passOver); err != nil || done {
			return err
		}
	}
	v.repair.report.Unfinished = true
	return nil
}

// passOverWithdrawn adds to passOver every record whose latest entry in the
// reading is a removal.
//
// A removal is a forget, finished or seen part way. A forget withdraws the
// vector before it lets go of the last of the record, so one that stopped part
// way leaves a record that looks held whole and without a vector. It is passed
// over for the rest of the rebuild and not only while the index shows the
// removal: the rebuild compacts the index once it has read past the damage, a
// compaction carries no removal into the new base, and a later round reading the
// index afresh gave the record its vector back.
func passOverWithdrawn(passOver map[record.ID]bool, reading index.Reading) {
	for id := range reading.Removed {
		passOver[id] = true
	}
}

// planRebuild reads the index and then what the device holds, in that order, and
// says what is left to do, and whether the read met damage.
func (v *Vault) planRebuild(passOver map[record.ID]bool) (rebuildPlan, bool, error) {
	var reading index.Reading
	if err := v.vectors.Read(func(r index.Reading) error {
		reading = r
		return nil
	}); err != nil {
		return rebuildPlan{}, false, err
	}
	passOverWithdrawn(passOver, reading)
	held, err := v.heldRecords()
	if err != nil {
		return rebuildPlan{}, false, err
	}
	return held.plan(reading, passOver), reading.Damage.Found(), nil
}

// finishRebuild clears the setting that says a rebuild is owed if a read of the
// index finds nothing left to do, and reports whether it did.
//
// The read and the clearing happen under one hold of the index's file lock. A
// compaction in another process that met fresh damage in between would
// otherwise record a rebuild owed just before this cleared it.
//
// What the device holds is read first, outside the lock, so a record another
// process writes meanwhile can look unfinished here. That costs another round
// and never a vector: nothing is taken out or embedded on the strength of this
// read.
func (v *Vault) finishRebuild(passOver map[record.ID]bool) (bool, error) {
	held, err := v.heldRecords()
	if err != nil {
		return false, err
	}
	var done bool
	err = v.vectors.Read(func(reading index.Reading) error {
		passOverWithdrawn(passOver, reading)
		if reading.Damage.Found() {
			return nil
		}
		if plan := held.plan(reading, passOver); len(plan.unheld) > 0 || len(plan.missing) > 0 {
			return nil
		}
		if err := v.local.DeleteMeta(indexRebuildKey); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}

// embedMissing gives each record a vector, saying as it goes how far it has got.
func (v *Vault) embedMissing(ctx context.Context, missing []missingRecord, passOver map[record.ID]bool) error {
	total := int64(len(missing))
	v.progress(Progress{Phase: PhaseIndexRebuild, Total: total, Unit: UnitRecords})
	for i, m := range missing {
		outcome, err := v.embedAgain(ctx, m)
		if err != nil {
			return err
		}
		switch outcome {
		case vectorWritten:
			v.repair.report.Embedded++
		case recordUnembeddable:
			passOver[m.id] = true
			v.repair.report.Failed++
		}
		v.progress(Progress{Phase: PhaseIndexRebuild, Done: int64(i + 1), Total: total, Unit: UnitRecords})
	}
	return nil
}

// An embedOutcome is what became of one record the rebuild set out to embed.
type embedOutcome int

const (
	vectorWritten embedOutcome = iota
	// recordGone is a record this device stopped holding whole before its
	// vector was written: a forget ran meanwhile, in this process or another.
	recordGone
	// recordUnembeddable is a record whose stored text could not be read back,
	// or that the model would not embed.
	recordUnembeddable
)

var (
	// errNotWhole reports a record this device does not hold whole.
	errNotWhole = errors.New("the record is not held whole on this device")
	// errUnreadable reports a stored record whose text could not be read back.
	errUnreadable = errors.New("the stored record could not be read")
)

// embedAgain gives one record held whole its vector again.
//
// The record is read again once the model is done with it. Embedding takes long
// enough for a forget to have run in between, and a vector written after that
// forget had taken the record's own would bring the record back to a search.
func (v *Vault) embedAgain(ctx context.Context, m missingRecord) (embedOutcome, error) {
	text, err := v.wholeText(m)
	if err != nil {
		return passedOver(err)
	}
	vector, err := v.embedder.EmbedOne(ctx, text)
	if err != nil {
		// An interrupt stops the rebuild, and the next open goes on with it. Any
		// other failure is this record's alone, and the rest are still worth
		// embedding.
		if ctx.Err() != nil {
			return recordGone, ctx.Err()
		}
		return recordUnembeddable, nil
	}
	if _, err := v.wholeText(m); err != nil {
		return passedOver(err)
	}
	return vectorWritten, v.putVector(m.id, vector)
}

// passedOver says what a failure to read a record whole means for the rebuild:
// a record no longer held or one that cannot be read back is passed over, and
// anything else stops the rebuild.
func passedOver(err error) (embedOutcome, error) {
	switch {
	case errors.Is(err, errNotWhole):
		return recordGone, nil
	case errors.Is(err, errUnreadable):
		return recordUnembeddable, nil
	}
	return recordGone, err
}

// wholeText reads what a record is embedded from, provided this device holds it
// whole: its ranking rows, and the memory's body or the conversation's head.
func (v *Vault) wholeText(m missingRecord) (string, error) {
	meta, err := v.local.RankingMetaFor(m.id)
	if errors.Is(err, local.ErrNotFound) {
		return "", errNotWhole
	}
	if err != nil {
		return "", err
	}
	if meta.Kind != m.kind {
		return "", errNotWhole
	}

	if m.kind == record.KindSession {
		head, err := v.local.GetSessionHead(m.id)
		if errors.Is(err, local.ErrNotFound) {
			return "", errNotWhole
		}
		if err != nil {
			return "", err
		}
		session, err := record.UnmarshalSession(head)
		if err != nil {
			return "", fmt.Errorf("conversation %s: %w: %v", m.id, errUnreadable, err)
		}
		return session.IndexText(), nil
	}
	kind, body, err := v.local.GetRecord(m.id)
	if errors.Is(err, local.ErrNotFound) {
		return "", errNotWhole
	}
	if err != nil {
		return "", err
	}
	if kind != record.KindMemory {
		return "", errNotWhole
	}
	memory, err := record.Unmarshal(body)
	if err != nil {
		return "", fmt.Errorf("memory %s: %w: %v", m.id, errUnreadable, err)
	}
	return memory.IndexText(), nil
}

// heldRecords is which records this device holds, and which of them the catalog
// names, read together for one decision about the index.
type heldRecords struct {
	local.Held
	catalogued map[record.ID]bool
}

func (v *Vault) heldRecords() (heldRecords, error) {
	held, err := v.local.HeldRecords()
	if err != nil {
		return heldRecords{}, err
	}
	catalogued := make(map[record.ID]bool)
	for _, entry := range v.manifest.Entries() {
		catalogued[entry.ID] = true
	}
	return heldRecords{Held: held, catalogued: catalogued}, nil
}

// mayReturn reports whether this device could still return the record a stored
// vector belongs to, which is what keeps the vector.
//
// It errs towards keeping. A conversation is returned from its head, and a
// memory from its body, or from the network through the catalog when only its
// ranking rows are here. Every path that writes a vector writes the body or the
// head first, so a vector with neither is not one another process is part way
// through writing. A forget takes what returns a record away either before its
// vector, as a memory's does, or after it, as a conversation's does, so a vector
// this drops while a forget is part way through is one that forget was taking
// out anyway.
func (h heldRecords) mayReturn(id record.ID) bool {
	return h.Heads[id] || h.MemoryBodies[id] || (h.MemoryRows[id] && h.catalogued[id])
}

// A rebuildPlan is what one read of the index leaves for a rebuild to do.
type rebuildPlan struct {
	// unheld are the stored vectors whose record this device no longer holds.
	unheld []record.ID
	// missing are the records held whole that have no vector at all.
	missing []missingRecord
}

// A missingRecord is one record to embed, and which kind of record it is.
type missingRecord struct {
	id   record.ID
	kind record.Kind
}

// plan says what a reading of the index leaves to do.
//
// A record is missing when this device holds it whole and the reading gives it
// no vector: a memory whose body and ranking rows are both here, or a
// conversation whose head and ranking rows are. A record is listed, counted and
// ranked only once its ranking rows are written, so one without them is a write
// that did not finish, or a record a forget has begun on, and neither is given a
// vector. Nor is a record the rebuild passes over: one the model would not embed,
// or one a read of the index found withdrawn, which is a forget seen part way
// through. A transcript chunk has no ranking rows, and is never embedded: the
// summary on its conversation's head is what is searched.
//
// A vector from another model is still a vector. Its record is not missing, and
// it goes on being counted as unsearchable, as it was before the damage.
func (h heldRecords) plan(reading index.Reading, passOver map[record.ID]bool) rebuildPlan {
	var plan rebuildPlan
	vectored := make(map[record.ID]bool, len(reading.Entries))
	for _, vector := range reading.Entries {
		vectored[vector.ID] = true
		if !h.mayReturn(vector.ID) {
			plan.unheld = append(plan.unheld, vector.ID)
		}
	}
	for _, class := range []struct {
		rows, text map[record.ID]bool
		kind       record.Kind
	}{
		{h.MemoryRows, h.MemoryBodies, record.KindMemory},
		{h.SessionRows, h.Heads, record.KindSession},
	} {
		var ids []record.ID
		for id := range class.rows {
			if class.text[id] && !vectored[id] && !passOver[id] {
				ids = append(ids, id)
			}
		}
		slices.SortFunc(ids, func(a, b record.ID) int { return bytes.Compare(a[:], b[:]) })
		for _, id := range ids {
			plan.missing = append(plan.missing, missingRecord{id: id, kind: class.kind})
		}
	}
	return plan
}
