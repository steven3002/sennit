package main

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// recalledThroughAPipe is what sennit recall prints through a pipe for one
// search, on a device that has restored what restoration says. A pipe is where
// a note is printed whole on one line, so a phrase in it is never split by a
// line break.
func recalledThroughAPipe(t *testing.T, restoration vault.Restoration, searched int, hits ...recall.Hit) string {
	t.Helper()
	s := sessionFor(t, 80, ui.Probe{GOOS: "linux", Getenv: func(string) string { return "" }})
	reportRecall(s.out, recall.Result{Searched: searched}, hits, restoration, false, false)
	return s.text()
}

// sennit recall says what this device has not restored, on every kind of device
// a hydrate leaves, from the check the server makes for the same answer.
//
// It used to say that the vault held nothing searchable whenever this device
// held nothing to search, and nothing at all when a device that had restored
// part of the vault answered from that part. The restorations below are the
// ones each kind of device reports: one that never hydrated is untouched, one
// hydrated at catalog depth knows of the two memories and the conversation of
// a vault like the one the server's tests restore and holds none of them, and
// one that then restored a single memory knows of the other memory and the
// conversation.
func TestSennitRecallSaysWhatThisDeviceHasNotRestored(t *testing.T) {
	harbour := recall.Hit{Found: recall.Found{Memory: &record.Memory{
		ID: id(t, "4083dbdd9e9f0f816c797080eed61fba"), Kind: record.KindMemory, Type: record.TypeFact,
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the fictional Tidepool field guide.",
		Tags:      []string{"harbour", "gauge"},
	}}, Similarity: 0.61}

	const command = "`sennit hydrate --depth index` restores"
	for _, c := range []struct {
		name        string
		restoration vault.Restoration
		hits        []recall.Hit
		// want is what the hint says, and wrong what it must not.
		want, wrong []string
	}{
		{name: "a device that never hydrated", restoration: vault.Restoration{Untouched: true},
			want: []string{"hint: nothing on this device is searchable yet",
				"it has not restored any record of this vault from Sia", "does not show the vault is empty",
				command, "`sennit remember` stores new ones"},
			wrong: []string{"this vault holds nothing"}},
		{name: "a device hydrated at catalog depth", restoration: vault.Restoration{Memories: 2, Chunks: 1},
			want: []string{"hint: nothing on this device is searchable yet",
				"it knows of 2 memories and at least one conversation on Sia that it has not restored", command},
			wrong: []string{"this vault holds nothing"}},
		{name: "a partly restored device", restoration: vault.Restoration{Memories: 1, Chunks: 1},
			hits: []recall.Hit{harbour},
			want: []string{"Found 1 memory", harbour.Memory.Statement,
				"hint: this device knows of 1 memory and at least one conversation on Sia that it has not " +
					"restored, so the search could not reach them", command}},
		{name: "a partly restored device missing only a conversation", restoration: vault.Restoration{Chunks: 2},
			hits: []recall.Hit{harbour},
			want: []string{"hint: this device knows of at least one conversation on Sia that it has not restored",
				command},
			wrong: []string{"memories", "0 memor"}},
		{name: "a device that restored the vault, holding nothing", restoration: vault.Restoration{},
			want:  []string{"hint: this vault holds nothing searchable yet; `sennit remember` stores the first record"},
			wrong: []string{"hydrate"}},
		{name: "a device that restored the vault, with results", restoration: vault.Restoration{},
			hits: []recall.Hit{harbour}, want: []string{"Found 1 memory"}, wrong: []string{"hint:"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			searched := 0
			if len(c.hits) > 0 {
				searched = 2
			}
			printed := recalledThroughAPipe(t, c.restoration, searched, c.hits...)
			for _, want := range c.want {
				if !strings.Contains(printed, want) {
					t.Errorf("the answer does not say %q:\n%s", want, printed)
				}
			}
			for _, wrong := range c.wrong {
				if strings.Contains(printed, wrong) {
					t.Errorf("the answer says %q:\n%s", wrong, printed)
				}
			}
		})
	}
}
