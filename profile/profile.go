// Package profile keeps the recovery phrase and the app key between runs.
//
// Without it every command needs both supplied again, and an MCP host, which
// has nobody to ask, can only be handed them in its own configuration, in
// plain text. sennit connect stores them here once, and every later run reads
// them when the environment does not supply them.
//
// There is one profile, named default. The files are laid out by name, so a
// later version can keep more than one beside it without moving this one.
//
// The secrets go to the operating system's credential store: Credential Manager
// on Windows, the login keychain on macOS, the Secret Service on Linux. Where
// there is none, as on a server with no desktop, they go to a file only this
// account can open, and the caller is told so. What is not secret, the indexer
// that issued the key and where the secrets went, is kept in a file beside
// them, so that saying where the secrets are never needs the secrets.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steven3002/sennit/keys"
)

// Name is the one profile this version keeps.
const Name = "default"

// ErrNone reports that no profile is stored.
var ErrNone = errors.New("no profile is stored")

// A Profile is what sennit connect stores.
type Profile struct {
	Phrase string
	AppKey keys.AppKey
	// Indexer is the indexer that issued the key. A key is honoured by that
	// indexer and no other, so the two are only ever used together.
	Indexer string
}

// A Place is where a stored profile's secrets are.
type Place struct {
	// InFile is true when there was no credential store to put them in, and
	// they are in a file this account alone can open.
	InFile bool
	// Name is the credential store's name, or the file's path.
	Name string
}

func (p Place) String() string { return p.Name }

// A Summary is what is known about the stored profile without its secrets.
type Summary struct {
	Indexer string
	Place   Place
	// KeyID identifies the stored app key. See KeyID.
	KeyID     string
	Connected time.Time
}

// A Saved reports where Save put the secrets.
type Saved struct {
	Place Place
	// NoKeychain is why the credential store was not used, when it was not.
	NoKeychain error
}

// KeyID identifies an app key without disclosing any of it.
//
// It is a hash rather than a slice of the key, so it can sit in a file that
// holds nothing secret. Telling two keys apart is all it is for: a variable
// left over from an earlier connect, naming a different key, otherwise wins
// without a word.
func KeyID(key keys.AppKey) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// A Store is where profiles are kept for the account running this.
type Store struct {
	dir      string
	keychain Keychain
	now      func() time.Time
}

// Open returns the store of the account running this, in its configuration
// directory: %AppData%\sennit on Windows, ~/Library/Application Support/sennit
// on macOS, and ~/.config/sennit elsewhere.
func Open() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find this account's configuration directory: %w", err)
	}
	return At(filepath.Join(dir, "sennit"), System()), nil
}

// At returns a store kept in dir, with keychain as its credential store.
func At(dir string, keychain Keychain) *Store {
	return &Store{dir: dir, keychain: keychain, now: time.Now}
}

// Dir is the directory the store keeps its files in.
func (s *Store) Dir() string { return s.dir }

// The secrets are two items in the credential store, named for the profile.
func phraseItem(name string) string { return name + "/phrase" }
func keyItem(name string) string    { return name + "/app-key" }

// Save stores p as the default profile, replacing whatever was stored.
//
// The credential store is tried first. If it cannot take the secrets, for any
// reason, they go to a file this account alone can open, and Saved says why.
// Either way they are read back before Save returns: the key was issued by an
// approval that cannot be repeated cheaply, and a store that took it and
// cannot give it back would otherwise only be found out at the next run.
func (s *Store) Save(p Profile) (Saved, error) {
	if strings.TrimSpace(p.Phrase) == "" {
		return Saved{}, errors.New("save the profile: there is no recovery phrase to save")
	}
	if err := p.AppKey.Check(); err != nil {
		return Saved{}, fmt.Errorf("save the profile: %w", err)
	}
	if p.Indexer == "" {
		return Saved{}, errors.New("save the profile: there is no indexer to save")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Saved{}, fmt.Errorf("save the profile: %w", err)
	}
	unlock, err := s.lock()
	if err != nil {
		return Saved{}, err
	}
	defer unlock()

	saved := Saved{Place: Place{Name: s.keychain.Name()}}
	if err := s.toKeychain(Name, p); err != nil {
		saved = Saved{Place: Place{InFile: true, Name: s.path(secretsFile)}, NoKeychain: err}
		if err := s.toFile(Name, p); err != nil {
			return Saved{}, err
		}
	} else if err := s.dropFromFile(Name); err != nil {
		return Saved{}, err
	}

	where := secretsInKeychain
	if saved.Place.InFile {
		where = secretsInFile
	}
	if err := s.setIndexEntry(Name, entry{
		Indexer:   p.Indexer,
		Secrets:   where,
		KeyID:     KeyID(p.AppKey),
		Connected: s.now().UTC().Truncate(time.Second),
	}); err != nil {
		return Saved{}, err
	}
	return saved, nil
}

