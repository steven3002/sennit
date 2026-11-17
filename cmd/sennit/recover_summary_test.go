package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/vault"
)

// What recover says once it has rebuilt conversations from the transcripts it
// put back: what it says of any recovery, and then what those conversations came
// back without, in the words hydrate uses for the same rebuild.
//
// A recovery rebuilt no conversation, so it said nothing of the ones whose
// transcripts it restored, and they were not listed until a hydrate rebuilt
// them.
func TestWhatRecoverSaysOfTheConversationsItRebuilt(t *testing.T) {
	// A recovery embeds what it restores unless told not to, and the heads it
	// rebuilds with it.
	rebuilt := rebuiltFromTranscripts(t, true,
		transcript{opening: "Why does every flush bill a whole slab?", dated: true},
		transcript{opening: "How long may a record wait in the queue?", dated: true},
		transcript{opening: "Which of the Tidepool gauges report hourly?"},
	)
	s := ended(t, 80, 56500*time.Millisecond)
	reportRecovered(s.out, vault.RecoveryReport{Recovered: 1008, Objects: 14, Foreign: 12, Rebuild: rebuilt})
	s.assert(t, "recover with rebuilt conversations", fixture(t, "recover-conversations.txt"))

	// The conversations whose heads this device still held are kept as they
	// were written, so there is nothing to say of them.
	kept := rebuiltFromTranscripts(t, true,
		transcript{opening: "Why does every flush bill a whole slab?", dated: true, kept: true},
		transcript{opening: "Which of the Tidepool gauges report hourly?", kept: true},
	)
	s = ended(t, 80, 56500*time.Millisecond)
	reportRecovered(s.out, vault.RecoveryReport{Recovered: 1008, Objects: 14, Rebuild: kept})
	if said := s.text(); strings.Contains(said, "conversation") {
		t.Errorf("a recovery that kept the head of every conversation it found says of them:\n%s", said)
	}
}
