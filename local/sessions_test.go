package local_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"

	_ "modernc.org/sqlite"
)

func sessionID(t *testing.T, b byte) record.ID {
	t.Helper()
	var id record.ID
	id[record.IDSize-1] = b
	return id
}

func head(t *testing.T, store *local.Store, id record.ID, title string, kind record.SessionKind, updated record.Time) *record.Session {
	t.Helper()
	session := &record.Session{
		ID: id, Schema: record.MessageSchema, SchemaVersion: record.SchemaVersion,
		Version: 1, Kind: kind, Title: title,
		Summary: title + " summary", Created: updated, Updated: updated,
		Project: record.Project{Repo: "tidepool/field"},
		Counts:  record.Counts{Messages: 4},
	}
	body, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal head: %v", err)
	}
	if err := store.PutSessionHead(session, body, 0); err != nil {
		t.Fatalf("store head: %v", err)
	}
	return session
}

func openStore(t *testing.T) *local.Store {
	t.Helper()
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// A listing reads the columns it needs and orders newest first, which is what
// makes browsing a thousand conversations independent of how large they are.
func TestListingSessionsIsOrderedAndFiltered(t *testing.T) {
	store := openStore(t)

	base := record.Now()
	main := head(t, store, sessionID(t, 1), "main run", record.SessionMain, base)
	older := head(t, store, sessionID(t, 2), "older run", record.SessionMain,
		record.At(base.Add(-2*time.Hour)))
	child := head(t, store, sessionID(t, 3), "delegated run", record.SessionSubagent, base)
	child.Lineage.ParentSession = &main.ID
	body, err := record.MarshalSession(child)
	if err != nil {
		t.Fatalf("marshal child: %v", err)
	}
	if err := store.PutSessionHead(child, body, 1); err != nil {
		t.Fatalf("store child: %v", err)
	}

	rows, err := store.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("listed %d sessions, want 3", len(rows))
	}
	if rows[len(rows)-1].ID != older.ID {
		t.Fatalf("the oldest session is not last: %s", rows[len(rows)-1].Title)
	}

	mains, err := store.ListSessions(local.SessionQuery{Kinds: []record.SessionKind{record.SessionMain}})
	if err != nil {
		t.Fatalf("list main: %v", err)
	}
	if len(mains) != 2 {
		t.Fatalf("listed %d main sessions, want 2", len(mains))
	}

	children, err := store.ListSessions(local.SessionQuery{Parent: &main.ID})
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("listing by parent returned %d rows", len(children))
	}
	if children[0].Parent == nil || *children[0].Parent != main.ID {
		t.Fatal("a listed sub-agent does not carry its parent")
	}

	since := record.At(base.Add(-time.Hour))
	recent, err := store.ListSessions(local.SessionQuery{Since: &since})
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("listed %d recent sessions, want 2", len(recent))
	}

	byProject, err := store.ListSessions(local.SessionQuery{Project: "tidepool/field"})
	if err != nil {
		t.Fatalf("list by project: %v", err)
	}
	if len(byProject) != 3 {
		t.Fatalf("listed %d sessions in the project, want 3", len(byProject))
	}
	if none, err := store.ListSessions(local.SessionQuery{Project: "somewhere/else"}); err != nil {
		t.Fatalf("list by another project: %v", err)
	} else if len(none) != 0 {
		t.Fatalf("a project nothing is filed under returned %d rows", len(none))
	}
}

// A head is rewritten in place on every append, and the listing has to follow
// it: the version, the counts and the timestamp are what a browser renders.
func TestASessionHeadIsReplacedInPlace(t *testing.T) {
	store := openStore(t)
	session := head(t, store, sessionID(t, 7), "first", record.SessionMain, record.Now())

	session.Version = 2
	session.Title = "second"
	session.Counts.Messages = 12
	session.Updated = record.Now()
	body, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.PutSessionHead(session, body, 1); err != nil {
		t.Fatalf("replace head: %v", err)
	}

	held, err := store.CountSessions()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if held != 1 {
		t.Fatalf("rewriting a head produced %d sessions", held)
	}
	rows, err := store.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows[0].Version != 2 || rows[0].Title != "second" || rows[0].Counts.Messages != 12 {
		t.Fatalf("the listing did not follow the rewritten head: %+v", rows[0])
	}

	stored, err := store.GetSessionHead(session.ID)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	reloaded, err := record.UnmarshalSession(stored)
	if err != nil {
		t.Fatalf("parse head: %v", err)
	}
	if reloaded.Version != 2 {
		t.Fatalf("the stored head is at version %d", reloaded.Version)
	}
}

