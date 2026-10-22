package local_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// listingMarker is the setting these tests record the rewrite under.
const listingMarker = "listingDatedByRecords"

// timeIn is the time a record carries, read from this device's copy of it the
// way the vault reads it: a memory's own creation time, and a conversation
// head's Created.
func timeIn(kind record.Kind, held []byte) (record.Time, error) {
	switch kind {
	case record.KindMemory:
		memory, err := record.Unmarshal(held)
		if err != nil {
			return record.Time{}, err
		}
		return memory.CreatedAt, nil
	case record.KindSession:
		session, err := record.UnmarshalSession(held)
		if err != nil {
			return record.Time{}, err
		}
		return session.Created, nil
	}
	return record.Time{}, fmt.Errorf("no time is read from a %s", kind)
}

// timesOnly dates each record as the vault does and leaves every head's dates
// as they are.
type timesOnly struct{}

func (timesOnly) TimeOf(kind record.Kind, held []byte) (record.Time, error) {
	return timeIn(kind, held)
}

func (timesOnly) Undated([]byte) (*record.Session, []byte, error) { return nil, nil, nil }

// undating is timesOnly that also takes the dates off the heads it names, as the
// vault takes them off a head an earlier release rebuilt: both cleared, and the
// head one revision on.
type undating struct {
	timesOnly
	heads map[record.ID]bool
}

func (u undating) Undated(held []byte) (*record.Session, []byte, error) {
	session, err := record.UnmarshalSession(held)
	if err != nil || !u.heads[session.ID] {
		return nil, nil, nil
	}
	session.Created, session.Updated = record.Time{}, record.Time{}
	session.Version++
	head, err := record.MarshalSession(session)
	return session, head, err
}

// A listedRecord is one row of a device's listing: the time its record carries,
// and the time a build before records were listed by their own times gave the
// row when a restore reached the record.
type listedRecord struct {
	name     string
	id       record.ID
	carries  record.Time
	restored record.Time
	// kept is whether a rewrite leaves the row at the restore's time, because
	// this device holds no copy of the record or holds one that will not read.
	kept bool
}

// What a device holds of a record a listing row names.
const (
	copyHeld = iota
	copyMissing
	copyGarbled
)

// restoredListing writes the listing a restore by an earlier build left on a
// device, each row dated by when the restore reached its record, together with
// this device's copy of each record. Besides records whose copy is here, it
// holds a memory whose body is not, a conversation whose head is not, a memory
// whose body will not read, a conversation whose turns recorded no time, and a
// memory whose row already carries the memory's own time, as a row written
// since does.
func restoredListing(t *testing.T, store *local.Store) []listedRecord {
	t.Helper()
	stored := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	at := func(minutes int) record.Time { return record.At(stored.Add(time.Duration(minutes) * time.Minute)) }
	restoredAt := time.Date(2026, time.October, 2, 16, 0, 0, 0, time.UTC)

	var out []listedRecord
	for i, c := range []struct {
		name    string
		kind    record.Kind
		carries record.Time
		held    int
		current bool
	}{
		{name: "harbour", kind: record.KindMemory, carries: at(0)},
		{name: "river", kind: record.KindMemory, carries: at(1)},
		{name: "survey", kind: record.KindSession, carries: at(2)},
		{name: "undated", kind: record.KindSession},
		{name: "estuary", kind: record.KindMemory, carries: at(3), held: copyMissing},
		{name: "lost", kind: record.KindSession, carries: at(4), held: copyMissing},
		{name: "garbled", kind: record.KindMemory, carries: at(5), held: copyGarbled},
		{name: "current", kind: record.KindMemory, carries: at(6), current: true},
	} {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		r := listedRecord{
			name: c.name, id: id, carries: c.carries, kept: c.held != copyHeld,
			restored: record.At(restoredAt.Add(time.Duration(i) * time.Second)),
		}
		if c.current {
			r.restored = c.carries
		}
		switch {
		case c.held == copyMissing:
		case c.held == copyGarbled:
			if err := store.PutBody(id, c.kind, []byte("not a memory")); err != nil {
				t.Fatalf("store the %s body: %v", c.name, err)
			}
		case c.kind == record.KindMemory:
			body, err := record.Marshal(&record.Memory{
				ID: id, Kind: record.KindMemory, Type: record.TypeFact, SchemaVersion: record.SchemaVersion,
				Version: 1, Statement: "The " + c.name + " gauge at Tidepool reports every hour.",
				Context: "Restored by the listing tests.", CreatedAt: c.carries, UpdatedAt: c.carries,
			})
			if err != nil {
				t.Fatalf("marshal the %s memory: %v", c.name, err)
			}
			if err := store.PutBody(id, record.KindMemory, body); err != nil {
				t.Fatalf("store the %s body: %v", c.name, err)
			}
		default:
			session := &record.Session{
				ID: id, Schema: record.MessageSchema, SchemaVersion: record.SchemaVersion, Version: 1,
				Kind: record.SessionMain, Title: "The " + c.name + " conversation",
				Created: c.carries, Updated: c.carries,
			}
			head, err := record.MarshalSession(session)
			if err != nil {
				t.Fatalf("marshal the %s head: %v", c.name, err)
			}
			if err := store.PutSessionHead(session, head, 0); err != nil {
				t.Fatalf("store the %s head: %v", c.name, err)
			}
		}
		if err := store.PutRankingMeta(local.RankingMeta{
			ID: id, Kind: c.kind, Type: record.TypeFact, Created: r.restored,
		}); err != nil {
			t.Fatalf("list the %s record: %v", c.name, err)
		}
		out = append(out, r)
	}
	return out
}

