package main

import (
	"slices"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/vault"
)

// A phaseNamer turns a phase the vault reports into the sentence shown for it,
// with the progress detail when something real counts it. It answers false for
// a phase the command does not show.
//
// The wording is the command's rather than the vault's, because the same phase
// is a different sentence in each: a flush uploads records, a repack uploads
// the same records again somewhere else.
type phaseNamer func(vault.Progress) (text, detail string, show bool)

// openPhases names what every vault command does before it starts its own work.
func openPhases(p vault.Progress) (string, string, bool) {
	switch p.Phase {
	case vault.PhaseUnlock:
		return "Unlocking vault", "", true
	case vault.PhaseModelFetch:
		return "Downloading embedding model", "", true
	case vault.PhaseModelLoad:
		return "Loading embedding model", "", true
	case vault.PhaseIndexLoad:
		return "Loading search index", "", true
	case vault.PhaseConnect:
		return "Connecting to Sia", "", true
	case vault.PhaseAwaitReady:
		return "Waiting for the indexer to fund host accounts", "", true
	}
	return "", "", false
}

// mayStayBilled is what an interrupt can leave once a write has started pinning.
//
// A slab is billed from the moment it is pinned, and the device writes it into
// its ledger only after the records on it are pinned as well, so an interrupt
// between the two leaves storage paid for that nothing here records. Measured
// against a live account, that is one whole slab holding nothing. `sennit
// status` finds it anyway, because it asks the indexer what the account is
// billed for rather than trusting the ledger.
const mayStayBilled = "A slab pinned before the interrupt may stay billed; `sennit status` shows it."

// writePhases names the two steps every write shares. The upload is the only
// phase with an exact count: shards written of shards to write.
//
// Pinning also changes what an interrupt leaves, so its first report adds
// mayStayBilled to the end of whatever the command has already said. An upload
// pins nothing, so an interrupt before that point is told exactly what it was
// told before.
func (s *session) writePhases(uploading string) phaseNamer {
	return func(p vault.Progress) (string, string, bool) {
		switch p.Phase {
		case vault.PhaseUpload:
			return uploading, s.percent(p.Done, p.Total), true
		case vault.PhasePin:
			if !slices.Contains(s.cancelled, mayStayBilled) {
				s.leaves(append(slices.Clip(s.cancelled), mayStayBilled)...)
			}
			return "Pinning on Sia", "", true
		}
		return openPhases(p)
	}
}

func flushPhases(s *session, records int) phaseNamer {
	return s.writePhases("Uploading " + plural(records, "record") + " to Sia")
}

func recallPhases(s *session) phaseNamer {
	return func(p vault.Progress) (string, string, bool) {
		if p.Phase == vault.PhaseSearch {
			return "Searching " + plural(int(p.Total), "memory"), "", true
		}
		return openPhases(p)
	}
}

func reclaimPhases(s *session) phaseNamer {
	write := s.writePhases("Uploading to Sia")
	return func(p vault.Progress) (string, string, bool) {
		switch p.Phase {
		case vault.PhaseRepackRead:
			return "Reading " + plural(int(p.Total), "record") + " to repack", "", true
		case vault.PhaseRepackRetire:
			return "Releasing the old slabs", "", true
		case vault.PhaseSweep:
			return "Sweeping storage nothing points to", "", true
		case vault.PhaseUnledgered:
			return "Checking for storage this device has no record of", "", true
		case vault.PhaseUnreadable:
			return "Dropping objects the indexer cannot open", "", true
		case vault.PhaseOrphans:
			return "Releasing slabs that hold nothing", "", true
		}
		return write(p)
	}
}

// restorePhases names a rebuild's two waits. The count of records restored is
// real; how many there will be is not known until the walk ends, so none is
// claimed.
func restorePhases(s *session, restoring, restored string, quiet bool) phaseNamer {
	return func(p vault.Progress) (string, string, bool) {
		switch p.Phase {
		case vault.PhaseList:
			return "Listing this account's objects", "", true
		case vault.PhaseRestore:
			if quiet {
				return restoring, "", true
			}
			return restoring, s.counted(p.Done, restored), true
		case vault.PhaseRebuild:
			return "Rebuilding conversations", "", true
		}
		return openPhases(p)
	}
}

// accountPhase is what a command that only reads the account is waiting on.
const accountPhase = "Checking the account"

// plural writes a count and its noun, with thousands separators, so a line
// reads as a sentence rather than as a record with a count bolted on.
func plural(n int, noun string) string {
	word := noun
	if n != 1 {
		switch noun {
		case "memory":
			word = "memories"
		case "conversation entry":
			word = "conversation entries"
		default:
			word = noun + "s"
		}
	}
	return ui.Commas(n) + " " + word
}
