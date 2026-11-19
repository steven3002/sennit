package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/profile"
	"github.com/steven3002/sennit/profile/profiletest"
	"github.com/steven3002/sennit/vault"
)

func newAppKey(t *testing.T) keys.AppKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate an app key: %v", err)
	}
	return keys.AppKey(private)
}

// storedFor points the server at a store of the test's own, with an empty
// environment, and saves p in it unless p has no phrase.
func storedFor(t *testing.T, p profile.Profile) {
	t.Helper()
	t.Setenv(keys.PhraseEnv, "")
	t.Setenv(keys.AppKeyEnv, "")
	t.Setenv(vault.IndexerEnv, "")
	store := profile.At(t.TempDir(), profiletest.NewKeychain())
	if p.Phrase != "" {
		if _, err := store.Save(p); err != nil {
			t.Fatalf("save the profile: %v", err)
		}
	}
	before := openProfiles
	openProfiles = func() (*profile.Store, error) { return store, nil }
	t.Cleanup(func() { openProfiles = before })
}

// An MCP host's configuration can name the program and nothing else: the
// phrase, the key and the key's indexer come from the stored profile.
func TestTheServerOpensWithTheStoredProfile(t *testing.T) {
	stored := profile.Profile{Phrase: testPhrase, AppKey: newAppKey(t), Indexer: "https://indexer.example"}
	storedFor(t, stored)

	var log strings.Builder
	creds, err := credentialsFor(&log)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if creds.phrase != stored.Phrase || hex.EncodeToString(creds.key) != hex.EncodeToString(stored.AppKey) {
		t.Errorf("the server did not take the stored phrase and key")
	}
	if creds.indexer != stored.Indexer {
		t.Errorf("the server uses %s, want the indexer that issued the stored key, %s", creds.indexer, stored.Indexer)
	}
	if want := "phrase and app key from the default profile, in the test keychain"; creds.from != want {
		t.Errorf("the server says %q, want %q", creds.from, want)
	}
	if log.Len() != 0 {
		t.Errorf("the server logged %q for an ordinary start", log.String())
	}
}

// The environment still comes first, and a key there that is not the stored
// one is used and said in the log.
func TestTheServerSaysWhenAVariableOverridesTheStoredKey(t *testing.T) {
	storedFor(t, profile.Profile{Phrase: testPhrase, AppKey: newAppKey(t), Indexer: "https://indexer.example"})
	other := newAppKey(t)
	t.Setenv(keys.PhraseEnv, testPhrase)
	t.Setenv(keys.AppKeyEnv, hex.EncodeToString(other))

	var log strings.Builder
	creds, err := credentialsFor(&log)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if hex.EncodeToString(creds.key) != hex.EncodeToString(other) {
		t.Errorf("the stored key won over the environment's")
	}
	if creds.indexer == "https://indexer.example" {
		t.Errorf("a key from the environment took the stored key's indexer")
	}
	if want := "phrase from SENNIT_PHRASE, app key from SENNIT_APP_KEY"; creds.from != want {
		t.Errorf("the server says %q, want %q", creds.from, want)
	}
	if !strings.Contains(log.String(), keys.AppKeyEnv+" is set, and is not the app key sennit connect stored") {
		t.Errorf("the override went unsaid: %q", log.String())
	}
}

// A stored key's indexer gives way to one the environment names.
func TestTheServersIndexerCanStillBeNamed(t *testing.T) {
	storedFor(t, profile.Profile{Phrase: testPhrase, AppKey: newAppKey(t), Indexer: "https://indexer.example"})
	t.Setenv(vault.IndexerEnv, "https://named.example")

	creds, err := credentialsFor(&strings.Builder{})
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if creds.indexer != "https://named.example" {
		t.Errorf("the server uses %s, want the one the environment named", creds.indexer)
	}
}

// With nothing stored and nothing set, the server reports what it always did,
// and is explained the way it always was.
func TestWithNothingStoredTheServerExplainsWhatItNeeds(t *testing.T) {
	storedFor(t, profile.Profile{})
	_, err := credentialsFor(&strings.Builder{})
	if !errors.Is(err, keys.ErrNoPhrase) {
		t.Fatalf("nothing stored and nothing set: %v, want ErrNoPhrase", err)
	}
	var log strings.Builder
	explain(&log, err)
	for _, says := range []string{"Run sennit connect once", "Set " + keys.PhraseEnv} {
		if !strings.Contains(log.String(), says) {
			t.Errorf("the explanation does not say %q: %q", says, log.String())
		}
	}
}

// Nothing the server says about a missing phrase suggests piping one in. Its
// stdin is the protocol, so the advice could only be followed by breaking it.
// The log line and every tool's error carry the error's own text, and the log
// adds the explanation.
func TestTheServerNeverSuggestsPipingAPhrase(t *testing.T) {
	storedFor(t, profile.Profile{})
	_, err := credentialsFor(&strings.Builder{})
	if !errors.Is(err, keys.ErrNoPhrase) {
		t.Fatalf("nothing stored and nothing set: %v, want ErrNoPhrase", err)
	}
	var log strings.Builder
	notOpened(&log, t.Context(), err)
	for name, said := range map[string]string{"the error": err.Error(), "the log": log.String()} {
		if strings.Contains(strings.ToLower(said), "pipe") {
			t.Errorf("%s suggests piping the phrase to a server whose stdin is the protocol: %q", name, said)
		}
	}
}