// openDevice opens the database at path and closes it when the test ends.
func openDevice(t *testing.T, path string) *local.Store {
	t.Helper()
	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// rawDB opens a device's database through a connection of the test's own, the
// way a second process or an earlier build reaches the same file.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// listedAt reads the time each row of a device's listing is dated by, every
// page of it.
func listedAt(t *testing.T, store *local.Store) map[record.ID]record.Time {
	t.Helper()
	out := make(map[record.ID]record.Time)
	var cursor local.Cursor
	for {
		rows, next, err := store.ListRecords(local.RecordQuery{Limit: local.MaxRecordLimit, Cursor: cursor})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, row := range rows {
			out[row.ID] = row.Created
		}
		if next == "" {
			return out
		}
		cursor = next
	}
}

// markerValue reads the marker as it is stored, and whether it is there at all.
func markerValue(t *testing.T, store *local.Store) (string, bool) {
	t.Helper()
	value, err := store.GetMeta(listingMarker)
	if errors.Is(err, local.ErrNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read the marker: %v", err)
	}
	return value, true
}

// recordedRewrite reads what the marker says the rewrite did.
func recordedRewrite(t *testing.T, store *local.Store) local.ListingRewrite {
	t.Helper()
	value, ok := markerValue(t, store)
	if !ok {
		t.Fatal("the rewrite left no marker")
	}
	var rewrite local.ListingRewrite
	if err := json.Unmarshal([]byte(value), &rewrite); err != nil {
		t.Fatalf("the marker holds %q, which is not a rewrite: %v", value, err)
	}
	return rewrite
}

// counted says in words what a rewrite counted, so that two can be compared
// and either one read in a failure.
func counted(r local.ListingRewrite) string {
	return fmt.Sprintf("%d rows read, %d rewritten, %d not held, %d unreadable",
		r.Rows, r.Rewritten, r.NotHeld, r.Unreadable)
}

// releaseHead writes a conversation head as a release that dated a rebuilt head
// by the rebuild left it: its Created and its Updated both the moment of the
// rebuild, at version 1, and its listing row dated a moment later, when the
// rebuild filed its metadata.
func releaseHead(t *testing.T, store *local.Store, name string, rebuiltAt time.Time) listedRecord {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	at := record.At(rebuiltAt)
	session := &record.Session{
		ID: id, Schema: record.MessageSchema, SchemaVersion: record.SchemaVersion, Version: 1,
		Kind: record.SessionMain, Title: "The " + name + " conversation", Created: at, Updated: at,
	}
	head, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal the %s head: %v", name, err)
	}
	if err := store.PutSessionHead(session, head, 0); err != nil {
		t.Fatalf("store the %s head: %v", name, err)
	}
	restored := record.At(rebuiltAt.Add(time.Millisecond))
	if err := store.PutRankingMeta(local.RankingMeta{ID: id, Kind: record.KindSession, Created: restored}); err != nil {
		t.Fatalf("list the %s conversation: %v", name, err)
	}
	return listedRecord{name: name, id: id, carries: at, restored: restored}
}

// storedHead reads back the head this device holds for a conversation.
func storedHead(t *testing.T, store *local.Store, id record.ID) *record.Session {
	t.Helper()
	held, err := store.GetSessionHead(id)
	if err != nil {
		t.Fatalf("read the head of %s: %v", id, err)
	}
	session, err := record.UnmarshalSession(held)
	if err != nil {
		t.Fatalf("decode the head of %s: %v", id, err)
	}
	return session
}

