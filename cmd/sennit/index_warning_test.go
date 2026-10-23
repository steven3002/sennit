package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/vault"
)

// A search index found damaged is rebuilt as the vault opens, and every command
// says so above everything else it prints, in the shape of the other warnings a
// vault gives as it opens: what was found, what the rebuild did, and what to do.
func TestARebuiltSearchIndexIsWarnedOf(t *testing.T) {
	s := ended(t, 80, 2100*time.Millisecond)
	s.out.warn(rebuiltIndex(vault.IndexRepair{Ran: true, Found: index.Damage{Skipped: 2}, Embedded: 5, Dropped: 1}))
	s.out.done(ui.MarkSuccess, "Checked this vault")
	s.assert(t, "a rebuilt index", fixture(t, "warning-index-rebuilt.txt"))

	for _, c := range []struct {
		name   string
		repair vault.IndexRepair
		text   string
		says   []string
	}{
		{"found by an earlier run", vault.IndexRepair{Ran: true, Embedded: 1},
			"the search index on this device was damaged, and has been rebuilt",
			[]string{"An earlier run found it damaged.", "given one, 1 in all."}},
		{"a damaged length", vault.IndexRepair{Ran: true, Found: index.Damage{Lost: 4096}, Embedded: 2},
			"the search index on this device was damaged, and has been rebuilt",
			[]string{"Nothing in it after a damaged record length could be read."}},
		{"nothing lost with it", vault.IndexRepair{Ran: true, Found: index.Damage{Skipped: 1}},
			"the search index on this device was damaged, and has been rebuilt",
			[]string{"1 record in it could not be read.", "still had its vector."}},
		{"a record the model would not embed", vault.IndexRepair{Ran: true, Found: index.Damage{Skipped: 1}, Embedded: 3, Failed: 1},
			"the search index on this device was damaged, and has been rebuilt",
			[]string{"1 could not be embedded, and is found by its words alone."}},
		{"left unfinished", vault.IndexRepair{Ran: true, Found: index.Damage{Skipped: 1}, Embedded: 4, Unfinished: true},
			"the search index on this device was damaged, and has been partly rebuilt",
			[]string{"still arriving from another process", "goes on with them"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			message := rebuiltIndex(c.repair)
			if message.Kind != ui.KindWarning || message.Text != c.text {
				t.Errorf("warned of as %q, want the warning %q", message.Text, c.text)
			}
			explained := strings.Join(message.Explanation, " ")
			for _, says := range c.says {
				if !strings.Contains(explained, says) {
					t.Errorf("the explanation does not say %q: %q", says, explained)
				}
			}
		})
	}
}

// The rebuild is the one part of opening a vault that can take minutes, so the
// live line names it and counts what is done of what there is, whichever
// command opened the vault, and an interrupt there says what it leaves. Every
// other phase keeps the name the command gives it.
func TestTheRebuildOfTheSearchIndexIsShownOnTheLiveLine(t *testing.T) {
	s := terminal(t, 80)
	s.out.leaves("what the command had said an interrupt leaves")
	namer := s.out.rebuilding(rememberPhases(s.out))

	text, detail, show := namer(vault.Progress{Phase: vault.PhaseIndexRebuild, Done: 12, Total: 1234, Unit: vault.UnitRecords})
	if !show || text != "Rebuilding the search index" || detail != "12 of 1,234 records" {
		t.Errorf("the rebuild is shown as %q with %q (shown %v)", text, detail, show)
	}
	if !slices.Equal(s.out.cancelled, []string{rebuildLeaves}) {
		t.Errorf("an interrupt during the rebuild would leave %q", s.out.cancelled)
	}
	if _, detail, _ := namer(vault.Progress{Phase: vault.PhaseIndexRebuild, Unit: vault.UnitRecords}); detail != "" {
		t.Errorf("a rebuild with nothing to count shows %q", detail)
	}
	if text, _, show := namer(vault.Progress{Phase: vault.PhaseEmbed}); !show || text != "Embedding and encrypting" {
		t.Errorf("the command's own phase is shown as %q (shown %v)", text, show)
	}
}
