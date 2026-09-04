// Package seal converts a record to an encrypted, framed, content-addressed
// blob and back.
package seal

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/steven3002/sennit/keys"
)

// Sizes of the authenticated-encryption envelope. The nonce is 24 bytes
// because XChaCha20's extended nonce is what makes a fresh random nonce per
// message safe at vault scale, and randomness per message is what keeps two
// copies of the same plaintext unlinkable on the wire.
const (
	NonceSize = chacha20poly1305.NonceSizeX
	TagSize   = 16
	// Overhead is the per-record cost of the envelope, which the packer must
	// budget for in ciphertext bytes rather than plaintext bytes.
	Overhead = NonceSize + TagSize
)

// ErrCorrupt reports that a blob failed authentication or could not be parsed.
// The SDK's own payload cipher is an unauthenticated stream, so this tag is the
// only integrity check a record has.
var ErrCorrupt = errors.New("sealed record is corrupt")

// A Sealer applies the vault's own authenticated encryption and content
// addressing. It is safe for concurrent use.
type Sealer struct {
	record  keys.Key
	content keys.Key
	encoder *zstd.Encoder
	decoder *zstd.Decoder
}

// New builds a sealer from the record and content-address keys.
func New(record, content keys.Key) (*Sealer, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("zstd encoder: %w", err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		encoder.Close()
		return nil, fmt.Errorf("zstd decoder: %w", err)
	}
	return &Sealer{record: record, content: content, encoder: encoder, decoder: decoder}, nil
}

// Close releases the compressor's resources.
func (s *Sealer) Close() {
	s.encoder.Close()
	s.decoder.Close()
}

// Seal compresses then encrypts a record body.
//
// Compression runs first because it is the only order that gains anything:
// ciphertext is incompressible by construction.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	compressed := s.encoder.EncodeAll(plaintext, nil)

	aead, err := chacha20poly1305.NewX(s.record[:])
	if err != nil {
		return nil, fmt.Errorf("record aead: %w", err)
	}
	nonce := make([]byte, NonceSize, NonceSize+len(compressed)+TagSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, compressed, nil), nil
}

// Open reverses Seal.
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < Overhead {
		return nil, fmt.Errorf("%w: %d bytes, minimum %d", ErrCorrupt, len(sealed), Overhead)
	}
	aead, err := chacha20poly1305.NewX(s.record[:])
	if err != nil {
		return nil, fmt.Errorf("record aead: %w", err)
	}
	nonce, ciphertext := sealed[:NonceSize], sealed[NonceSize:]
	compressed, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrCorrupt)
	}
	plaintext, err := s.decoder.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: decompress: %w", ErrCorrupt, err)
	}
	return plaintext, nil
}