// A head the dater picks out is stored without its dates, one revision on, in
// the same rewrite that dates the listing: its row is dated by the head it is
// left with, which puts it after every dated record, and the conversation
// listing puts it after every conversation that has a time. A head the dater
// does not pick out is left exactly as it was, dates and revision both.
func TestAHeadTheDaterUndatesIsStoredOneRevisionOnAndListedLast(t *testing.T) {
	store := openStore(t)
	rebuiltAt := time.Date(2026, time.October, 2, 17, 0, 0, 0, time.UTC)
	rebuilt := releaseHead(t, store, "rebuilt", rebuiltAt)
	kept := releaseHead(t, store, "kept", rebuiltAt.Add(time.Minute))

	rewrite, err := store.RewriteListingDates(listingMarker, undating{heads: map[record.ID]bool{rebuilt.id: true}})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !rewrite.Ran || rewrite.Undated != 1 {
		t.Errorf("the rewrite was made %v and undated %d head(s), want it made and 1 undated", rewrite.Ran, rewrite.Undated)
	}

	head := storedHead(t, store, rebuilt.id)
	if head.Version != 2 || !head.Created.IsZero() || !head.Updated.IsZero() {
		t.Errorf("the undated head is at version %d, created %s, updated %s; want version 2 and no dates",
			head.Version, head.Created, head.Updated)
	}
	if untouched := storedHead(t, store, kept.id); untouched.Version != 1 ||
		!untouched.Created.Equal(kept.carries.Time) || !untouched.Updated.Equal(kept.carries.Time) {
		t.Errorf("the head the dater left is at version %d, created %s, updated %s; want it as it was, "+
			"at version 1 and dated %s", untouched.Version, untouched.Created, untouched.Updated, kept.carries)
	}

	sessions, err := store.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 || sessions[1].ID != rebuilt.id {
		t.Fatalf("the conversations are listed %+v, want the undated one last", sessions)
	}
	if last := sessions[1]; !last.Created.IsZero() || !last.Updated.IsZero() {
		t.Errorf("the undated conversation is listed as created %s and updated %s, want neither", last.Created, last.Updated)
	}
	listed := listedAt(t, store)
	if got := listed[rebuilt.id]; !got.IsZero() {
		t.Errorf("the undated conversation's row is dated %s, want no time", got)
	}
	if got := listed[kept.id]; !got.Equal(kept.carries.Time) {
		t.Errorf("the kept conversation's row is dated %s, want %s, the time its head carries", got, kept.carries)
	}
	if recorded := recordedRewrite(t, store); recorded.Undated != 1 {
		t.Errorf("the marker records %d head(s) undated, want 1", recorded.Undated)
	}
}

// Rewriting the listing dates each row by the time the copy of its record on
// this device carries, and records what it did. A memory is dated by its own
// creation time and a conversation by its head's Created, which for one whose
// turns recorded no time is the zero time, so it is listed after every record
// that has a time. A row whose record is not on this device, or whose copy will
// not read, is left as it was and counted.
func TestRewritingTheListingDatesEachRowByTheCopyOfItsRecordOnThisDevice(t *testing.T) {
	store := openStore(t)
	records := restoredListing(t, store)

	rewrite, err := store.RewriteListingDates(listingMarker, timesOnly{})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	want := local.ListingRewrite{Rows: len(records), Rewritten: 4, NotHeld: 2, Unreadable: 1}
	if !rewrite.Ran || counted(rewrite) != counted(want) {
		t.Errorf("the rewrite was made %v, with %s; want it made, with %s", rewrite.Ran, counted(rewrite), counted(want))
	}

	listed := listedAt(t, store)
	for _, r := range records {
		dated := r.carries
		if r.kept {
			dated = r.restored
		}
		if got := listed[r.id]; !got.Equal(dated.Time) || got.IsZero() != dated.IsZero() {
			t.Errorf("the %s row is dated %s, want %s", r.name, got, dated)
		}
	}

	if recorded := recordedRewrite(t, store); counted(recorded) != counted(want) || recorded.At.IsZero() {
		t.Errorf("the marker records %s, made at %s; want %s and when it was made",
			counted(recorded), recorded.At, counted(want))
	}
}

