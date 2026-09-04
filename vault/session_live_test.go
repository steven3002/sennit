package vault_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// TestLiveSessionChunksRideThePackerAndComeBack is the session half of the
// end-to-end claim: a transcript written through the vault reaches Sia inside a
// shared slab, and comes back off the network with its tool correlation and its
// provider fields intact.
//
// It reclaims what it wrote. A session is the largest record the vault holds and
// this is the one test that pins storage for one, so it deletes its own objects
// and releases the slab afterwards and reports the net.
func TestLiveSessionChunksRideThePackerAndComeBack(t *testing.T) {
	v := liveVault(t)
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("wait for the account: %v", err)
	}
	before, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("read the account: %v", err)
	}

	messages := conversation("live")
	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Live session round trip",
		Summary:  "A conversation written to Sia and read back off the network.",
		Tags:     []string{"sia", "sessions"},
		Agent:    record.Agent{Name: "sennit-test"},
		Messages: messages,
		Durable:  true,
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}
	if !saved.OnNetwork {
		t.Fatal("a session saved with Durable set did not reach the network")
	}
	if saved.Flushed == nil || len(saved.Flushed.Slabs) == 0 {
		t.Fatal("the flush reported no slab")
	}
	t.Logf("%d chunk(s), %d bytes of transcript, flushed into %d slab(s) in %v",
		len(saved.Chunks), saved.Bytes, len(saved.Flushed.Slabs), saved.FlushFor.Round(1e6))

	// Drop this device's copy of every chunk so the reload has to come off the
	// network. Without this the read is served from the local store and proves
	// nothing about what was stored.
	for _, chunk := range saved.Chunks {
		if err := v.ForgetLocally(chunk.ID); err != nil {
			t.Fatalf("drop the local copy of chunk %s: %v", chunk.ID, err)
		}
	}

	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load from the network: %v", err)
	}
	if len(loaded.Messages) != len(messages) {
		t.Fatalf("the network returned %d messages, wrote %d", len(loaded.Messages), len(messages))
	}
	for i := range messages {
		want, err := json.Marshal(messages[i])
		if err != nil {
			t.Fatalf("marshal message %d: %v", i, err)
		}
		got, err := json.Marshal(loaded.Messages[i])
		if err != nil {
			t.Fatalf("marshal returned message %d: %v", i, err)
		}
		if string(want) != string(got) {
			t.Fatalf("message %d changed on the way through Sia\n wrote: %s\n  read: %s", i, want, got)
		}
	}
	exchanges := record.ToolExchanges(loaded.Messages)
	if len(exchanges) != 1 || !exchanges[0].Matched {
		t.Fatalf("the tool exchange did not survive the network: %+v", exchanges)
	}

	// The stored bytes hold no plaintext of the conversation.
	stored, err := v.StoredBytes(ctx, saved.Chunks[0].ID)
	if err != nil {
		t.Fatalf("read the stored object: %v", err)
	}
	for _, plaintext := range []string{"tide stations", "Forty-one", "registry.json", "toolu_live"} {
		if bytes.Contains(stored, []byte(plaintext)) {
			t.Fatalf("the stored chunk contains the plaintext %q", plaintext)
		}
	}

	after, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("read the account: %v", err)
	}
	t.Logf("quota: %d B before, %d B after the write (+%d B)",
		before.PinnedData, after.PinnedData, after.PinnedData-before.PinnedData)

	// Reclaim what this test pinned. Objects first and slabs second: releasing a
	// slab that objects still point into strands them permanently.
	if err := v.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget the session: %v", err)
	}
	sweep, err := v.Reclaim(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	t.Logf("reclaimed: %d object(s) deleted, %d slab(s) released, %d B returned; quota %d B → %d B",
		sweep.ObjectsDeleted, sweep.SlabsReleased, sweep.Freed(),
		sweep.Before.PinnedData, sweep.After.PinnedData)
	if sweep.After.PinnedData > before.PinnedData {
		t.Errorf("the account is %d B above where this test found it",
			sweep.After.PinnedData-before.PinnedData)
	}
}
