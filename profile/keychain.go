package profile

import (
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/zalando/go-keyring"
)

// A Keychain is the operating system's credential store, as a profile uses it:
// named items, each holding one secret.
type Keychain interface {
	// Name is what a person calls the store, as a status line names it.
	Name() string
	Set(item, secret string) error
	// Get reports ErrNotFound for an item the store does not hold.
	Get(item string) (string, error)
	Delete(item string) error
}

// ErrNotFound reports an item the credential store does not hold.
var ErrNotFound = errors.New("not in the credential store")

// ErrNoAnswer reports a credential store that did not answer in time.
var ErrNoAnswer = errors.New("the credential store did not answer")

// service is the name every item is filed under in the credential store.
const service = "sennit"

// keychainTimeout bounds every call to the credential store.
//
// The library that reaches it has no way to give up on a call. On Linux a
// Secret Service whose keyring is locked answers a read by asking somebody to
// unlock it and waiting, and on a machine with nobody there to ask that wait
// has no end, which would hang the command, or the MCP server, for good. Half a
// minute is long enough to type a password into an unlock prompt that does
// appear. The call that runs past it is abandoned, not stopped, and is
// collected when the process exits.
const keychainTimeout = 30 * time.Second

// System is the operating system's credential store.
func System() Keychain { return systemKeychain{timeout: keychainTimeout} }

type systemKeychain struct{ timeout time.Duration }

func (k systemKeychain) Name() string { return systemName(runtime.GOOS) }

func (k systemKeychain) Set(item, secret string) error {
	_, err := bounded(k.timeout, func() (string, error) { return "", keyring.Set(service, item, secret) })
	return err
}

func (k systemKeychain) Get(item string) (string, error) {
	return bounded(k.timeout, func() (string, error) { return keyring.Get(service, item) })
}

func (k systemKeychain) Delete(item string) error {
	_, err := bounded(k.timeout, func() (string, error) { return "", keyring.Delete(service, item) })
	return err
}

// systemName is what a person on each platform calls the store the library
// reaches there.
func systemName(goos string) string {
	switch goos {
	case "windows":
		return "Windows Credential Manager"
	case "darwin":
		return "the macOS keychain"
	default:
		return "the Secret Service keyring"
	}
}

// bounded runs one call to the credential store and gives up on it after
// limit. It is the same shape the GitHub CLI wraps the same library in, for
// the same reason.
func bounded(limit time.Duration, call func() (string, error)) (string, error) {
	type answer struct {
		value string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		value, err := call()
		done <- answer{value, err}
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case got := <-done:
		if errors.Is(got.err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return got.value, got.err
	case <-timer.C:
		return "", fmt.Errorf("%w within %s, which on Linux usually means its keyring is locked", ErrNoAnswer, limit)
	}
}