// The listing is rewritten once. A later call, from a handle opened afresh as
// the next process to open the vault opens one, finds the marker and leaves
// every row as it is, including a row that changed after the rewrite.
func TestTheListingIsRewrittenOnceAndALaterCallLeavesEveryRowAsItFindsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	records := restoredListing(t, store)
	if rewrite, err := store.RewriteListingDates(listingMarker, timesOnly{}); err != nil || !rewrite.Ran {
		t.Fatalf("the first rewrite was made %v, with %s: %v", rewrite.Ran, counted(rewrite), err)
	}
	first, _ := markerValue(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	changed := records[0]
	later := record.At(time.Date(2031, time.January, 1, 0, 0, 0, 0, time.UTC))
	if _, err := rawDB(t, path).Exec(`UPDATE record_meta SET created_at = ? WHERE record_id = ?`,
		later.String(), changed.id.String()); err != nil {
		t.Fatalf("change the %s row: %v", changed.name, err)
	}

	reopened := openDevice(t, path)
	again, err := reopened.RewriteListingDates(listingMarker, timesOnly{})
	if err != nil {
		t.Fatalf("rewrite again: %v", err)
	}
	if again.Ran {
		t.Errorf("the listing was rewritten a second time, with %s", counted(again))
	}
	if got := listedAt(t, reopened)[changed.id]; !got.Equal(later.Time) {
		t.Errorf("the %s row is dated %s after the second call, want %s, the time it was changed to",
			changed.name, got, later)
	}
	if value, _ := markerValue(t, reopened); value != first {
		t.Errorf("the marker holds %s after the second call, want %s, what the first recorded", value, first)
	}
}

// A rewrite that stops part way changes nothing: every head and every row keeps
// what it had and no marker is set, so the next call makes the whole rewrite. It
// is stopped here at its last step, recording the marker, after a head has been
// stored without its dates and every row has been given its new time.
func TestARewriteThatStopsPartWayChangesNothingAndTheNextMakesAllOfIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	store := openDevice(t, path)
	records := restoredListing(t, store)
	rebuilt := releaseHead(t, store, "rebuilt", time.Date(2026, time.October, 2, 17, 0, 0, 0, time.UTC))
	dater := undating{heads: map[record.ID]bool{rebuilt.id: true}}

	db := rawDB(t, path)
	if _, err := db.Exec(`CREATE TRIGGER refuse_marker BEFORE INSERT ON meta WHEN NEW.key = '` +
		listingMarker + `' BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`); err != nil {
		t.Fatalf("make the device refuse the marker: %v", err)
	}
	if _, err := store.RewriteListingDates(listingMarker, dater); err == nil ||
		!strings.Contains(err.Error(), "the disk refused the write") {
		t.Fatalf("the rewrite the disk stopped returned %v", err)
	}
	listed := listedAt(t, store)
	for _, r := range append(slices.Clone(records), rebuilt) {
		if got := listed[r.id]; !got.Equal(r.restored.Time) {
			t.Errorf("the stopped rewrite left the %s row dated %s, want %s, the time it had", r.name, got, r.restored)
		}
	}
	if head := storedHead(t, store, rebuilt.id); head.Version != 1 || !head.Created.Equal(rebuilt.carries.Time) ||
		!head.Updated.Equal(rebuilt.carries.Time) {
		t.Errorf("the stopped rewrite left the rebuilt head at version %d, created %s, updated %s; "+
			"want it as it was, at version 1 and dated %s", head.Version, head.Created, head.Updated, rebuilt.carries)
	}
	if _, ok := markerValue(t, store); ok {
		t.Error("the stopped rewrite left a marker")
	}

	if _, err := db.Exec(`DROP TRIGGER refuse_marker`); err != nil {
		t.Fatalf("lift the refusal: %v", err)
	}
	rewrite, err := store.RewriteListingDates(listingMarker, dater)
	if err != nil {
		t.Fatalf("rewrite after the refusal: %v", err)
	}
	if !rewrite.Ran || rewrite.Rewritten != 5 || rewrite.Undated != 1 {
		t.Errorf("the next rewrite was made %v, with %s and %d head(s) undated; want it made, rewriting all "+
			"5 rows it can date and undating the 1 head", rewrite.Ran, counted(rewrite), rewrite.Undated)
	}
	listed = listedAt(t, store)
	for _, r := range records {
		if r.kept {
			continue
		}
		if got := listed[r.id]; !got.Equal(r.carries.Time) || got.IsZero() != r.carries.IsZero() {
			t.Errorf("the %s row is dated %s, want %s", r.name, got, r.carries)
		}
	}
	if got := listed[rebuilt.id]; !got.IsZero() {
		t.Errorf("the rebuilt conversation's row is dated %s, want no time", got)
	}
	if head := storedHead(t, store, rebuilt.id); head.Version != 2 || !head.Created.IsZero() || !head.Updated.IsZero() {
		t.Errorf("the rebuilt head is at version %d, created %s, updated %s; want version 2 and no dates",
			head.Version, head.Created, head.Updated)
	}
}

