package profile

import (
	"errors"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// A credential store that does not answer is given up on, rather than left to
// hang the command or the server waiting on it.
func TestACredentialStoreThatDoesNotAnswerIsGivenUpOn(t *testing.T) {
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })

	start := time.Now()
	_, err := bounded(50*time.Millisecond, func() (string, error) {
		<-never
		return "", nil
	})
	if !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("a store that never answers: %v, want ErrNoAnswer", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("gave up after %s, for a limit of 50ms", waited)
	}
}

// The library's own not-found becomes this package's, so callers need not
// know which library reaches the store.
func TestAnItemTheStoreDoesNotHoldIsNotFound(t *testing.T) {
	_, err := bounded(time.Second, func() (string, error) { return "", keyring.ErrNotFound })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	value, err := bounded(time.Second, func() (string, error) { return "kept", nil })
	if err != nil || value != "kept" {
		t.Errorf("an answer was lost: %q, %v", value, err)
	}
}

func TestEachPlatformsStoreIsNamedAsItsUsersKnowIt(t *testing.T) {
	for goos, want := range map[string]string{
		"windows": "Windows Credential Manager",
		"darwin":  "the macOS keychain",
		"linux":   "the Secret Service keyring",
		"freebsd": "the Secret Service keyring",
	} {
		if got := systemName(goos); got != want {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
	}
}
