package main

import (
	"fmt"
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/reclaim"
	"github.com/steven3002/sennit/vault"
)

// flushedRecords is what a flush that wrote records says it did.
func flushedRecords(flushed *store.Flush) string {
	return "Flushed " + plural(len(flushed.Written), "record") + " to Sia"
}

// flushOutcome is the line a flush ends with: what it wrote of the records that
// were queued, and how many forgotten records' copies it deleted from Sia.
//
// Both are said on the one line, because one run does both, and a reader who
// is told only about the records written would take a forgotten record's copy
// to be gone or still there by guesswork.
func flushOutcome(queued int, flushed *store.Flush, deleted int) (ui.Mark, string) {
	gone := plural(deleted, "forgotten record")
	switch {
	case flushed != nil && deleted > 0:
		return ui.MarkSuccess, flushedRecords(flushed) + " and deleted " + gone + " from it"
	case flushed != nil:
		return ui.MarkSuccess, flushedRecords(flushed)
	case queued > 0 && deleted > 0:
		return ui.MarkWarning, "The flush wrote nothing, and deleted " + gone + " from Sia"
	case queued > 0:
		return ui.MarkWarning, "The flush wrote nothing"
	case deleted > 0:
		return ui.MarkSuccess, "Deleted " + gone + " from Sia"
	}
	return ui.MarkSuccess, "Nothing queued: everything on this device is on Sia"
}

// owedStays says what a flush that cannot reach Sia leaves owed to it: the
// records still queued, and the copies of forgotten records still there.
func owedStays(queued, forgotten int) string {
	var said []string
	if queued > 0 {
		said = append(said, queuedStay(queued))
	}
	if forgotten > 0 {
		said = append(said, forgottenStays(forgotten))
	}
	return strings.Join(said, " ")
}

// forgottenStays says that the copies of forgotten records are still on Sia,
// and what that still lets another device do with them.
func forgottenStays(forgotten int) string {
	if forgotten == 1 {
		return "The forgotten record stays on Sia, where a restore on another device can still bring it back."
	}
	return "The " + plural(forgotten, "forgotten record") + " stay on Sia, where a restore on another " +
		"device can still bring them back."
}

// forgottenLeaves is what an interrupt leaves once a flush has begun deleting
// the copies of forgotten records. Whatever the flush wrote before that is on
// Sia, and is said to be, so that nobody writes it again.
func forgottenLeaves(flushed *store.Flush) []string {
	stay := "Forgotten records not deleted yet stay on Sia, and a later flush deletes them."
	if flushed == nil {
		return []string{stay}
	}
	return []string{flushedRecords(flushed) + ".", stay}
}

// forgottenStay is the failure of a flush that could not delete the copies of
// forgotten records: they stay on Sia, a later flush tries again, and until then
// a restore on another device can still bring them back.
func forgottenStay(left int, err error) error {
	if left < 1 {
		return err
	}
	them := pick(left, "it", "them")
	return refuse(fmt.Sprintf("%s could not be deleted from Sia", plural(left, "forgotten record"))).
		because(err.Error()+".", fmt.Sprintf("%s there until a later flush deletes %s, and until then a "+
			"restore on another device can still bring %s back.", pick(left, "It stays", "They stay"), them, them)).
		from(err)
}

// forgottenRow is the line a restore reports the records it left out with,
// because this device has forgotten them.
func forgottenRow(n int) [2]string {
	return [2]string{"Left out", plural(n, "record") + " this device has forgotten"}
}

// goneNote is what a repack says of the records it left where they were,
// because their copies on Sia had been deleted, and nothing when it left none.
//
// A device that forgets a record deletes its copy, and forgets are not shared,
// so the device that wrote the record still holds and catalogues it. Repack
// leaves such a record where it is rather than write it back to Sia, and keeps
// its slab, so the reader is told why that storage did not come back.
func goneNote(packed reclaim.Repack) (ui.Message, bool) {
	n := len(packed.Gone)
	if n == 0 {
		return ui.Message{}, false
	}
	return ui.Message{
		Kind: ui.KindWarning,
		Text: fmt.Sprintf("%s %s left where %s, because %s on Sia %s been deleted",
			plural(n, "record"), verb(n, "was", "were"), pick(n, "it is", "they are"),
			pick(n, "its copy", "their copies"), verb(n, "has", "have")),
		Explanation: []string{"A device that forgets a record deletes its copy, and forgets are not shared " +
			"between devices. " + pick(n, "It stays", "Each stays") + " on this device, and keeps its storage, " +
			"until it is forgotten here too."},
	}, true
}

// keptRow is the line a release of orphans reports the slabs it kept with: they
// hold nothing, and a record on this device still names them, so releasing one
// would take it out of the ledger every later repack reads from.
func keptRow(slabs int) [2]string {
	return [2]string{"Kept", fmt.Sprintf("%s that %s nothing, because a record on this device still names %s",
		plural(slabs, "slab"), verb(slabs, "holds", "hold"), pick(slabs, "it", "them"))}
}

// recoverRows is what a recovery left out, when it left anything out: records
// this device has forgotten, and frames this phrase does not open.
func recoverRows(report vault.RecoveryReport) [][2]string {
	var rows [][2]string
	if report.Forgotten > 0 {
		rows = append(rows, forgottenRow(report.Forgotten))
	}
	if report.Foreign > 0 {
		rows = append(rows, [2]string{"Skipped", plural(report.Foreign, "frame") + " this phrase does not open"})
	}
	return rows
}