// Processes that rewrite one device's listing at the same moment make the
// rewrite once between them, as a running server and a command run beside it
// both open the vault. Each is held until all of them are ready, so each looks
// for the marker before any has set it. One makes the rewrite; the others wait
// for its transaction, find the marker under the lock, and change nothing.
func TestProcessesRewritingTheListingAtOnceMakeTheRewriteOnce(t *testing.T) {
	if os.Getenv(rewriterEnv) != "" {
		runRewriter(t)
		return
	}
	const (
		rewriters = 3
		memories  = 400
	)
	path := filepath.Join(t.TempDir(), "vault.db")
	store, err := local.Open(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	restoredAt := record.At(time.Date(2026, time.October, 2, 16, 0, 0, 0, time.UTC))
	carries := make(map[record.ID]record.Time, memories)
	for i := range memories {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		created := record.At(stored.Add(time.Duration(i) * time.Minute))
		body, err := record.Marshal(&record.Memory{
			ID: id, Kind: record.KindMemory, Type: record.TypeFact, SchemaVersion: record.SchemaVersion,
			Version: 1, Statement: fmt.Sprintf("Tidepool station %d reports every hour.", i),
			Context: "Restored by the listing tests.", CreatedAt: created, UpdatedAt: created,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := store.PutBody(id, record.KindMemory, body); err != nil {
			t.Fatalf("store the body: %v", err)
		}
		if err := store.PutRankingMeta(local.RankingMeta{
			ID: id, Kind: record.KindMemory, Type: record.TypeFact, Created: restoredAt,
		}); err != nil {
			t.Fatalf("list the memory: %v", err)
		}
		carries[id] = created
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	gate := t.TempDir()
	var wg sync.WaitGroup
	outputs := make([]string, rewriters)
	failures := make([]error, rewriters)
	for w := range rewriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run", "^TestProcessesRewritingTheListingAtOnceMakeTheRewriteOnce$")
			cmd.Env = append(os.Environ(),
				rewriterEnv+"="+strconv.Itoa(w), rewriterDBEnv+"="+path, rewriterGateEnv+"="+gate)
			out, err := cmd.CombinedOutput()
			outputs[w] = string(out)
			if err != nil {
				failures[w] = fmt.Errorf("rewriter %d: %w\n%s", w, err, out)
			}
		}()
	}
	waitForFiles(t, gate, rewriters)
	if err := os.WriteFile(filepath.Join(gate, "go"), nil, 0o600); err != nil {
		t.Fatalf("release the rewriters: %v", err)
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var made int
	for _, out := range outputs {
		made += strings.Count(out, rewriteMade)
	}
	if made != 1 {
		t.Errorf("%d of %d processes made the rewrite, want exactly 1", made, rewriters)
	}

	reopened := openDevice(t, path)
	listed := listedAt(t, reopened)
	for id, want := range carries {
		if got := listed[id]; !got.Equal(want.Time) {
			t.Errorf("memory %s is dated %s, want %s, the time it carries", id, got, want)
			break
		}
	}
	if recorded := recordedRewrite(t, reopened); recorded.Rewritten != memories {
		t.Errorf("the marker records %s, want the one rewrite of all %d rows", counted(recorded), memories)
	}
}

// runRewriter is the child half of the concurrency test: one process opening a
// device, saying it is ready, and rewriting the listing once all of them are.
func runRewriter(t *testing.T) {
	gate := os.Getenv(rewriterGateEnv)
	store, err := local.Open(os.Getenv(rewriterDBEnv))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := os.WriteFile(filepath.Join(gate, "ready-"+os.Getenv(rewriterEnv)), nil, 0o600); err != nil {
		t.Fatalf("say ready: %v", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, err := os.Stat(filepath.Join(gate, "go")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never released")
		}
	}
	rewrite, err := store.RewriteListingDates(listingMarker, timesOnly{})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if rewrite.Ran {
		fmt.Println(rewriteMade)
	}
}

// waitForFiles waits until a directory holds n files, each one a process's word
// that it is ready.
func waitForFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		if len(entries) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d processes said they were ready", len(entries), n)
		}
	}
}

const (
	rewriteMade     = "this process made the rewrite"
	rewriterEnv     = "SENNIT_TEST_REWRITER"
	rewriterDBEnv   = "SENNIT_TEST_REWRITER_DB"
	rewriterGateEnv = "SENNIT_TEST_REWRITER_GATE"
)