// Summary says what is stored, without reading the secrets. It reports
// ErrNone when no profile is stored.
func (s *Store) Summary() (Summary, error) {
	idx, err := s.readIndex()
	if err != nil {
		return Summary{}, err
	}
	e, ok := idx.Profiles[Name]
	if !ok {
		return Summary{}, ErrNone
	}
	place := Place{Name: s.keychain.Name()}
	switch e.Secrets {
	case secretsInKeychain:
	case secretsInFile:
		place = Place{InFile: true, Name: s.path(secretsFile)}
	default:
		return Summary{}, fmt.Errorf("%s names %q as where the secrets are, which this version does not know",
			s.path(indexFile), e.Secrets)
	}
	return Summary{Indexer: e.Indexer, Place: place, KeyID: e.KeyID, Connected: e.Connected}, nil
}

// Load reads the default profile, secrets and all. It reports ErrNone when
// none is stored.
//
// A profile whose secrets have both gone from the credential store, removed
// there by hand, is treated as no profile: that is how someone who never asked
// for a command to remove it would remove it. One secret gone without the
// other is an error, because that state was not left by anyone on purpose.
func (s *Store) Load() (Profile, Summary, error) {
	sum, err := s.Summary()
	if err != nil {
		return Profile{}, Summary{}, err
	}
	var phrase, key string
	if sum.Place.InFile {
		phrase, key, err = s.fromFile(Name)
	} else {
		phrase, key, err = s.fromKeychain(Name)
	}
	if err != nil {
		return Profile{}, Summary{}, err
	}
	appKey, err := decodeKey(key)
	if err != nil {
		return Profile{}, Summary{}, fmt.Errorf("the app key stored in %s: %w", sum.Place, err)
	}
	return Profile{Phrase: phrase, AppKey: appKey, Indexer: sum.Indexer}, sum, nil
}

func (s *Store) fromKeychain(name string) (phrase, key string, err error) {
	phrase, phraseErr := s.keychain.Get(phraseItem(name))
	key, keyErr := s.keychain.Get(keyItem(name))
	switch {
	case errors.Is(phraseErr, ErrNotFound) && errors.Is(keyErr, ErrNotFound):
		return "", "", ErrNone
	case phraseErr != nil:
		return "", "", fmt.Errorf("read the stored recovery phrase from %s: %w", s.keychain.Name(), phraseErr)
	case keyErr != nil:
		return "", "", fmt.Errorf("read the stored app key from %s: %w", s.keychain.Name(), keyErr)
	}
	return phrase, key, nil
}

// toKeychain puts both secrets in the credential store and reads them back.
//
// A store that took one and refused the other is left with neither, so that
// what the index says and what the store holds cannot disagree.
func (s *Store) toKeychain(name string, p Profile) error {
	key := encodeKey(p)
	err := s.keychain.Set(phraseItem(name), p.Phrase)
	if err == nil {
		err = s.keychain.Set(keyItem(name), key)
	}
	if err == nil {
		err = s.verify(p.Phrase, key, func() (string, string, error) { return s.fromKeychain(name) },
			s.keychain.Name())
	}
	if err != nil {
		_ = s.keychain.Delete(phraseItem(name))
		_ = s.keychain.Delete(keyItem(name))
		return err
	}
	return nil
}

// verify reads back what was just stored and confirms it is what was given.
func (s *Store) verify(phrase, key string, read func() (string, string, error), where string) error {
	gotPhrase, gotKey, err := read()
	if err != nil {
		return fmt.Errorf("read back what was stored in %s: %w", where, err)
	}
	if gotPhrase != phrase || gotKey != key {
		return fmt.Errorf("%s gave back something other than what was stored in it", where)
	}
	return nil
}

func encodeKey(p Profile) string { return hex.EncodeToString(p.AppKey) }

func decodeKey(text string) (keys.AppKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("it cannot be decoded: %w", err)
	}
	key := keys.AppKey(raw)
	if err := key.Check(); err != nil {
		return nil, err
	}
	return key, nil
}
