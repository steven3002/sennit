package vault_test

import (
	"context"
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// A resume of an id that names no conversation on this device says what the
// device has not restored, on every kind of device a hydrate leaves. A memory's
// id is refused as a memory's wherever this device can tell it is one, from its
// catalog as well as from what it holds.
//
// Each of these devices refused the id with "no record at that address" and its
// address. On a device that had not restored the conversation, that read as
// though the vault did not hold it, and it did not say that the id was a
// memory's on a device whose catalog said so.
func TestAResumeOfAnIdThatNamesNoConversationHereSaysWhatThisDeviceHasNotRestored(t *testing.T) {
	ctx := context.Background()
	writer := vault.OpenWithLedgerForTest(t, t.TempDir())
	memory := remember(t, writer, harbourGauge, record.TypeFact, "harbour", "gauge")
	network := vault.FlushToNetworkForTest(t, writer)
	// Two conversations, each flushed on its own, so that a device can restore
	// one and know of the other only from its catalog.
	var (
		conversations [2]record.ID
		transcripts   [2][]vault.StoredObject
	)
	for i, prefix := range []string{"survey", "calibration"} {
		saved, err := writer.SaveSession(ctx, vault.SaveSessionRequest{
			Title:    "Tidepool station " + prefix,
			Summary:  "Counted the stations reporting hourly.",
			Messages: conversation(prefix),
		})
		if err != nil {
			t.Fatalf("save the %s conversation: %v", prefix, err)
		}
		conversations[i], transcripts[i] = saved.ID, vault.FlushToNetworkForTest(t, writer)
		network = append(network, transcripts[i]...)
	}
	other := conversations[1]

	catalogued := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, catalogued, network, vault.HydrateCatalog)
	// It holds the first conversation, and knows of the second and the memory
	// only from its catalog.
	partly := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, partly, network, vault.HydrateCatalog)
	vault.RestoreForTest(t, partly, transcripts[0], vault.HydrateMetadata)
	// It holds both conversations, and knows of the memory only from its
	// catalog.
	conversationsOnly := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, conversationsOnly, network, vault.HydrateCatalog)
	vault.RestoreForTest(t, conversationsOnly,
		append(append([]vault.StoredObject{}, transcripts[0]...), transcripts[1]...), vault.HydrateMetadata)
	restored := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, restored, network, vault.HydrateIndex)

	const instead = "Run /mcp__sennit__resume alone to choose from the recent conversations, or with what " +
		"the conversation was about to search for it"
	zero := strings.Repeat("0", 32)
	notHeld := "nothing was resumed, because this vault holds no conversation at " + mcp.Scheme + "session/" +
		zero + ": it was never saved here, or it has been forgotten. " + instead
	memoryRefused := "nothing was resumed, because " + memory.String() + " is the id of a memory, not of a " +
		"conversation, and a memory cannot be resumed. Ask your agent to read it with `open` on " +
		mcp.URI(record.KindMemory, memory)

	for _, tc := range []struct {
		name   string
		device *vault.Vault
		// id is one this device holds no conversation under.
		id string
		// alone says that this device holds no conversation at all, so that
		// nothing the user could run instead would find one, and the id is
		// answered as a resume that names nothing is.
		alone bool
		// says is what the answer says, and never what it must not say.
		says, never []string
		// memory says whether this device can tell that the memory's id is a
		// memory's. One that cannot answers it as it answers the id.
		memory bool
	}{
		{"a device that never hydrated", vault.OpenWithLedgerForTest(t, t.TempDir()), other.String(), true,
			[]string{"has not restored any record of this vault", "`sennit hydrate`"}, []string{instead}, false},
		{"a device hydrated at catalog depth", catalogued, other.String(), true,
			[]string{"at least one stored conversation", "`sennit hydrate`"}, []string{instead}, true},
		{"a device holding one conversation and knowing of another", partly, other.String(), false,
			[]string{"nothing was resumed, because this device holds no conversation at " +
				mcp.URI(record.KindSession, other) + ", though it knows of at least one stored conversation " +
				"on the network that it has not restored. `sennit hydrate` restores this vault's records " +
				"from the network, conversations included. " + instead},
			// The memory it has not restored is not what a resume is short of.
			[]string{"memory record"}, true},
		{"a device holding every conversation and not the memory", conversationsOnly, zero, false,
			[]string{notHeld}, []string{"hydrate", "has not restored"}, true},
		{"a device that restored the vault", restored, zero, false,
			[]string{notHeld}, []string{"hydrate", "has not restored"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := serveDevice(t, tc.device)
			got := resumeRefusal(t, session, map[string]string{"session": tc.id})
			if got == "" {
				t.Fatalf("resume %s went ahead on a device holding no conversation under that id", tc.id)
			}
			alone := resumeRefusal(t, session, nil)
			if tc.alone && got != alone {
				t.Errorf("resume %s answered\n%q\nwant what a resume that names nothing answers\n%q", tc.id, got, alone)
			}
			for _, says := range tc.says {
				if !strings.Contains(got, says) {
					t.Errorf("resume %s answered\n%q\nwhich does not say\n%q", tc.id, got, says)
				}
			}
			for _, never := range tc.never {
				if strings.Contains(got, never) {
					t.Errorf("resume %s answered\n%q\nwhich says %q", tc.id, got, never)
				}
			}

			got = resumeRefusal(t, session, map[string]string{"session": memory.String()})
			switch {
			case tc.memory && !strings.Contains(got, memoryRefused):
				t.Errorf("resume of the memory's id answered\n%q\nwhich does not say\n%q", got, memoryRefused)
			case !tc.memory && got != alone:
				t.Errorf("resume of the memory's id answered\n%q\nwant what a resume that names nothing "+
					"answers\n%q", got, alone)
			}
		})
	}
}
