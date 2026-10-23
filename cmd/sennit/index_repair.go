package main

import (
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/vault"
)

// rebuildLeaves is what an interrupt leaves while the vault rebuilds its search
// index as it opens.
const rebuildLeaves = "The records given a vector before the interrupt keep it, and the next run " +
	"to open the vault rebuilds the rest."

// rebuilding names the rebuild of the search index a vault runs as it opens,
// once it has found the index damaged, for whichever command opened it, and
// leaves every other phase to the command's own names.
//
// It is the one phase of an open that can take minutes, so it counts what is
// done of what there is to do, and it says what an interrupt there leaves,
// whatever the command had said before it.
func (s *session) rebuilding(phases phaseNamer) phaseNamer {
	return func(p vault.Progress) (string, string, bool) {
		if p.Phase != vault.PhaseIndexRebuild {
			return phases(p)
		}
		s.leaves(rebuildLeaves)
		var detail string
		if p.Total > 0 {
			detail = ui.Commas(int(p.Done)) + " of " + plural(int(p.Total), "record")
		}
		return "Rebuilding the search index", detail, true
	}
}

// rebuiltIndex is the warning a vault prints when it rebuilt its search index as
// it opened: what it found, what the rebuild did about it, and what is left.
//
// It names no record. The records whose vectors were lost are the ones the
// damaged part held, and nothing could read which they were, so it says what
// the rebuild did for every record instead.
func rebuiltIndex(r vault.IndexRepair) ui.Message {
	text := "the search index on this device was damaged, and has been rebuilt"
	if r.Unfinished {
		text = "the search index on this device was damaged, and has been partly rebuilt"
	}
	said := []string{indexDamageFound(r)}
	switch {
	case r.Embedded > 0:
		said = append(said, "Every memory and conversation this device holds that had no vector has "+
			"been given one, "+ui.Commas(r.Embedded)+" in all.")
	case r.Failed == 0 && !r.Unfinished:
		said = append(said, "Every memory and conversation this device holds still had its vector.")
	}
	if r.Failed > 0 {
		said = append(said, ui.Commas(r.Failed)+pick(r.Failed, " could not be embedded, and is found by its",
			" could not be embedded, and are found by their")+" words alone.")
	}
	if r.Dropped > 0 {
		said = append(said, plural(r.Dropped, "vector")+pick(r.Dropped, " of a record this device no longer holds was",
			" of records this device no longer holds were")+" taken out, so a forgotten record cannot come "+
			"back in a search.")
	}
	if r.Unfinished {
		said = append(said, "Records without a vector were still arriving from another process, and the "+
			"next run to open the vault goes on with them.")
	}
	return ui.Message{
		Kind:        ui.KindWarning,
		Text:        text,
		Explanation: []string{strings.Join(said, " ")},
		Hints:       []string{"a conversation saved recently may be found by an earlier summary until it is next saved"},
	}
}

// indexDamageFound says what this run found wrong with the search index, or
// that an earlier one found it.
func indexDamageFound(r vault.IndexRepair) string {
	skipped, lost := r.Found.Skipped, r.Found.Lost > 0
	switch {
	case skipped > 0 && lost:
		return plural(skipped, "record") + " in it could not be read, nor anything after a damaged record length."
	case skipped > 0:
		return plural(skipped, "record") + " in it could not be read."
	case lost:
		return "Nothing in it after a damaged record length could be read."
	}
	return "An earlier run found it damaged."
}
