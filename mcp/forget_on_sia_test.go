package mcp_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// flushedInAnEarlierRun leaves records under home the way a flush in an earlier
// run leaves them: each one catalogued at an object on the network, and none of
// them queued any more. The vault under home is closed while it runs.
func flushedInAnEarlierRun(t *testing.T, home string, records map[record.ID]record.Kind) {
	t.Helper()
	seed, err := keys.SeedFromPhrase(testPhrase)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	hierarchy, err := keys.Derive(seed)
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Manifest, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	defer sealer.Close()
	log, err := manifest.OpenLog(home, sealer)
	if err != nil {
		t.Fatalf("open the catalog: %v", err)
	}
	catalog, err := manifest.Load(log)
	if err != nil {
		t.Fatalf("load the catalog: %v", err)
	}
	defer catalog.Close()
	ids := make([]record.ID, 0, len(records))
	for id, kind := range records {
		var ref sia.ObjectRef
		copy(ref.ID[:], id[:])
		if err := catalog.Append(manifest.Entry{
			ID: id, Kind: kind, Version: 1, ObjectRef: ref.String(), WrittenAt: record.Now(),
		}); err != nil {
			t.Fatalf("catalogue %s: %v", id, err)
		}
		ids = append(ids, id)
	}

	device, err := local.Open(filepath.Join(home, "vault.db"))
	if err != nil {
		t.Fatalf("open the device store: %v", err)
	}
	defer device.Close()
	if err := device.DropQueued(ids); err != nil {
		t.Fatalf("take the flushed records out of the queue: %v", err)
	}
}

// says checks that a forget's note says every one of want, and none of never.
func says(t *testing.T, what string, out mcp.ForgetOut, want, never []string) {
	t.Helper()
	if !out.Removed {
		t.Errorf("forgetting %s reported removing nothing", what)
	}
	for _, phrase := range want {
		if !strings.Contains(out.Note, phrase) {
			t.Errorf("forgetting %s does not say %q: %q", what, phrase, out.Note)
		}
	}
	for _, phrase := range never {
		if strings.Contains(out.Note, phrase) {
			t.Errorf("forgetting %s says %q: %q", what, phrase, out.Note)
		}
	}
}

// Forgetting a record that never reached Sia says so: no other device can have
// it or restore it, and there is nothing left there to delete.
//
// The answer used to be the same whether or not the record had reached Sia, so
// it said nothing at all about what another device could still restore.
func TestForgettingARecordThatNeverReachedSiaSaysNoOtherDeviceCanRestoreIt(t *testing.T) {
	session, _ := serve(t)
	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the Tidepool field guide, written to be forgotten before it is flushed.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	})
	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})

	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: memory, Confirm: true}, &forgotten)
	says(t, "a memory still queued", forgotten,
		[]string{"The memory is gone from this vault.", "It had not reached Sia, so no other device can restore it."},
		[]string{"not deleted yet", "freed space"})

	call(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true}, &forgotten)
	says(t, "a conversation still queued", forgotten,
		[]string{"It had not reached Sia, so no other device can restore it.",
			"Memories drawn from it are untouched and are still stored."},
		[]string{"not deleted yet"})
}

// Forgetting a record that reached Sia says that its copy there is not deleted
// yet, what deletes it, what a restore on another device can still do until
// then, and that a device which already holds the record keeps it. This server
// has no connection to Sia, and the answer says that too.
func TestForgettingARecordOnSiaSaysWhatAnotherDeviceCanStillRestore(t *testing.T) {
	home := t.TempDir()
	v := openVault(t, home)
	ctx := context.Background()
	memory, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "The river gauge at Tidepool reads in millimetres.",
		Context:   "From the Tidepool field guide, written to be forgotten after it reached the network.",
		Type:      record.TypeFact,
		Tags:      []string{"river", "gauge"},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	flushed := map[record.ID]record.Kind{memory.ID: record.KindMemory}
	for _, chunk := range saved.Chunks {
		flushed[chunk.ID] = record.KindChunk
	}
	v.Close()
	flushedInAnEarlierRun(t, home, flushed)

	session := connect(t, mcp.New(openVault(t, home)))
	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: mcp.URI(record.KindMemory, memory.ID), Confirm: true}, &forgotten)
	says(t, "a memory on Sia", forgotten, []string{
		"The memory is gone from this vault.",
		"Its copy on Sia is not deleted yet, because this server has no connection to Sia",
		"`sennit flush` deletes it",
		"a `sennit hydrate` or `sennit recover` on another device can still restore it",
		"do not tell the user it is gone from Sia yet",
		"Another device that already holds the memory keeps it",
		"do not report freed space",
	}, []string{"had not reached Sia"})

	call(t, session, "forget", mcp.ForgetIn{URI: mcp.URI(record.KindSession, saved.ID), Confirm: true}, &forgotten)
	says(t, "a conversation on Sia", forgotten, []string{
		"The conversation and its transcript are gone from this vault.",
		"Its transcript on Sia is not deleted yet",
		"can still rebuild the conversation from it",
		"Another device that already holds the conversation keeps it",
		"Memories drawn from it are untouched and are still stored.",
	}, []string{"had not reached Sia"})
}
