package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/profile"
	"github.com/steven3002/sennit/profile/profiletest"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

const storedPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

func anAppKey(t *testing.T) keys.AppKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate an app key: %v", err)
	}
	return keys.AppKey(private)
}

// withStoredProfile gives the screen's session a store of the test's own, with
// a profile saved in it, and an environment that supplies nothing.
func withStoredProfile(t *testing.T, s *screen, indexer string) (profile.Profile, *profiletest.Keychain) {
	t.Helper()
	t.Setenv(keys.PhraseEnv, "")
	t.Setenv(keys.AppKeyEnv, "")
	t.Setenv(vault.IndexerEnv, "")
	keychain := profiletest.NewKeychain()
	s.out.profiles = profile.At(t.TempDir(), keychain)
	p := profile.Profile{Phrase: storedPhrase, AppKey: anAppKey(t), Indexer: indexer}
	if _, err := s.out.profiles.Save(p); err != nil {
		t.Fatalf("save the profile: %v", err)
	}
	return p, keychain
}

// statusFlags parses a command that opens a vault, the way status does.
func statusFlags(t *testing.T, s *screen, args ...string) *vaultFlags {
	t.Helper()
	cmd := newInvocation("status").withVault()
	if err := cmd.parse(s.out, args); err != nil {
		t.Fatalf("parse %q: %v", args, err)
	}
	return cmd.vault
}

// With nothing in the environment and a profile stored, the phrase, the key
// and the key's indexer come from the profile, and nobody is asked.
func TestAStoredProfileSuppliesWhatTheEnvironmentDoesNot(t *testing.T) {
	s, asks := asked(t, characterDevice(t), true, "a typed phrase", nil)
	stored, _ := withStoredProfile(t, s, "https://indexer.example")

	phrase, err := s.out.readPhrase()
	if err != nil {
		t.Fatalf("read the phrase: %v", err)
	}
	if phrase != stored.Phrase || s.out.sources.phrase != fromProfile {
		t.Errorf("the phrase came from %q, want the stored profile", s.out.sources.phrase)
	}
	if *asks != 0 {
		t.Errorf("the terminal was asked %d time(s) with a phrase stored", *asks)
	}

	flags := statusFlags(t, s)
	key, err := flags.appKey(s.out)
	if err != nil {
		t.Fatalf("the app key: %v", err)
	}
	if hex.EncodeToString(key) != hex.EncodeToString(stored.AppKey) || s.out.sources.key != fromProfile {
		t.Errorf("the key came from %q, want the stored profile", s.out.sources.key)
	}
	if flags.indexer != stored.Indexer {
		t.Errorf("the run uses %s, want the indexer that issued the stored key, %s", flags.indexer, stored.Indexer)
	}
}

// The environment comes first, as it always has, and a variable naming a key
// other than the stored one is used and said out loud.
func TestTheEnvironmentComesBeforeTheStoredProfile(t *testing.T) {
	s, _ := asked(t, characterDevice(t), true, "", nil)
	withStoredProfile(t, s, sia.DefaultIndexer)
	other := anAppKey(t)
	t.Setenv(keys.PhraseEnv, "twelve other words")
	t.Setenv(keys.AppKeyEnv, hex.EncodeToString(other))

	phrase, err := s.out.readPhrase()
	if err != nil || phrase != "twelve other words" || s.out.sources.phrase != keys.PhraseEnv {
		t.Errorf("the phrase came from %q (%v), want %s", s.out.sources.phrase, err, keys.PhraseEnv)
	}
	key, err := statusFlags(t, s).appKey(s.out)
	if err != nil || hex.EncodeToString(key) != hex.EncodeToString(other) || s.out.sources.key != keys.AppKeyEnv {
		t.Errorf("the key came from %q (%v), want %s", s.out.sources.key, err, keys.AppKeyEnv)
	}
	said := s.text()
	if !strings.Contains(said, keys.AppKeyEnv+" is set, and is not the app key") ||
		!strings.Contains(said, "remove "+keys.AppKeyEnv+" from the environment") {
		t.Errorf("a variable overriding a different stored key went unsaid:\n%s", said)
	}
}

// A variable naming the stored key itself is no news, and says nothing.
func TestTheStoredKeyInTheEnvironmentSaysNothing(t *testing.T) {
	s, _ := asked(t, characterDevice(t), true, "", nil)
	stored, _ := withStoredProfile(t, s, sia.DefaultIndexer)
	t.Setenv(keys.AppKeyEnv, hex.EncodeToString(stored.AppKey))

	if _, err := statusFlags(t, s).appKey(s.out); err != nil {
		t.Fatalf("the app key: %v", err)
	}
	if s.text() != "" {
		t.Errorf("the stored key in the environment was warned about:\n%s", s.text())
	}
}