// Appending to a session reads the head, writes chunks over the network and
// writes the head back, so the write cannot be held inside a transaction. The
// version guard is what stops the slower of two writers erasing the other's
// turns instead of being told to read again.
func TestAHeadWrittenAgainstAVersionThatHasPassedIsRefused(t *testing.T) {
	store := openStore(t)
	session := head(t, store, sessionID(t, 11), "first", record.SessionMain, record.Now())

	advance := func(title string, from int64) error {
		session.Version = from + 1
		session.Title = title
		body, err := record.MarshalSession(session)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return store.PutSessionHead(session, body, from)
	}

	if err := advance("second", 1); err != nil {
		t.Fatalf("the first writer was refused: %v", err)
	}
	if err := advance("third from a stale read", 1); !errors.Is(err, local.ErrStaleHead) {
		t.Fatalf("a write against version 1 after it had moved on returned %v", err)
	}

	rows, err := store.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows[0].Title != "second" || rows[0].Version != 2 {
		t.Fatalf("the refused write landed anyway: %+v", rows[0])
	}

	// Creating a session whose id is taken is the same refusal, not an
	// overwrite of somebody else's conversation.
	body, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.PutSessionHead(session, body, 0); !errors.Is(err, local.ErrStaleHead) {
		t.Fatalf("creating a session over an existing one returned %v", err)
	}
}

// A vault written before sessions existed is a file on somebody's disk, not
// something that can be recreated, so the column ranking now needs has to be
// added to a table that is already there.
func TestAVaultWrittenBeforeSessionsGainsTheKindColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE record_meta (
		record_id  TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		supersedes TEXT,
		created_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the old table: %v", err)
	}
	id := sessionID(t, 42)
	if _, err := old.Exec(
		`INSERT INTO record_meta (record_id, type, created_at) VALUES (?, 'fact', ?)`,
		id.String(), record.Now().String()); err != nil {
		t.Fatalf("insert an old row: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("open the old vault: %v", err)
	}
	defer store.Close()

	meta, err := store.RankingMetaFor(id)
	if err != nil {
		t.Fatalf("read the migrated row: %v", err)
	}
	if meta.Kind != record.KindMemory {
		t.Fatalf("a record written before sessions existed came back as kind %q, want %q",
			meta.Kind, record.KindMemory)
	}
	if meta.Type != record.TypeFact {
		t.Fatalf("the migration changed the record's type to %q", meta.Type)
	}

	// And a second open is a no-op rather than a duplicate-column error.
	again, err := local.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// A session ranks on its own metadata, under its own class, in the same tables
// memories use, which is what puts both in one ranked list.
func TestASessionIsDescribedUnderItsOwnKind(t *testing.T) {
	store := openStore(t)
	session := sessionID(t, 5)
	memory := sessionID(t, 6)

	if err := store.PutRankingMeta(local.RankingMeta{
		ID: session, Kind: record.KindSession, Tags: []string{"tidepool"},
		Text: "ingestion rules conversation",
	}); err != nil {
		t.Fatalf("store session metadata: %v", err)
	}
	if err := store.PutRankingMeta(local.RankingMeta{
		ID: memory, Kind: record.KindMemory, Type: record.TypeFact,
		Tags: []string{"tidepool"}, Text: "ingestion rejects tall readings",
	}); err != nil {
		t.Fatalf("store memory metadata: %v", err)
	}

	described, err := store.Describe([]record.ID{session, memory})
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Meta[session].Kind != record.KindSession {
		t.Fatalf("the session is described as %q", described.Meta[session].Kind)
	}
	if described.Meta[memory].Kind != record.KindMemory {
		t.Fatalf("the memory is described as %q", described.Meta[memory].Kind)
	}
	if described.Meta[session].Type != "" {
		t.Fatalf("a session was given the memory type %q", described.Meta[session].Type)
	}

	// Both are reachable by their words, from the one term index.
	hits, err := store.SearchLexical("ingestion", 10)
	if err != nil {
		t.Fatalf("lexical search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("the term index returned %d records, want both kinds", len(hits))
	}
}
