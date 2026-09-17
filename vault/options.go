// Package vault is the public API and orchestrates every operation.
package vault

import (
	"os"
	"path/filepath"
	"time"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/packer"
)

// HomeEnv overrides where a vault keeps its files.
const HomeEnv = "SENNIT_HOME"

// IndexerEnv overrides which indexer the vault talks to.
const IndexerEnv = "SENNIT_INDEXER"

// ModelDirEnv overrides where embedding models are kept, so several vaults on
// one machine can share a single copy.
const ModelDirEnv = "SENNIT_MODEL_DIR"

// Options configure a vault.
type Options struct {
	// Home is the directory holding the device's copy of the vault.
	Home string
	// Phrase is the BIP-39 recovery phrase. It is used to derive keys and then
	// discarded; the vault never writes it to disk.
	Phrase string
	// AppKey authorizes the vault against the indexer.
	AppKey keys.AppKey
	// Indexer is the indexer URL.
	Indexer string
	// Model is the embedding model.
	Model embed.Model
	// ModelDir is where model files live.
	ModelDir string
	// Embedder supplies vectors instead of loading a model from ModelDir.
	//
	// The vault takes its model identity from whatever is supplied here, since
	// a vector and the model that produced it are a pair and the vault stamps
	// every vector with the model's name so a later mismatch is detectable.
	//
	// Closing it stays with whoever opened it: a model is expensive enough that
	// it is normally shared across several vaults, and a vault that closed
	// something it did not open would take the others down with it.
	Embedder embed.Vectorizer
	// Flush decides when queued records are written to the network.
	Flush packer.Policy
	// SlabMetaTTL bounds how long cached object locations are trusted. Past it
	// the vault asks the indexer again, because a location that has decayed too
	// far does not fail slowly, it takes the process down.
	SlabMetaTTL time.Duration
	// Offline opens the vault without contacting the indexer, which is enough
	// for anything served from this device.
	Offline bool
	// OnProgress, when set, is called as an operation changes phase and as
	// countable work completes, so a caller can show what a command is waiting
	// on. It may be called from several goroutines at once, because shards are
	// uploaded in parallel, and it must not block.
	//
	// Nil reports nothing and is exactly the behaviour of a vault that was
	// opened before this existed.
	OnProgress func(Progress)
}

// DefaultSlabMetaTTL is how long a cached object location is used before the
// indexer is asked again.
const DefaultSlabMetaTTL = time.Hour

// DefaultHome is where a vault lives unless told otherwise.
func DefaultHome() string {
	if home := os.Getenv(HomeEnv); home != "" {
		return home
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".sennit")
	}
	return ".sennit"
}

// DefaultIndexer is the indexer a vault uses unless told otherwise.
func DefaultIndexer() string {
	if indexer := os.Getenv(IndexerEnv); indexer != "" {
		return indexer
	}
	return sia.DefaultIndexer
}

// DefaultModelDir is where embedding models are kept unless told otherwise.
func DefaultModelDir(home string) string {
	if dir := os.Getenv(ModelDirEnv); dir != "" {
		return dir
	}
	return filepath.Join(home, "models")
}

// dbFile is the device's working copy. The catalog keeps its own files
// alongside it.
const dbFile = "vault.db"

func (o *Options) applyDefaults() {
	if o.Home == "" {
		o.Home = DefaultHome()
	}
	if o.Indexer == "" {
		o.Indexer = DefaultIndexer()
	}
	if o.Model.Name == "" {
		o.Model = embed.BGESmallEN
	}
	if o.ModelDir == "" {
		o.ModelDir = DefaultModelDir(o.Home)
	}
	if o.SlabMetaTTL == 0 {
		o.SlabMetaTTL = DefaultSlabMetaTTL
	}
}

func (o *Options) dbPath() string      { return filepath.Join(o.Home, dbFile) }
func (o *Options) manifestDir() string { return o.Home }
func (o *Options) indexDir() string    { return o.Home }
