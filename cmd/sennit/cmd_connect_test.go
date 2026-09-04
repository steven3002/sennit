package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
)

// The approval that issues an app key needs a person and a browser and expires
// in about ten minutes, so a key file that exists but does not carry the key is
// the expensive failure: it surfaces much later as an authorization error with
// nothing to connect it back to this step.
// Not parallel: it sets the environment variable a later run would load from,
// which is the half of the round trip that matters.
func TestTheAppKeyWrittenIsTheAppKeyReadBack(t *testing.T) {
	// A whole key's worth of bytes: a shorter one no longer loads, because a
	// key that is not an ed25519 private key panics when it signs.
	key := make(keys.AppKey, 64)
	copy(key, []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04})
	path := filepath.Join(t.TempDir(), "sennit.key")

	if err := writeAppKey(path, key); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the key file is mode %04o, want 0600: it is a secret", perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("the file does not decode: %v", err)
	}
	if string(got) != string(key) {
		t.Fatalf("the file carries a different key than was issued")
	}
	// The environment is the only way a later run loads it, so the round trip
	// that matters is file to environment to key.
	t.Setenv(keys.AppKeyEnv, strings.TrimSpace(string(raw)))
	loaded, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("the written key does not load from the environment: %v", err)
	}
	if string(loaded) != string(key) {
		t.Fatal("the key that loads is not the key that was written")
	}
}

// A file that does not carry the issued key is a failure of connect, not
// something to discover against the indexer later.
func TestAKeyFileThatDoesNotCarryTheKeyIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	issued := keys.AppKey{0xaa, 0xbb, 0xcc, 0xdd}

	cases := map[string]string{
		"truncated":  hex.EncodeToString(issued)[:4],
		"empty":      "",
		"not hex":    "this is not a key",
		"a differen": hex.EncodeToString(keys.AppKey{0x11, 0x22, 0x33, 0x44}),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := verifyAppKey(path, issued); err == nil {
				t.Fatal("a key file that does not carry the issued key was accepted")
			}
		})
	}
}
