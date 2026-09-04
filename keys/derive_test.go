package keys_test

import (
	"os"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
)

func TestSeedFromPhraseIsDeterministic(t *testing.T) {
	phrase := keys.NewPhrase()

	first, err := keys.SeedFromPhrase(phrase)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Whitespace is normalised, so a phrase pasted with odd spacing still
	// opens the vault it created.
	second, err := keys.SeedFromPhrase("  " + strings.Join(strings.Fields(phrase), "  ") + "\n")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if first != second {
		t.Fatal("one phrase produced two seeds")
	}
}

func TestSeedFromPhraseRejectsRubbish(t *testing.T) {
	for _, phrase := range []string{"", "   ", "not a bip39 phrase at all"} {
		if _, err := keys.SeedFromPhrase(phrase); err == nil {
			t.Fatalf("%q was accepted as a recovery phrase", phrase)
		}
	}
}

// Each key has one purpose. Reusing one across purposes would make a weakness
// in either a weakness in both.
func TestDerivedKeysAreDistinct(t *testing.T) {
	hierarchy, err := keys.Derive(keys.Seed{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	named := map[string]keys.Key{
		"record":   hierarchy.Record,
		"content":  hierarchy.Content,
		"manifest": hierarchy.Manifest,
		"check":    hierarchy.Check,
	}
	for nameA, keyA := range named {
		if keyA == (keys.Key{}) {
			t.Fatalf("the %s key is all zeroes", nameA)
		}
		for nameB, keyB := range named {
			if nameA != nameB && keyA == keyB {
				t.Fatalf("the %s and %s keys are identical", nameA, nameB)
			}
		}
	}
}

func TestDeriveIsDeterministicPerSeed(t *testing.T) {
	first, err := keys.Derive(keys.Seed{9})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	second, err := keys.Derive(keys.Seed{9})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if first != second {
		t.Fatal("one seed produced two hierarchies")
	}
	other, err := keys.Derive(keys.Seed{10})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if first.Record == other.Record {
		t.Fatal("two seeds produced the same record key")
	}
}

// The app key is a secret. It comes from the environment and nowhere else, so
// it cannot be read out of shell history or a process listing.
func TestAppKeyComesFromTheEnvironment(t *testing.T) {
	t.Setenv(keys.AppKeyEnv, "")
	if _, err := keys.AppKeyFromEnv(); !keys.MissingAppKey(err) {
		t.Fatalf("an unset app key reported %v", err)
	}

	// A whole key's worth of bytes, because a shorter one is now refused as
	// damaged rather than read; the leading bytes are what the fingerprint reads.
	t.Setenv(keys.AppKeyEnv, "  deadbeef"+strings.Repeat("01", 60)+"  ")
	key, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	if key.Fingerprint() != "deadbeef" {
		t.Fatalf("fingerprint is %q", key.Fingerprint())
	}

	t.Setenv(keys.AppKeyEnv, "not hex")
	if _, err := keys.AppKeyFromEnv(); err == nil {
		t.Fatal("a non-hex app key was accepted")
	}
}

func TestReadPhrasePrefersTheEnvironment(t *testing.T) {
	t.Setenv(keys.PhraseEnv, " twelve words go here ")
	phrase, err := keys.ReadPhrase(strings.NewReader("something else\n"))
	if err != nil {
		t.Fatalf("read phrase: %v", err)
	}
	if phrase != "twelve words go here" {
		t.Fatalf("phrase is %q", phrase)
	}

	os.Unsetenv(keys.PhraseEnv)
	phrase, err = keys.ReadPhrase(strings.NewReader("from stdin\n"))
	if err != nil {
		t.Fatalf("read phrase: %v", err)
	}
	if phrase != "from stdin" {
		t.Fatalf("phrase is %q", phrase)
	}
}
