package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/vault"
)

// flushWriting is a flush that wrote n records.
func flushWriting(n int) *store.Flush { return &store.Flush{Written: make([]store.Written, n)} }

// lineOf is the text of a final line, without its mark.
func lineOf(_ ui.Mark, text string) string { return text }

// forgottenScreens is what flush, hydrate and recover put on screen once a
// record has been forgotten here, by the file each approved screen is kept in.
func forgottenScreens(t *testing.T) map[string]string {
	t.Helper()
	screens := map[string]string{}

	s := ended(t, 80, 1400*time.Millisecond)
	s.out.done(flushOutcome(0, nil, 2))
	screens["flush-forgotten.txt"] = s.text()

	s = ended(t, 80, 23100*time.Millisecond)
	s.out.done(flushOutcome(3, flushWriting(3), 1))
	screens["flush-and-forgotten.txt"] = s.text()

	screens["flush-offline-forgotten.txt"] = shown(t, 80, needsIndexer("flush", &vault.Vault{}, owedStays(37, 2)))

	// The write has landed before the deletion fails, and the screen says so
	// before it says what failed.
	s = ended(t, 80, 24600*time.Millisecond)
	s.out.done(flushOutcome(3, flushWriting(3), 0))
	report(t.Context(), s.out, forgottenStay(2,
		errors.New("delete the copies of 2 forgotten record(s) from the network: the indexer did not answer")))
	screens["flush-forgotten-not-deleted.txt"] = s.text()

	s = ended(t, 80, 25100*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Hydrated 1,006 records to depth metadata")
	s.out.report(hydrateRows(vault.HydrateReport{
		Records: 1006, Objects: 14, Bytes: 2013265, Bodies: 1006, Slabs: 14, Forgotten: 2,
	}, "https://sia.storage"))
	screens["hydrate-forgotten.txt"] = s.text()

	s = ended(t, 80, 56500*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Recovered 1,006 records from 14 objects")
	s.out.report(recoverRows(vault.RecoveryReport{Recovered: 1006, Objects: 14, Foreign: 12, Forgotten: 2}))
	screens["recover-forgotten.txt"] = s.text()
	return screens
}

// What flush, hydrate and recover say once a record has been forgotten here.
//
// A flush used to say nothing queued and everything on Sia while a forgotten
// record's copy was still there, and a restore left nothing out, so neither
// had anything to say about one.
func TestWhatIsSaidOfAForgottenRecord(t *testing.T) {
	for name, got := range forgottenScreens(t) {
		if want := fixture(t, name); got != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
}

// Every count said of forgotten records agrees with its nouns, verbs and
// pronouns, and one is the count that catches a fixed plural.
func TestWhatIsSaidOfForgottenRecordsAgreesWithItsCount(t *testing.T) {
	stays, ok := forgottenStay(1, errors.New("the indexer did not answer")).(*problem)
	if !ok || len(stays.message.Explanation) != 2 {
		t.Fatalf("a deletion that failed is reported as %#v", stays)
	}
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"one forgotten record deleted", lineOf(flushOutcome(0, nil, 1)), "Deleted 1 forgotten record from Sia"},
		{"one record flushed and two deleted", lineOf(flushOutcome(1, flushWriting(1), 2)),
			"Flushed 1 record to Sia and deleted 2 forgotten records from it"},
		{"nothing written and one deleted", lineOf(flushOutcome(2, nil, 1)),
			"The flush wrote nothing, and deleted 1 forgotten record from Sia"},
		{"one record queued and one forgotten", owedStays(1, 1),
			"The record stays queued on this device. The forgotten record stays on Sia, where a restore " +
				"on another device can still bring it back."},
		{"three forgotten", owedStays(0, 3),
			"The 3 forgotten records stay on Sia, where a restore on another device can still bring them back."},
		{"one forgotten record not deleted", stays.message.Text, "1 forgotten record could not be deleted from Sia"},
		{"what stays of it", stays.message.Explanation[1],
			"It stays there until a later flush deletes it, and until then a restore on another device can " +
				"still bring it back."},
		{"one record left out", forgottenRow(1)[1], "1 record this device has forgotten"},
		{"stopped before anything was written", strings.Join(forgottenLeaves(nil), " "),
			"Forgotten records not deleted yet stay on Sia, and a later flush deletes them."},
		{"stopped after one record was written", strings.Join(forgottenLeaves(flushWriting(1)), " "),
			"Flushed 1 record to Sia. Forgotten records not deleted yet stay on Sia, and a later flush deletes them."},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %q, want %q", c.got, c.want)
			}
		})
	}
}

// A flush with nothing forgotten says what it always said, and so does one that
// cannot reach Sia with only records queued.
func TestAFlushWithNothingForgottenSaysWhatItAlwaysSaid(t *testing.T) {
	for _, c := range []struct {
		name    string
		queued  int
		flushed *store.Flush
		mark    ui.Mark
		text    string
	}{
		{"flushed", 3, flushWriting(3), ui.MarkSuccess, "Flushed 3 records to Sia"},
		{"wrote nothing", 3, nil, ui.MarkWarning, "The flush wrote nothing"},
	} {
		if mark, text := flushOutcome(c.queued, c.flushed, 0); mark != c.mark || text != c.text {
			t.Errorf("%s: got %v %q, want %v %q", c.name, mark, text, c.mark, c.text)
		}
	}
	if got, want := owedStays(37, 0), "The 37 records stay queued on this device."; got != want {
		t.Errorf("an offline flush with only records queued says %q, want %q", got, want)
	}
}
