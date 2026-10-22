package main

import (
	"errors"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
)

// A listing the vault could not date by its records as it opened is warned of
// above everything else a command prints, in one sentence: that the listing
// was not dated, that the next open tries again, and why. It says nothing more,
// because nothing was lost and there is nothing to run.
func TestAListingTheVaultCouldNotDateIsWarnedOfInOneSentence(t *testing.T) {
	s := ended(t, 80, 400*time.Millisecond)
	s.out.warn(undatedListing(errors.New(`store meta "listingDatedByRecords": database or disk is full (13)`)))
	s.out.done(ui.MarkSuccess, "Checked this vault")
	s.assert(t, "a listing not dated", fixture(t, "warning-listing-not-dated.txt"))

	message := undatedListing(errors.New("the cause"))
	if message.Kind != ui.KindWarning || len(message.Explanation) != 0 || len(message.Hints) != 0 {
		t.Errorf("the warning is %+v, want a warning of one sentence and nothing else", message)
	}
}
