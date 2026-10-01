package vault

import (
	"fmt"
	"testing"

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