// A stored key brings the indexer that issued it, unless this run names one.
func TestAStoredKeyBringsItsIndexerUnlessTheRunNamesOne(t *testing.T) {
	t.Run("--indexer", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", nil)
		withStoredProfile(t, s, "https://indexer.example")
		flags := statusFlags(t, s, "--indexer", "https://named.example")
		if _, err := flags.appKey(s.out); err != nil {
			t.Fatalf("the app key: %v", err)
		}
		if flags.indexer != "https://named.example" {
			t.Errorf("the run uses %s, want the one it named", flags.indexer)
		}
	})
	t.Run(vault.IndexerEnv, func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", nil)
		withStoredProfile(t, s, "https://indexer.example")
		t.Setenv(vault.IndexerEnv, "https://from-the-environment.example")
		flags := statusFlags(t, s)
		if _, err := flags.appKey(s.out); err != nil {
			t.Fatalf("the app key: %v", err)
		}
		if flags.indexer != "https://from-the-environment.example" {
			t.Errorf("the run uses %s, want the one the environment named", flags.indexer)
		}
	})
	t.Run("a key from the environment", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", nil)
		withStoredProfile(t, s, "https://indexer.example")
		t.Setenv(keys.AppKeyEnv, hex.EncodeToString(anAppKey(t)))
		flags := statusFlags(t, s)
		if _, err := flags.appKey(s.out); err != nil {
			t.Fatalf("the app key: %v", err)
		}
		if flags.indexer != sia.DefaultIndexer {
			t.Errorf("a key from the environment took the stored key's indexer, %s", flags.indexer)
		}
	})
}

// A profile that cannot be read stops the run with the reason, rather than
// asking for a phrase as though nothing were stored.
func TestAProfileThatCannotBeReadSaysWhy(t *testing.T) {
	s, asks := asked(t, characterDevice(t), true, "a typed phrase", nil)
	_, keychain := withStoredProfile(t, s, sia.DefaultIndexer)
	keychain.Refuse = errors.New("the keyring is locked")

	_, err := s.out.readPhrase()
	if err == nil || !strings.Contains(err.Error(), "the keyring is locked") {
		t.Errorf("a profile that cannot be read gave %v, want the store's reason", err)
	}
	if *asks != 0 {
		t.Errorf("the terminal was asked as though nothing were stored")
	}
}

// connect does not put a different phrase in place of a stored one unasked.
func TestConnectKeepsADifferentStoredPhrase(t *testing.T) {
	s := terminal(t, 80)
	if err := keepsTheStoredPhrase(s.out, "any phrase"); err != nil {
		t.Errorf("with nothing stored: %v", err)
	}
	withStoredProfile(t, s, sia.DefaultIndexer)
	if err := keepsTheStoredPhrase(s.out, storedPhrase); err != nil {
		t.Errorf("the stored phrase itself was refused: %v", err)
	}
	err := keepsTheStoredPhrase(s.out, "a different phrase altogether")
	var p *problem
	if !errors.As(err, &p) || !strings.Contains(p.message.Text, "a different recovery phrase is stored") {
		t.Errorf("a different phrase gave %v, want the refusal", err)
	}
}

func TestStatusSaysWhereTheSecretsCameFrom(t *testing.T) {
	keychain := profile.Place{Name: "Windows Credential Manager"}
	file := profile.Place{InFile: true, Name: "/home/ada/.config/sennit/secrets.json"}
	for _, c := range []struct {
		sources credentialSources
		place   profile.Place
		want    string
	}{
		{credentialSources{fromProfile, fromProfile}, keychain,
			"phrase and app key from the default profile, in Windows Credential Manager"},
		{credentialSources{fromProfile, fromProfile}, file,
			"phrase and app key from the default profile, in /home/ada/.config/sennit/secrets.json"},
		{credentialSources{keys.PhraseEnv, keys.AppKeyEnv}, keychain,
			"phrase from SENNIT_PHRASE, app key from SENNIT_APP_KEY"},
		{credentialSources{keys.PhraseEnv, fromProfile}, keychain,
			"phrase from SENNIT_PHRASE, app key from the default profile, in Windows Credential Manager"},
		{credentialSources{fromPrompt, ""}, keychain, "phrase from the prompt"},
		{credentialSources{}, keychain, ""},
	} {
		if got := secretsRow(c.sources, c.place); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.sources, got, c.want)
		}
	}

	s := terminal(t, 80)
	s.out.report(statusRows(heldView{path: "~/.sennit", secrets: "phrase and app key from the default " +
		"profile, in the macOS keychain", stored: 3, indexed: 3, model: "bge-small-en-v1.5-fp32"}))
	if !strings.Contains(s.text(), "\nSecrets  phrase and app key from the default profile, in the macOS keychain\n") {
		t.Errorf("the status has no line saying where the secrets came from:\n%s", s.text())
	}
}

// What connect ends with when it stores the profile, in the credential store
// and, on a machine with none, in a file.
func TestWhatConnectSaysWhenItStores(t *testing.T) {
	key := keys.AppKey(append([]byte{0x3f, 0x9a, 0x1c, 0x2b}, make([]byte, 60)...))
	rows := connectRows(1, 72*time.Second, 16*time.Second, account(40*mib, 46*gib+583*mib+269228))
	for name, saved := range map[string]profile.Saved{
		"connect-stored.txt": {Place: profile.Place{Name: "Windows Credential Manager"}},
		"connect-stored-in-a-file.txt": {
			Place:      profile.Place{InFile: true, Name: "/home/ada/.config/sennit/secrets.json"},
			NoKeychain: errors.New("The name org.freedesktop.secrets was not provided by any .service files"),
		},
	} {
		s := ended(t, 80, 102*time.Second)
		connectedAndStored(s.out, key, saved, rows)
		s.assert(t, name, fixture(t, name))
	}
}
