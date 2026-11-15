// Package profiletest stands in for the operating system's credential store in
// tests, so that no test reads or writes the real one of whoever runs it.
package profiletest

import (
	"sync"

	"github.com/steven3002/sennit/profile"
)

// A Keychain is a credential store held in memory.
type Keychain struct {
	mu    sync.Mutex
	items map[string]string
	// Refuse, when set, is what every call answers, the way a machine with no
	// credential store answers.
	Refuse error
	// RefuseItem, when set, is refused on its own, to stand for a store that
	// takes one secret and fails on the next.
	RefuseItem string
}

// NewKeychain returns an empty credential store.
func NewKeychain() *Keychain { return &Keychain{items: map[string]string{}} }

// Name is what the store is called in what a command prints.
func (k *Keychain) Name() string { return "the test keychain" }

func (k *Keychain) Set(item, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.refused(item); err != nil {
		return err
	}
	k.items[item] = secret
	return nil
}

func (k *Keychain) Get(item string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.refused(item); err != nil {
		return "", err
	}
	secret, ok := k.items[item]
	if !ok {
		return "", profile.ErrNotFound
	}
	return secret, nil
}

func (k *Keychain) Delete(item string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.Refuse != nil {
		return k.Refuse
	}
	if _, ok := k.items[item]; !ok {
		return profile.ErrNotFound
	}
	delete(k.items, item)
	return nil
}

// Holds reports whether the store holds an item, whatever it refuses.
func (k *Keychain) Holds(item string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.items[item]
	return ok
}

// Remove takes an item out, the way a person would in the system's own tool.
func (k *Keychain) Remove(item string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.items, item)
}

func (k *Keychain) refused(item string) error {
	if k.Refuse != nil {
		return k.Refuse
	}
	if k.RefuseItem != "" && item == k.RefuseItem {
		return errRefusedItem
	}
	return nil
}

type refusedItem struct{}

func (refusedItem) Error() string { return "the test keychain refused this item" }

var errRefusedItem error = refusedItem{}
