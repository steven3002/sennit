// Package embedtest supplies embedders for tests.
//
// It offers two things, and which one a test wants follows from what the test
// is measuring. A test of ranking *quality* needs the real model, because the
// numbers are properties of that model. Everything else, storage, versioning,
// supersession, chunking, listing, links, needs vectors that behave sensibly
// and does not care where they came from; those tests take the stub, which
// costs nothing to load.
//
// The distinction is not cosmetic. The real model occupies several hundred
// megabytes for the life of a test binary, and `go test ./...` runs packages in
// parallel, so a second package loading it is the difference between a suite
// that runs anywhere and one that runs on a large machine or serially. Only
// packages that measure the model should pay for it, and OpenModel makes sure
// no two of them pay at once.
package embedtest

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/steven3002/sennit/embed"
)

// ModelDirEnv points at an already-downloaded model. Nothing here downloads
// one: an ordinary test run touches no network.
const ModelDirEnv = "SENNIT_MODELS"

// ModelDir is where the real model is expected to be.
func ModelDir() string {
	if root := os.Getenv(ModelDirEnv); root != "" {
		return root
	}
	return filepath.Join(os.Getenv("HOME"), ".cache", "sennit", "models")
}

// ModelPresent reports whether the real model is on this machine.
func ModelPresent() bool { return embed.BGESmallEN.Present(ModelDir()) }

// ErrNoModel reports that the real model is not on this machine, which is a
// skip and never a failure: a contributor who has not downloaded it still gets
// a green run of everything that does not measure the model.
var ErrNoModel = errors.New("embedding model is not present")

// OpenModel loads the real model, waiting until no other test process holds it.
//
// The lock is what makes a parallel `go test ./...` survive on a small machine.
// Two test binaries each holding the model need more memory than the machine
// this was written on has free, and the failure mode is the kernel killing one
// of them, which reads as a flaky test rather than as what it is. Serialising
// only the packages that genuinely need the model leaves every other package
// running in parallel, which is where the wall-clock time actually is.
//
// The returned release function must be called when the model is finished with.
// It is also released by the process exiting, so a test binary that dies
// holding it does not wedge the next one.
func OpenModel(ctx context.Context) (*embed.Embedder, func(), error) {
	root := ModelDir()
	if !embed.BGESmallEN.Present(root) {
		return nil, nil, fmt.Errorf("%w under %s; set %s to run the tests that measure it",
			ErrNoModel, root, ModelDirEnv)
	}
	release, err := acquireModelSlot()
	if err != nil {
		return nil, nil, err
	}
	embedder, err := embed.Open(ctx, embed.BGESmallEN, embed.BGESmallEN.Dir(root))
	if err != nil {
		release()
		return nil, nil, err
	}
	return embedder, func() {
		embedder.Close()
		release()
	}, nil
}

// modelSlot is the lock file the real model is held under.
//
// It lives in the temporary directory rather than beside the model, because the
// scope that matters is one test run: `go test ./...` gives every package's
// binary the same temporary directory, and two unrelated runs on one machine
// have no reason to wait for each other.
const modelSlot = "sennit-model-slot.lock"

var slotOnce sync.Mutex

func acquireModelSlot() (func(), error) {
	// The in-process mutex handles two tests in one binary; the file lock
	// handles two binaries. Both are needed: `go test` runs packages as
	// separate processes and tests within a package in one.
	slotOnce.Lock()

	path := filepath.Join(os.TempDir(), modelSlot)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		slotOnce.Unlock()
		return nil, fmt.Errorf("open the model lock %s: %w", path, err)
	}
	// Blocking, because the caller wants the model rather than a report that
	// somebody else has it. Waiting costs wall-clock time in one package; not
	// waiting costs the machine.
	if err := lockFile(file); err != nil {
		file.Close()
		slotOnce.Unlock()
		return nil, fmt.Errorf("wait for the model lock %s: %w", path, err)
	}
	return func() {
		unlockFile(file)
		file.Close()
		slotOnce.Unlock()
	}, nil
}

// StubModel is what the stub embedder reports.
//
// The name is deliberately not a real model's. It is stored beside every vector
// the stub produces, so a vault built with the stub and then opened with the
// real model reports its vectors as foreign and refuses to rank them, which is
// the behaviour that exists to catch a half-re-embedded index, working exactly
// as it should.
var StubModel = embed.Model{Name: "stub-hashed-bag-of-words", Dim: 128}

// A Stub is a deterministic embedder with no model behind it.
//
// It is a hashed bag of words: each term is hashed to a dimension, the counts
// are L2-normalised, and cosine similarity is therefore term overlap. That is
// not semantic similarity and is not meant to be, it is enough for a test to
// assert that a record is found by its own words, that an identical statement
// is recognised as a restatement, and that a vault's vectors survive a restart.
// Anything measuring how well *meaning* is retrieved must use the real model.
type Stub struct{ model embed.Model }

// NewStub returns a stub embedder.
func NewStub() *Stub { return &Stub{model: StubModel} }

// Model reports the stub's identity.
func (s *Stub) Model() embed.Model { return s.model }

// Dim reports the vector width.
func (s *Stub) Dim() int { return s.model.Dim }

// Close releases nothing.
func (s *Stub) Close() error { return nil }

// Embed vectorises a batch.
func (s *Stub) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = s.vector(text)
	}
	return out, nil
}

// EmbedOne vectorises a single text.
func (s *Stub) EmbedOne(_ context.Context, text string) ([]float32, error) {
	return s.vector(text), nil
}

func (s *Stub) vector(text string) []float32 {
	vector := make([]float32, s.model.Dim)
	for _, term := range terms(text) {
		digest := fnv.New32a()
		digest.Write([]byte(term))
		vector[digest.Sum32()%uint32(s.model.Dim)]++
	}

	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		// A text with no terms still needs a unit vector: the index rejects
		// anything else, and an empty query is a legitimate thing to embed.
		vector[0] = 1
		return vector
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / norm)
	}
	return vector
}

func terms(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) < 2 {
			continue
		}
		out = append(out, field)
	}
	return out
}

var _ embed.Vectorizer = (*Stub)(nil)
