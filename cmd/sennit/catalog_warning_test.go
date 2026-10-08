package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
)

// A change a crash cut off is dropped as the catalog is read, and every command
// says so above everything else it prints, in the shape of the other warning a
// vault gives as it opens: what happened, what it undid, and what to run again.
func TestADroppedCatalogChangeIsWarnedOf(t *testing.T) {
	s := ended(t, 80, 400*time.Millisecond)
	s.out.warn(cutCatalog(1))
	s.out.done(ui.MarkSuccess, "Checked this vault")
	s.assert(t, "one change dropped", fixture(t, "warning-catalog-cut.txt"))

	several := cutCatalog(2)
	if want := "2 changes to the catalog were cut off by crashes and have been dropped"; several.Text != want {
		t.Errorf("two dropped changes are warned of as %q, want %q", several.Text, want)
	}
	if explained := strings.Join(several.Explanation, " "); !strings.HasPrefix(explained, "Each was one record's latest change") {
		t.Errorf("the explanation of two dropped changes does not speak of each: %q", explained)
	}
}
