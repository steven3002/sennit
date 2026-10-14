package vault

import (
	"fmt"
	"testing"
	"time"

	"github.com/steven3002/sennit/record"
)

// A vector written while the index is being compacted survives the compaction:
// a record forgotten meanwhile stays forgotten in the next process, and one
// stored meanwhile is still findable there.
//
// Compacting read every vector off the disk, let go of the index, and took it
// again to write the new base and empty the delta. A write that had waited on
// the index while it was read was handed it in between, landed in the delta,
// and was emptied out with it. A removal was undone, so the forgotten record's
// vector was back the next time the vault opened and a recall that ranked it
// failed; an addition was lost, so a record just stored could not be found by
// meaning. The index in memory hid both until the process ended.
func TestAVectorWrittenDuringACompactionIsNotLost(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)

	var forgotten, kept []record.ID
	for i := range 40 {
		forgotten = append(forgotten, rememberFact(t, v,
			fmt.Sprintf("Gauge %d at Tidepool was retired.", i), "retired"))
	}

	stop := make(chan struct{})
	compacted := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				compacted <- nil
				return
			default:
			}
			if err := v.CompactIndex(); err != nil {
				compacted <- err
				return
			}
		}
	}()
	for i, id := range forgotten {
		if err := v.Forget(id); err != nil {
			t.Errorf("forget %s: %v", id, err)
		}
		kept = append(kept, rememberFact(t, v, fmt.Sprintf("Gauge %d at Tidepool reads in centimetres.", i), "gauge"))
	}
	close(stop)
	if err := <-compacted; err != nil {
		t.Fatalf("compact the index: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := ledgerVault(t, home)
	defer reopened.Close()
	var back, lost int
	for _, id := range forgotten {
		if reopened.index.Has(id) {
			back++
		}
	}
	for _, id := range kept {
		if !reopened.index.Has(id) {
			lost++
		}
	}
	if back > 0 {
		t.Errorf("%d of %d forgotten record(s) are searchable again in the next process", back, len(forgotten))
	}
	if lost > 0 {
		t.Errorf("%d of %d stored record(s) cannot be found by meaning in the next process", lost, len(kept))
	}
}

// A command run while an agent's server has the same vault open is a second
// process on one vault directory. What it stores while the server compacts the
// index is findable by the next process to open the vault, and what it forgets
// stays forgotten there.
//
// The server's compaction read the index's files, wrote a new base from what it
// had read, and only then locked the delta, to empty it. A vector the command
// appended in between was in neither file afterwards, so the memory it had just
// reported stored could not be found by meaning again, by anything, and a
// removal was undone the same way. Both processes also wrote their new base to
// one pending file, and on Windows the base cannot be renamed over while the
// other process has it open to read.
func TestWhatOneProcessWritesWhileAnotherCompactsIsNotLost(t *testing.T) {
	home := t.TempDir()
	server := ledgerVault(t, home)
	defer server.Close()

	var forgotten, kept []record.ID
	for i := range 30 {
		forgotten = append(forgotten, rememberFact(t, server,
			fmt.Sprintf("Buoy %d off Tidepool was withdrawn.", i), "withdrawn"))
	}
	command := ledgerVault(t, home)
	defer command.Close()

	stop := make(chan struct{})
	compacted := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				compacted <- nil
				return
			default:
			}
			if err := server.CompactIndex(); err != nil {
				compacted <- err
				return
			}
			// The index's lock is not handed out in turn, so a loop that took
			// it again at once would keep the other process waiting for most of
			// the test.
			time.Sleep(time.Millisecond)
		}
	}()
	for i, id := range forgotten {
		if err := command.Forget(id); err != nil {
			t.Errorf("forget %s from the other process: %v", id, err)
		}
		kept = append(kept, rememberFact(t, command, fmt.Sprintf("Buoy %d off Tidepool reports every ten minutes.", i), "buoy"))
	}
	close(stop)
	if err := <-compacted; err != nil {
		t.Fatalf("compact the index while another process writes to it: %v", err)
	}
	if err := command.Close(); err != nil {
		t.Fatalf("close the command's vault: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close the server's vault: %v", err)
	}

	next := ledgerVault(t, home)
	defer next.Close()
	var back, lost int
	for _, id := range forgotten {
		if next.index.Has(id) {
			back++
		}
	}
	for _, id := range kept {
		if !next.index.Has(id) {
			lost++
		}
	}
	if back > 0 {
		t.Errorf("%d of %d record(s) one process forgot are searchable again in the next", back, len(forgotten))
	}
	if lost > 0 {
		t.Errorf("%d of %d record(s) one process stored cannot be found by meaning in the next", lost, len(kept))
	}
}
