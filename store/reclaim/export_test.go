package reclaim

import "github.com/steven3002/sennit/local"

// CheckOccupancyForTest exposes the orphan scan's I12 guard to the package's
// external test. The guard sits between two indexer calls that a test cannot
// make, so it is reached directly rather than not at all.
var CheckOccupancyForTest = checkOccupancy

// PlanSweepForTest exposes the sweep's decision so shared-slab correctness can
// be checked without spending storage on every case.
var PlanSweepForTest = planSweep

// OrphansOfForTest exposes the orphan scan's decision, which sits between two
// indexer calls a test cannot make.
var OrphansOfForTest = orphansOf

// ReleasingForTest exposes what a release of orphans unpins and what it keeps,
// which ReleaseOrphans acts on between indexer calls a test cannot make.
var ReleasingForTest = releasing

// NewOverForTest builds a reclaimer whose calls to the indexer go to net rather
// than to a client, over the device's ledger in store. A repack asks nothing
// else of the network, so the package's external tests can run one whole.
func NewOverForTest(net network, store *local.Store) *Reclaimer {
	return &Reclaimer{net: net, store: store}
}
