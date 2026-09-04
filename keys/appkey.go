package keys

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// AppKeyEnv is the only accepted source of the Sia app key.
//
// It is never a flag or a tool argument: flags land in shell history and in the
// process table, and a tool argument would let a calling model read or forge it.
const AppKeyEnv = "SENNIT_APP_KEY"

// ErrNoAppKey reports that the environment carries no app key.
var ErrNoAppKey = fmt.Errorf("%s is not set", AppKeyEnv)

// ErrAppKeyLength reports a value that is not an ed25519 private key.
//
// It is a separate failure from an absent key because it is a different
// mistake: the key was issued and then truncated in transit, by a copy that
// missed characters or a secret store that clipped it.
var ErrAppKeyLength = errors.New("app key is the wrong length")

// An AppKey authorizes this installation against a Sia indexer. It is issued
// once by an interactive approval and is not derivable from the recovery
// phrase alone.
type AppKey []byte

// AppKeyFromEnv reads and decodes the app key from the environment.
func AppKeyFromEnv() (AppKey, error) {
	raw := strings.TrimSpace(os.Getenv(AppKeyEnv))
	if raw == "" {
		return nil, ErrNoAppKey
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", AppKeyEnv, err)
	}
	if err := AppKey(key).Check(); err != nil {
		return nil, err
	}
	return key, nil
}

// Check reports whether the key is well formed.
//
// The length is not a formality. An AppKey becomes an ed25519 private key at
// the point it signs an indexer request, and that type is a plain byte slice
// whose size the compiler cannot enforce: a key of any other length panics
// inside the signing code rather than failing. This is the boundary the bytes
// arrive at, so it is the boundary that checks them.
func (k AppKey) Check() error {
	if len(k) == 0 {
		return ErrNoAppKey
	}
	if len(k) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: got %d, expected %d bytes", ErrAppKeyLength, len(k), ed25519.PrivateKeySize)
	}
	return nil
}

// Fingerprint identifies an app key in logs and error messages without
// disclosing it.
func (k AppKey) Fingerprint() string {
	if len(k) < 4 {
		return "invalid"
	}
	return hex.EncodeToString(k[:4])
}

// MissingAppKey reports whether err is the absent-app-key case, which callers
// answer with onboarding instructions rather than a stack trace.
func MissingAppKey(err error) bool { return errors.Is(err, ErrNoAppKey) }

// WrongAppKeyLength reports whether err is a key that arrived damaged, which
// asks the user for something different from the key that never arrived.
func WrongAppKeyLength(err error) bool { return errors.Is(err, ErrAppKeyLength) }
