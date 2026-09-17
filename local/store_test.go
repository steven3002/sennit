package local_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

func TestBodyRoundTrip(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	id, _ := record.NewID()
	body := []byte(`{"statement":"a fact worth keeping"}`)
	if err := store.PutBody(id, record.KindMemory, body); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetBody(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body came back as %q", got)
	}
}

// Ranking metadata is read before anything is fetched, so it has to survive
// this device dropping its copy of the record body.
func TestRankingMetadataOutlivesTheBody(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	id, _ := record.NewID()
	if err := store.PutBody(id, record.KindMemory, []byte("{}")); err != nil {
		t.Fatalf("put body: %v", err)
	}
	if err := store.PutRankingMeta(local.RankingMeta{
		ID: id, Type: record.TypeFact, Tags: []string{"Sia", " storage ", "sia"},
	}); err != nil {
		t.Fatalf("put ranking metadata: %v", err)
	}

	// Dropping the local body is how the lower read tiers are reached; the
	// record is still on the network and still has to rank.
	if err := store.ForgetBody(id); err != nil {
		t.Fatalf("forget body: %v", err)
	}

	meta, err := store.RankingMetaFor(id)
	if err != nil {
		t.Fatalf("read ranking metadata after forgetting the body: %v", err)
	}
	if meta.Type != record.TypeFact {
		t.Fatalf("type came back as %q", meta.Type)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "sia" || meta.Tags[1] != "storage" {
		t.Fatalf("tags came back as %v, want them normalised and deduplicated", meta.Tags)
	}
}

// Tag frequency is what makes a tag's specificity visible at write time.
func TestTagFrequenciesCountRecords(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	for i := range 5 {
		id, _ := record.NewID()
		tags := []string{"sia"}
		if i == 0 {
			tags = append(tags, "slab-quantum")
		}
		if err := store.PutRankingMeta(local.RankingMeta{
			ID: id, Type: record.TypeFact, Tags: tags,
		}); err != nil {
			t.Fatalf("put ranking metadata: %v", err)
		}
	}

	counts, total, err := store.TagFrequencies(record.ID{}, []string{"sia", "slab-quantum", "never-used"})
	if err != nil {
		t.Fatalf("tag frequencies: %v", err)
	}
	if total != 5 {
		t.Fatalf("counted %d records, want 5", total)
	}
	want := map[string]int{"sia": 5, "slab-quantum": 1, "never-used": 0}
	for _, count := range counts {
		if want[count.Tag] != count.Records {
			t.Fatalf("tag %q counted %d records, want %d", count.Tag, count.Records, want[count.Tag])
		}
	}
}

// A record is left out of its own tag counts, because the write path stores it
// before it asks how specific its tags are.
func TestTagFrequenciesLeaveOutTheNamedRecord(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	var newest record.ID
	for i := range 3 {
		id, _ := record.NewID()
		tags := []string{"sia"}
		if i == 2 {
			newest, tags = id, []string{"sia", "first-use"}
		}
		if err := store.PutRankingMeta(local.RankingMeta{
			ID: id, Type: record.TypeFact, Tags: tags,
		}); err != nil {
			t.Fatalf("put ranking metadata: %v", err)
		}
	}

	counts, total, err := store.TagFrequencies(newest, []string{"sia", "first-use"})
	if err != nil {
		t.Fatalf("tag frequencies: %v", err)
	}
	if total != 2 {
		t.Fatalf("counted %d other records, want 2", total)
	}
	want := map[string]int{"sia": 2, "first-use": 0}
	for _, count := range counts {
		if want[count.Tag] != count.Records {
			t.Fatalf("tag %q counted %d records, want %d", count.Tag, count.Records, want[count.Tag])
		}
	}
}

// Every protocol client launches its own process against one vault, so
// concurrent writers are the normal case rather than an edge one.
func TestConcurrentProcessesShareOneDatabase(t *testing.T) {
	if os.Getenv(writerEnv) != "" {
		runWriter(t)
		return
	}
	const (
		writers = 3
		rows    = 200
	)
	path := filepath.Join(t.TempDir(), "vault.db")

	// The database has to exist before the writers race for it: creating it is
	// the one step that needs a lock no other connection holds.
	seed, err := local.Open(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	seed.Close()

	var wg sync.WaitGroup
	failures := make([]error, writers)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run", "^TestConcurrentProcessesShareOneDatabase$")
			cmd.Env = append(os.Environ(),
				writerEnv+"="+strconv.Itoa(w),
				pathEnv+"="+path,
				rowsEnv+"="+strconv.Itoa(rows))
			if out, err := cmd.CombinedOutput(); err != nil {
				failures[w] = fmt.Errorf("writer %d: %w\n%s", w, err, out)
			}
		}()
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}

	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	count, err := store.CountBodies()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := writers * rows; count != want {
		t.Fatalf("%d records survived %d concurrent writers, want %d", count, writers, want)
	}
}

// runWriter is the child half of the concurrency test: one process writing into
// a database two others are writing to at the same time.
func runWriter(t *testing.T) {
	path := os.Getenv(pathEnv)
	rows, err := strconv.Atoi(os.Getenv(rowsEnv))
	if err != nil {
		t.Fatalf("bad %s: %v", rowsEnv, err)
	}
	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	for range rows {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("new id: %v", err)
		}
		if err := store.PutBody(id, record.KindMemory, []byte("{}")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
}

const (
	writerEnv = "SENNIT_TEST_WRITER"
	pathEnv   = "SENNIT_TEST_DB"
	rowsEnv   = "SENNIT_TEST_ROWS"
)
