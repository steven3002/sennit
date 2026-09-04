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

func lexID(b byte) record.ID {
	var id record.ID
	id[record.IDSize-1] = b
	return id
}

func put(t *testing.T, store *local.Store, id record.ID, statement, context string, tags ...string) {
	t.Helper()
	memory := &record.Memory{
		ID: id, Kind: record.KindMemory, Type: record.TypeFact,
		SchemaVersion: record.SchemaVersion, Version: 1,
		Statement: statement, Context: context, Tags: tags,
		CreatedAt: record.Now(), UpdatedAt: record.Now(),
	}
	if err := store.PutRankingMeta(local.RankingMeta{
		ID: id, Type: memory.Type, Tags: tags, Text: local.LexicalText(memory),
	}); err != nil {
		t.Fatalf("index %s: %v", id, err)
	}
}

// The lexical pass has to find a record by a word the record actually uses, and
// rank it above one that merely shares the vault.
func TestLexicalSearchRanksTheMatchingRecordFirst(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	wanted := lexID(1)
	put(t, store, wanted, "Repack rewrites every object id in the vault.",
		"Measured across a thousand records.", "storage")
	put(t, store, lexID(2), "Session transcripts are chunked at 256 KiB.",
		"Settled during the sessions design.", "sessions")
	put(t, store, lexID(3), "The weekly check-in is on Thursday.",
		"Standing commitment.", "grant")

	hits, err := store.SearchLexical("what does repack do to object ids", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("the lexical pass returned nothing for a query sharing three words with a record")
	}
	if hits[0] != wanted {
		t.Fatalf("ranked %s first, want %s", hits[0], wanted)
	}
}

// A query made only of stopwords carries no information about which record
// answers it, and must not return an arbitrary ranking of the whole vault.
func TestAStopwordOnlyQueryMatchesNothing(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	put(t, store, lexID(1), "Repack rewrites every object id.", "Context here.", "storage")

	for _, query := range []string{"what is that", "how does this", "   ", "?!?"} {
		hits, err := store.SearchLexical(query, 5)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(hits) != 0 {
			t.Fatalf("query %q matched %d records on stopwords alone", query, len(hits))
		}
	}
}

// FTS5 has a query syntax, and a user's question is not written in it. A word
// that happens to be an operator has to be matched as a word.
func TestQuerySyntaxInTheQuestionIsMatchedAsWords(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	put(t, store, lexID(1), "The packer flushes near the slab boundary.",
		"Noted while sizing slabs.", "storage")

	for _, query := range []string{
		`near packer`,
		`packer OR NOT AND`,
		`"packer" (flushes)`,
		`packer* ^flushes`,
		`slab NEAR/3 boundary`,
		`packer" OR "x`,
	} {
		if _, err := store.SearchLexical(query, 5); err != nil {
			t.Fatalf("query %q was not handled as words: %v", query, err)
		}
	}
}

// The index is on disk, not in the process. A vault reopened by a later run has
// to rank the same way without reindexing anything.
func TestLexicalIndexSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	store, err := local.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wanted := lexID(1)
	put(t, store, wanted, "Repack rewrites every object id in the vault.",
		"Measured across a thousand records.", "storage")
	put(t, store, lexID(2), "Sessions are chunked at 256 KiB.", "Sessions design.", "sessions")
	before, err := store.SearchLexical("repack object ids", 5)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := local.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	count, err := reopened.CountLexical()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("%d records carry terms after a reopen, want 2", count)
	}
	after, err := reopened.SearchLexical("repack object ids", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || len(after) == 0 || after[0] != before[0] {
		t.Fatalf("ranking changed across a reopen: %v then %v", before, after)
	}
}

// Rewriting a record's metadata replaces its terms rather than accumulating
// them, or a record edited twice would be indexed under words it no longer uses.
func TestReindexingARecordReplacesItsTerms(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id := lexID(1)
	put(t, store, id, "The packer flushes at the slab boundary.", "First wording.", "storage")
	put(t, store, id, "Records are uploaded once the batch fills.", "Second wording.", "storage")

	count, err := store.CountLexical()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("one record occupies %d rows in the lexical index", count)
	}
	stale, err := store.SearchLexical("packer flushes slab boundary", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("the replaced wording still matches: %v", stale)
	}
	current, err := store.SearchLexical("records uploaded batch fills", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0] != id {
		t.Fatalf("the current wording matched %v, want just %s", current, id)
	}
}

// A record dropped from the vault stops being searchable, or reclaim would
// leave the ranker proposing records nothing can fetch.
func TestForgettingARecordRemovesItsTerms(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id := lexID(1)
	put(t, store, id, "Repack rewrites every object id.", "Context.", "storage")
	if err := store.ForgetRankingMeta(id); err != nil {
		t.Fatalf("forget: %v", err)
	}
	hits, err := store.SearchLexical("repack object id", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("a forgotten record is still searchable: %v", hits)
	}
	count, err := store.CountLexical()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d lexical rows survived forgetting the only record", count)
	}
}

// Every protocol client runs its own process against one vault, so the lexical
// index has to tolerate concurrent writers exactly as the rest of the store
// does.
func TestConcurrentProcessesWriteTheLexicalIndex(t *testing.T) {
	if os.Getenv(lexWriterEnv) != "" {
		runLexWriter(t)
		return
	}
	const (
		writers = 3
		rows    = 60
	)
	path := filepath.Join(t.TempDir(), "vault.db")
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
			cmd := exec.Command(os.Args[0], "-test.run", "^TestConcurrentProcessesWriteTheLexicalIndex$")
			cmd.Env = append(os.Environ(),
				lexWriterEnv+"="+strconv.Itoa(w),
				lexPathEnv+"="+path,
				lexRowsEnv+"="+strconv.Itoa(rows))
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

	count, err := store.CountLexical()
	if err != nil {
		t.Fatal(err)
	}
	if want := writers * rows; count != want {
		t.Fatalf("%d records carry terms after %d concurrent writers, want %d", count, writers, want)
	}
	// The index has to be usable, not merely populated: a torn FTS5 write would
	// leave rows that match nothing.
	hits, err := store.SearchLexical("concurrent writer statement", writers*rows)
	if err != nil {
		t.Fatalf("search after concurrent writes: %v", err)
	}
	if len(hits) != writers*rows {
		t.Fatalf("%d of %d concurrently written records are searchable", len(hits), writers*rows)
	}
}

func runLexWriter(t *testing.T) {
	path := os.Getenv(lexPathEnv)
	writer := os.Getenv(lexWriterEnv)
	rows, err := strconv.Atoi(os.Getenv(lexRowsEnv))
	if err != nil {
		t.Fatalf("bad %s: %v", lexRowsEnv, err)
	}
	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	for row := range rows {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("new id: %v", err)
		}
		put(t, store, id,
			fmt.Sprintf("Concurrent writer %s produced statement %d.", writer, row),
			"Written while two other processes wrote the same vault.", "concurrency")
	}
}

const (
	lexWriterEnv = "SENNIT_TEST_LEX_WRITER"
	lexPathEnv   = "SENNIT_TEST_LEX_DB"
	lexRowsEnv   = "SENNIT_TEST_LEX_ROWS"
)
