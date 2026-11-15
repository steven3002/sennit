package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/internal/private"
	"github.com/steven3002/sennit/internal/rename"
)

// The files a store keeps, all in its directory.
const (
	// indexFile says which profiles there are, which indexer each one's key is
	// for, and where its secrets are. It holds nothing secret.
	indexFile = "profiles.json"
	// secretsFile holds the secrets of a profile saved where there was no
	// credential store, and exists only then.
	secretsFile = "secrets.json"
	// lockFile is taken around every change, because a terminal and a running
	// MCP server can both reach the store.
	lockFile = "profiles.lock"
)

// formatVersion is the version of both files. A file of a later version was
// written by a later Sennit, which may have put something in it this one
// would drop on its next write, so it is refused rather than rewritten.
const formatVersion = 1

const (
	secretsInKeychain = "keychain"
	secretsInFile     = "file"
)

type index struct {
	Version  int              `json:"version"`
	Profiles map[string]entry `json:"profiles"`
}

type entry struct {
	Indexer string `json:"indexer"`
	// Secrets is where the phrase and the key are, secretsInKeychain or
	// secretsInFile.
	Secrets   string    `json:"secrets"`
	KeyID     string    `json:"keyId"`
	Connected time.Time `json:"connected"`
}

type secrets struct {
	Version  int                    `json:"version"`
	Profiles map[string]secretEntry `json:"profiles"`
}

type secretEntry struct {
	Phrase string `json:"phrase"`
	AppKey string `json:"appKey"`
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// lock takes the store's lock. Reads do not take it: every file is replaced
// whole by a rename, so a reader finds the old file or the new one, never a
// file half written.
func (s *Store) lock() (func(), error) {
	mu, err := filelock.Open(s.path(lockFile))
	if err != nil {
		return nil, fmt.Errorf("save the profile: %w", err)
	}
	if err := mu.Lock(); err != nil {
		mu.Close()
		return nil, fmt.Errorf("save the profile: %w", err)
	}
	return func() {
		_ = mu.Unlock()
		_ = mu.Close()
	}, nil
}

func (s *Store) readIndex() (index, error) {
	var idx index
	found, err := s.readJSON(indexFile, &idx)
	if err != nil {
		return index{}, err
	}
	if !found {
		return index{}, ErrNone
	}
	if err := checkVersion(s.path(indexFile), idx.Version); err != nil {
		return index{}, err
	}
	return idx, nil
}

func (s *Store) setIndexEntry(name string, e entry) error {
	idx, err := s.readIndex()
	switch {
	case errors.Is(err, ErrNone):
		idx = index{Version: formatVersion}
	case err != nil:
		return err
	}
	if idx.Profiles == nil {
		idx.Profiles = map[string]entry{}
	}
	idx.Profiles[name] = e
	return s.writeJSON(indexFile, idx)
}

func (s *Store) readSecrets() (secrets, bool, error) {
	var sec secrets
	found, err := s.readJSON(secretsFile, &sec)
	if err != nil || !found {
		return secrets{}, found, err
	}
	if err := checkVersion(s.path(secretsFile), sec.Version); err != nil {
		return secrets{}, true, err
	}
	return sec, true, nil
}

func (s *Store) fromFile(name string) (phrase, key string, err error) {
	sec, found, err := s.readSecrets()
	if err != nil {
		return "", "", err
	}
	e, ok := sec.Profiles[name]
	if !found || !ok {
		return "", "", fmt.Errorf("%s says the secrets are in %s, and they are not there: "+
			"run sennit connect again to store them", s.path(indexFile), s.path(secretsFile))
	}
	return e.Phrase, e.AppKey, nil
}

// toFile writes both secrets to the secrets file, which this account alone can
// open, and reads them back.
func (s *Store) toFile(name string, p Profile) error {
	sec, _, err := s.readSecrets()
	if err != nil {
		return err
	}
	if sec.Profiles == nil {
		sec = secrets{Version: formatVersion, Profiles: map[string]secretEntry{}}
	}
	key := encodeKey(p)
	sec.Profiles[name] = secretEntry{Phrase: p.Phrase, AppKey: key}
	if err := s.writeJSON(secretsFile, sec); err != nil {
		return err
	}
	return s.verify(p.Phrase, key, func() (string, string, error) { return s.fromFile(name) }, s.path(secretsFile))
}

// dropFromFile removes a profile's secrets from the secrets file, once they
// are in the credential store, so that no copy is left behind in a file. The
// file goes when the last profile in it does.
func (s *Store) dropFromFile(name string) error {
	sec, found, err := s.readSecrets()
	if err != nil || !found {
		return err
	}
	if _, ok := sec.Profiles[name]; !ok {
		return nil
	}
	delete(sec.Profiles, name)
	if len(sec.Profiles) == 0 {
		if err := os.Remove(s.path(secretsFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the secrets file the credential store replaces: %w", err)
		}
		return nil
	}
	return s.writeJSON(secretsFile, sec)
}

func (s *Store) readJSON(name string, into any) (bool, error) {
	raw, err := os.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", s.path(name), err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return true, fmt.Errorf("%s cannot be read: %w", s.path(name), err)
	}
	return true, nil
}

// writeJSON replaces a file whole: a temporary file in the same directory is
// limited to this account, written, synced and closed, then renamed over the
// old one. Each file is made private whether or not it holds a secret, since
// the rule is simpler to keep than the exception.
func (s *Store) writeJSON(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	tmp, err := os.CreateTemp(s.dir, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := private.Restrict(tmp); err != nil {
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	if err := rename.Over(tmp.Name(), s.path(name)); err != nil {
		os.Remove(tmp.Name())
		done = true
		return fmt.Errorf("write %s: %w", s.path(name), err)
	}
	done = true
	return nil
}

func checkVersion(path string, version int) error {
	switch {
	case version == formatVersion:
		return nil
	case version > formatVersion:
		return fmt.Errorf("%s was written by a later version of Sennit (format %d, where this one reads %d), "+
			"so this one leaves it alone: use the later version", path, version, formatVersion)
	default:
		return fmt.Errorf("%s has no format this version reads (format %d)", path, version)
	}
}
