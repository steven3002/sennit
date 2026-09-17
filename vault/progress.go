package vault

import (
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/reclaim"
)

// A Phase names what an operation is doing at one moment.
//
// The names are the operation's own, not a caller's wording: an interface
// decides what to call a phase in front of a person, and only the vault knows
// which one it is actually in. Every online operation here spends seconds
// somewhere, so a caller that cannot tell them apart can only say "working".
type Phase string

const (
	// PhaseUnlock derives the keys and opens the device's store and catalog.
	PhaseUnlock Phase = "unlock"
	// PhaseModelFetch downloads the embedding model, which happens once per
	// machine and is the longest wait a first run meets.
	PhaseModelFetch Phase = "model-fetch"
	// PhaseModelLoad reads the model into memory.
	PhaseModelLoad Phase = "model-load"
	// PhaseIndexLoad reads the search vectors from the device.
	PhaseIndexLoad Phase = "index-load"
	// PhaseConnect opens the connection to Sia, which warms a connection to
	// every host the indexer lists and is the bulk of an online command's fixed
	// cost.
	PhaseConnect Phase = "connect"
	// PhaseAwaitReady waits for the indexer to finish funding host accounts,
	// which only a freshly approved installation meets.
	PhaseAwaitReady Phase = "await-ready"
	// PhaseEmbed turns a record into a vector and seals it.
	PhaseEmbed Phase = "embed"
	// PhaseSearch scores the query against the index. Total is how many vectors
	// it holds.
	PhaseSearch Phase = "search"
	// PhaseUpload writes shards to hosts. Done and Total count shard uploads.
	PhaseUpload Phase = "upload"
	// PhasePin registers a write with the indexer, which is what makes it
	// retrievable.
	PhasePin Phase = "pin"
	// PhaseList enumerates the account's objects.
	PhaseList Phase = "list"
	// PhaseRestore reads records back. Done counts them.
	PhaseRestore Phase = "restore"
	// PhaseRebuild reconstructs conversations from their transcripts.
	PhaseRebuild Phase = "rebuild"
	// PhaseSweep releases storage nothing points at any more.
	PhaseSweep Phase = "sweep"
	// PhaseUnledgered asks the indexer what it bills that this device has no
	// record of.
	PhaseUnledgered Phase = "unledgered"
	// PhaseOrphans releases slabs that hold nothing.
	PhaseOrphans Phase = "orphans"
	// PhaseUnreadable deletes objects the indexer will not open.
	PhaseUnreadable Phase = "unreadable"
	// PhaseRepackRead and PhaseRepackRetire are a repack's first and last steps.
	// Total on the read is how many records it moves.
	PhaseRepackRead   Phase = "repack-read"
	PhaseRepackRetire Phase = "repack-retire"
)

// Progress reports what an operation is doing, for a caller that shows it.
type Progress struct {
	Phase Phase
	// Done is how much of Total is finished, and Total how much there is. Total
	// is zero when the phase has no countable size, which is most of them: a
	// count that was invented is worse than none.
	Done, Total int64
	// Unit is what Done and Total count.
	Unit string
}

// Units a phase counts in.
const (
	UnitShards  = "shards"
	UnitRecords = "records"
)

func (v *Vault) progress(p Progress) {
	if v.opts.OnProgress != nil {
		v.opts.OnProgress(p)
	}
}

// observe wires the layers below the vault to its own progress, so that a
// caller sees one sequence of phases rather than having to subscribe to each of
// them.
func (v *Vault) observe() {
	if v.opts.OnProgress == nil {
		return
	}
	if v.store != nil {
		v.store.Observe(func(e store.Event) {
			switch e.Kind {
			case store.Uploading:
				v.progress(Progress{Phase: PhaseUpload, Done: e.Done, Total: e.Total, Unit: UnitShards})
			default:
				v.progress(Progress{Phase: PhasePin})
			}
		})
	}
	if v.reclaimer != nil {
		v.reclaimer.Observe(func(e reclaim.Event) {
			phase, ok := reclaimPhases[e.Stage]
			if !ok {
				return
			}
			v.progress(Progress{Phase: phase, Total: e.Total, Unit: UnitRecords})
		})
	}
}

var reclaimPhases = map[reclaim.Stage]Phase{
	reclaim.StageSweep:        PhaseSweep,
	reclaim.StageUnledgered:   PhaseUnledgered,
	reclaim.StageOrphans:      PhaseOrphans,
	reclaim.StageUnreadable:   PhaseUnreadable,
	reclaim.StageRepackRead:   PhaseRepackRead,
	reclaim.StageRepackRetire: PhaseRepackRetire,
}
