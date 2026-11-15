package profile_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/internal/private/privatetest"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/profile"
	"github.com/steven3002/sennit/profile/profiletest"
)

const testPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

func newKey(t *testing.T) keys.AppKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate an app key: %v", err)
	}
	return keys.AppKey(private)
}

func aProfile(t *testing.T) profile.Profile {
	return profile.Profile{Phrase: testPhrase, AppKey: newKey(t), Indexer: "https://sia.storage"}
}

func sameProfile(t *testing.T, got, want profile.Profile) {
	t.Helper()
	if got.Phrase != want.Phrase {
		t.Errorf("the phrase read back is not the one stored")
	}
	if hex.EncodeToString(got.AppKey) != hex.EncodeToString(want.AppKey) {
		t.Errorf("the app key read back is %s, stored %s", got.AppKey.Fingerprint(), want.AppKey.Fingerprint())
	}
	if got.Indexer != want.Indexer {
		t.Errorf("the indexer read back is %q, stored %q", got.Indexer, want.Indexer)
	}
}

// fileHolds reports whether the file at path contains text.
func fileHolds(t *testing.T, path, text string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Contains(string(raw), text)
}

// With a credential store, the secrets go there and nowhere else. The file
// beside it says which indexer and where, and holds neither secret.
func TestAProfileIsKeptInTheCredentialStore(t *testing.T) {
	dir := t.TempDir()
	keychain := profiletest.NewKeychain()
	store := profile.At(dir, keychain)
	p := aProfile(t)

	saved, err := store.Save(p)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Place.InFile || saved.NoKeychain != nil {
		t.Errorf("saved to %s (%v), want the credential store", saved.Place, saved.NoKeychain)
	}
	if saved.Place.Name != keychain.Name() {
		t.Errorf("saved to %q, want %q", saved.Place, keychain.Name())
	}

	got, summary, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sameProfile(t, got, p)
	if summary.KeyID != profile.KeyID(p.AppKey) {
		t.Errorf("the summary names key %s, stored %s", summary.KeyID, profile.KeyID(p.AppKey))
	}

	index := filepath.Join(dir, "profiles.json")
	if fileHolds(t, index, "abandon") || fileHolds(t, index, hex.EncodeToString(p.AppKey)) {
		t.Errorf("profiles.json holds a secret, and it is the file that is never secret")
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a secrets file exists beside a working credential store (%v)", err)
	}
}

// Where there is no credential store the secrets go to a file this account
// alone can open, and the reason the store was not used comes back with it.
func TestWithNoCredentialStoreTheProfileGoesToAFileOnlyThisAccountCanOpen(t *testing.T) {
	dir := t.TempDir()
	keychain := profiletest.NewKeychain()
	keychain.Refuse = errors.New("The name org.freedesktop.secrets was not provided by any .service files")
	store := profile.At(dir, keychain)
	p := aProfile(t)

	saved, err := store.Save(p)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	secretsFile := filepath.Join(dir, "secrets.json")
	if !saved.Place.InFile || saved.Place.Name != secretsFile {
		t.Errorf("saved to %s, want the file %s", saved.Place, secretsFile)
	}
	if saved.NoKeychain == nil || !strings.Contains(saved.NoKeychain.Error(), "org.freedesktop.secrets") {
		t.Errorf("the reason the credential store was not used is %v, want the store's own answer", saved.NoKeychain)
	}
	privatetest.OnlyThisAccountMayOpen(t, secretsFile)
	privatetest.OnlyThisAccountMayOpen(t, filepath.Join(dir, "profiles.json"))

	got, summary, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sameProfile(t, got, p)
	if !summary.Place.InFile {
		t.Errorf("the summary says the secrets are in %s, want the file", summary.Place)
	}
}

// Once the credential store takes the secrets, no copy is left in a file.
func TestMovingIntoTheCredentialStoreLeavesNoCopyInAFile(t *testing.T) {
	dir := t.TempDir()
	keychain := profiletest.NewKeychain()
	keychain.Refuse = errors.New("no credential store")
	store := profile.At(dir, keychain)
	if _, err := store.Save(aProfile(t)); err != nil {
		t.Fatalf("save to the file: %v", err)
	}

	keychain.Refuse = nil
	p := aProfile(t)
	saved, err := store.Save(p)
	if err != nil {
		t.Fatalf("save to the credential store: %v", err)
	}
	if saved.Place.InFile {
		t.Fatalf("saved to %s, want the credential store", saved.Place)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the secrets file is still there once the credential store holds them (%v)", err)
	}
	got, _, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sameProfile(t, got, p)
}

