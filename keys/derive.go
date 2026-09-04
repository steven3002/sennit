package keys

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// KeySize is the length of every derived key.
const KeySize = 32

// The derivation labels. Each key has exactly one purpose: reusing one key for
// two purposes is what turns a weakness in either into a weakness in both.
const (
	labelRecord   = "sennit/v1/record-aead"
	labelContent  = "sennit/v1/content-address"
	labelManifest = "sennit/v1/manifest-aead"
	labelCheck    = "sennit/v1/phrase-check"
)

// A Key is a 32-byte symmetric key derived from the vault seed.
type Key [KeySize]byte

// A Hierarchy holds the keys derived from one vault seed.
type Hierarchy struct {
	// Record seals record bodies.
	Record Key
	// Content keys the content address. A plain hash of plaintext would let
	// anyone holding a candidate plaintext confirm the vault stores it.
	Content Key
	// Manifest seals the record-to-location catalog.
	Manifest Key
	// Check is a non-secret-equivalent value written to the vault config so a
	// mistyped phrase is caught before it silently produces an empty vault.
	Check Key
}

// Derive expands the seed into the key hierarchy.
func Derive(seed Seed) (Hierarchy, error) {
	var h Hierarchy
	for _, target := range []struct {
		label string
		key   *Key
	}{
		{labelRecord, &h.Record},
		{labelContent, &h.Content},
		{labelManifest, &h.Manifest},
		{labelCheck, &h.Check},
	} {
		derived, err := hkdf.Key(sha256.New, seed[:], nil, target.label, KeySize)
		if err != nil {
			return Hierarchy{}, fmt.Errorf("derive %s: %w", target.label, err)
		}
		copy(target.key[:], derived)
		clear(derived)
	}
	return h, nil
}

// Wipe clears every key in the hierarchy.
func (h *Hierarchy) Wipe() {
	clear(h.Record[:])
	clear(h.Content[:])
	clear(h.Manifest[:])
	clear(h.Check[:])
}
