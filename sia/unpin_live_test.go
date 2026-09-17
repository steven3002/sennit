package sia

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/steven3002/sennit/keys"
)

// UnpinLiveEnv opts this file in to talking to a real indexer. It is the same
// variable the other live tests use.
const UnpinLiveEnv = "SENNIT_LIVE"

// TestLiveUnpinningASlabTheIndexerHasNeverSeen asks what the indexer says when
// it is told to release a slab that has never existed.
//
// It is a state the write path can reach. Each slab is written into the
// device's ledger before it is pinned, so that an interrupt anywhere in the
// pinning leaves a slab something can still find. If the slab pin itself is
// what failed, the ledger then names a slab the account never had, and the next
// reclamation will try to unpin it. UnpinSlab is built to treat an absent slab
// as success, and that tolerance is tested offline against the wording the live
// indexer returned for a slab it had released. A slab it has never seen at all
// is a different question, and only the service can answer it. If the answer
// has a different shape the ledger row is stuck, and every later sweep fails on
// the same slab.
//
// It pins nothing, deletes nothing and costs nothing. The id is 32 random
// bytes, so it addresses no slab on this account or any other, and releasing
// something that does not exist removes nothing.
func TestLiveUnpinningASlabTheIndexerHasNeverSeen(t *testing.T) {
	if os.Getenv(UnpinLiveEnv) == "" {
		t.Skipf("set %s=1 to run against a real indexer", UnpinLiveEnv)
	}
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	client, err := Connect(Config{AppKey: appKey})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random slab id: %v", err)
	}
	id := SlabID(hex.EncodeToString(raw))
	t.Logf("INVENTED SLAB %s", id)

	// The SDK call underneath UnpinSlab, so the reply is seen before any
	// tolerance is applied to it.
	parsed, err := parseSlabID(id)
	if err != nil {
		t.Fatalf("parse the invented id: %v", err)
	}
	rawErr := client.app.UnpinSlab(context.Background(), client.appKey, parsed)
	if rawErr == nil {
		t.Logf("RAW REPLY: no error at all; the indexer treats it as done")
	} else {
		t.Logf("RAW REPLY: %v", rawErr)
	}

	if !isSlabGone(rawErr, id) {
		t.Errorf("isSlabGone does not recognise this reply, so a ledger row for a slab that was "+
			"never pinned would be stuck and every later sweep would fail on it: %v", rawErr)
	}

	// What a reclamation actually calls, which is the behaviour that matters.
	if err := client.UnpinSlab(context.Background(), id); err != nil {
		t.Errorf("UnpinSlab on a slab the indexer never had: %v", err)
	}
}