// A store that took the phrase and refused the key is left holding neither,
// and the profile goes to the file, so the two places never disagree.
func TestAStoreThatTookOneSecretAndRefusedTheOtherIsLeftWithNeither(t *testing.T) {
	dir := t.TempDir()
	keychain := profiletest.NewKeychain()
	keychain.RefuseItem = "default/app-key"
	store := profile.At(dir, keychain)
	p := aProfile(t)

	saved, err := store.Save(p)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.Place.InFile {
		t.Errorf("saved to %s, want the file", saved.Place)
	}
	if keychain.Holds("default/phrase") {
		t.Errorf("the phrase was left in the credential store after the key was refused")
	}
	got, _, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sameProfile(t, got, p)
}

func TestNothingStoredIsNoProfile(t *testing.T) {
	store := profile.At(t.TempDir(), profiletest.NewKeychain())
	if _, err := store.Summary(); !errors.Is(err, profile.ErrNone) {
		t.Errorf("summary of an empty store: %v, want ErrNone", err)
	}
	if _, _, err := store.Load(); !errors.Is(err, profile.ErrNone) {
		t.Errorf("load from an empty store: %v, want ErrNone", err)
	}
}

// Secrets removed from the credential store by hand, both of them, mean there
// is no profile. One removed without the other is an error, since nobody
// leaves that state on purpose.
func TestSecretsRemovedByHandFromTheCredentialStore(t *testing.T) {
	keychain := profiletest.NewKeychain()
	store := profile.At(t.TempDir(), keychain)
	if _, err := store.Save(aProfile(t)); err != nil {
		t.Fatalf("save: %v", err)
	}

	keychain.Remove("default/app-key")
	if _, _, err := store.Load(); err == nil || errors.Is(err, profile.ErrNone) {
		t.Errorf("load with the key removed and the phrase still there: %v, want an error", err)
	}
	keychain.Remove("default/phrase")
	if _, _, err := store.Load(); !errors.Is(err, profile.ErrNone) {
		t.Errorf("load with both removed: %v, want ErrNone", err)
	}
}

// A file a later version wrote is refused, and not rewritten, since this
// version would drop whatever it does not know.
func TestAFileFromALaterVersionIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "profiles.json")
	later := `{"version": 2, "profiles": {}, "added": "by a later version"}` + "\n"
	if err := os.WriteFile(index, []byte(later), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := profile.At(dir, profiletest.NewKeychain())

	if _, err := store.Summary(); err == nil || !strings.Contains(err.Error(), "later version") {
		t.Errorf("summary of a later file: %v, want it refused as a later version's", err)
	}
	if _, err := store.Save(aProfile(t)); err == nil {
		t.Errorf("a save over a later version's file succeeded")
	}
	raw, err := os.ReadFile(index)
	if err != nil || string(raw) != later {
		t.Errorf("the later version's file was changed: %q (%v)", raw, err)
	}
}

func TestSaveRefusesWhatCannotBeUsed(t *testing.T) {
	store := profile.At(t.TempDir(), profiletest.NewKeychain())
	for name, p := range map[string]profile.Profile{
		"no phrase":  {Phrase: "  ", AppKey: newKey(t), Indexer: "https://sia.storage"},
		"a bad key":  {Phrase: testPhrase, AppKey: keys.AppKey{1, 2, 3}, Indexer: "https://sia.storage"},
		"no indexer": {Phrase: testPhrase, AppKey: newKey(t)},
	} {
		if _, err := store.Save(p); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if _, err := store.Summary(); !errors.Is(err, profile.ErrNone) {
		t.Errorf("a refused save left a profile behind: %v", err)
	}
}

// KeyID tells keys apart and shows nothing of either.
func TestAKeyIDDisclosesNothingOfTheKey(t *testing.T) {
	a, b := newKey(t), newKey(t)
	if profile.KeyID(a) != profile.KeyID(a) {
		t.Errorf("the same key gave two ids")
	}
	if profile.KeyID(a) == profile.KeyID(b) {
		t.Errorf("two keys gave the same id")
	}
	// A fingerprint that is a slice of the key would appear in it whole.
	if id, key := profile.KeyID(a), hex.EncodeToString(a); strings.Contains(key, id) {
		t.Errorf("the id %s is a piece of the key itself", id)
	}
}
