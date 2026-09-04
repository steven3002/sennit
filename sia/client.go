// Package sia is the only package that speaks to the Sia SDK and indexer.
//
// The boundary is deliberate: the SDK has surprised this project three times,
// a prune cutoff it gives no way to override, a reclamation call it does not
// expose, and a download path that panics in a goroutine the caller cannot
// recover. Confining it to one package means the next surprise is absorbed in
// one place instead of spreading through the substrate.
package sia

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/indexd/api/app"
	"go.sia.tech/siastorage"

	"github.com/steven3002/sennit/keys"
)

// DefaultIndexer is the hosted indexer. The URL is a parameter throughout: the
// SDK works against any indexer, including a self-hosted one.
const DefaultIndexer = "https://sia.storage"

// ErrUnauthorized reports that the app key is not authorized by the indexer,
// which means an approval round is needed rather than a retry.
var ErrUnauthorized = errors.New("app key is not authorized by the indexer")

// A Client holds an authorized connection to an indexer.
type Client struct {
	sdk     *siastorage.SDK
	app     *app.Client
	appKey  types.PrivateKey
	indexer string

	// The account's host set, held briefly so that cached object locations can
	// be checked against it without a round trip each time.
	hostsMu sync.Mutex
	hosts   map[types.PublicKey]struct{}
	hostsAt time.Time
}

// Config parameterises a connection.
type Config struct {
	// Indexer is the indexer URL; empty means DefaultIndexer.
	Indexer string
	// AppKey authorizes this installation. It comes from the environment.
	AppKey keys.AppKey
}

// Connect opens a headless connection with an already-issued app key.
//
// The indexer authorizes on the key alone, so the application metadata here is
// descriptive only; it is the interactive approval that binds a key to an
// application, and that has already happened by the time this runs.
func Connect(cfg Config) (*Client, error) {
	indexer := cfg.Indexer
	if indexer == "" {
		indexer = DefaultIndexer
	}
	// Checked here as well as where the key is read, because the conversion
	// below is unchecked by construction: types.PrivateKey is a byte slice, and
	// a wrong-sized one panics when it signs rather than returning an error.
	if err := cfg.AppKey.Check(); err != nil {
		return nil, err
	}
	appKey := types.PrivateKey(cfg.AppKey)

	builder := siastorage.NewBuilder(indexer, metadata())
	sdk, err := builder.SDK(appKey)
	if err != nil {
		if errors.Is(err, siastorage.ErrUnauthorized) {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, indexer)
		}
		return nil, asIndexerError("connect to the indexer", indexer, err)
	}
	return &Client{
		sdk:     sdk,
		app:     app.NewClient(indexer),
		appKey:  appKey,
		indexer: indexer,
	}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.sdk.Close() }

// Indexer reports which indexer this client is bound to.
func (c *Client) Indexer() string { return c.indexer }

// An ObjectRef locates a stored blob on Sia.
//
// It is a pointer, not an identity. Repack rewrites every object id, so nothing
// above this package may treat a ref as a stable key for a record.
type ObjectRef struct {
	ID types.Hash256
}

func (r ObjectRef) String() string { return hex.EncodeToString(r.ID[:]) }

// IsZero reports whether the ref is unset.
func (r ObjectRef) IsZero() bool { return r.ID == types.Hash256{} }

// ParseObjectRef reads the hex form produced by String.
func ParseObjectRef(s string) (ObjectRef, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return ObjectRef{}, fmt.Errorf("parse object ref %q: %w", s, err)
	}
	if len(b) != len(types.Hash256{}) {
		return ObjectRef{}, fmt.Errorf("parse object ref %q: got %d bytes, want 32", s, len(b))
	}
	var ref ObjectRef
	copy(ref.ID[:], b)
	return ref, nil
}

// A SlabID identifies a slab that this vault has pinned. Tracking them is the
// only way to release storage later: the indexer bills a whole slab whether it
// holds one live record or none, and nothing else in the system knows which
// slabs belong to this vault.
type SlabID string

func metadata() siastorage.AppMetadata {
	return siastorage.AppMetadata{
		ID:          appID,
		Name:        "Sennit",
		Description: "User-owned encrypted storage for an AI's memory, sessions and skills",
		LogoURL:     "https://sia.tech/favicon.ico",
		ServiceURL:  "https://github.com/steven3002/sennit",
	}
}
