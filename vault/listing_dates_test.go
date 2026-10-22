package vault

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A restoredRecord is one record a device restored: the time the copy it holds
// carries, and the time a build before records were listed by their own times
// gave its listing row when the restore reached it.
type restoredRecord struct {
	name     string
	id       record.ID
	kind     record.Kind
	carries  record.Time
	restored record.Time
}

// deviceDB opens a device's database through a connection of the test's own,
// the way an earlier build or a second process reaches the same file.
func deviceDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, dbFile)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open the device's database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// asAnEarlierBuildLeftIt makes a device's database what a build before records
// were listed by their own times left after restoring them: every listing row
// dated by when the restore reached its record, which is later than anything
// was stored, and nothing recorded about dating the listing from the records.
// The restore is taken to have reached the records in the reverse of the order
// they are given in, as a hydrate walking the account's objects may.
func asAnEarlierBuildLeftIt(t *testing.T, home string, records []restoredRecord) {
	t.Helper()
	db := deviceDB(t, home)
	restoredAt := record.Now()
	for i := range records {
		r := &records[len(records)-1-i]
		r.restored = record.At(restoredAt.Add(time.Duration(i+1) * time.Second))
		if _, err := db.Exec(`UPDATE record_meta SET created_at = ? WHERE record_id = ?`,
			r.restored.String(), r.id.String()); err != nil {
			t.Fatalf("date the %s row by the restore: %v", r.name, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM meta WHERE key = ?`, listingDatesKey); err != nil {
		t.Fatalf("take the marker away: %v", err)
	}
}

// restoredByAnEarlierBuild has a writer store memories and conversations a few
// milliseconds apart, restores them onto a fresh device in the reverse of the
// order they were stored, and leaves that device closed and as a build before
// records were listed by their own times left it. It returns the device's home
// and the records in the order they were stored.
//
// Each record carries the time a restore brings back with it. A memory carries
// its own creation time. A conversation's head stays on the device that saved
// it, so the device rebuilds one, dated by the first of its turns that records
// when it was said, and one whose turns record none carries no time at all.
func restoredByAnEarlierBuild(t *testing.T) (string, []restoredRecord) {
	t.Helper()
	ctx := context.Background()
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()

	var records []restoredRecord
	memory := func(name string) {
		t.Helper()
		// Times are kept to the millisecond, so records stored inside one
		// would tie.
		time.Sleep(3 * time.Millisecond)
		result, err := writer.Remember(ctx, RememberRequest{
			Statement: "The " + name + " gauge at Tidepool reports every hour.",
			Context:   "Stored by the listing tests, one gauge at a time.",
			Type:      record.TypeFact,
			Tags:      []string{"gauge"},
		})
		if err != nil {
			t.Fatalf("remember the %s gauge: %v", name, err)
		}
		records = append(records, restoredRecord{name: name, id: result.ID, kind: record.KindMemory, carries: result.Stored})
	}
	talk := func(name string, dated bool) {
		t.Helper()
		time.Sleep(3 * time.Millisecond)
		turns := exchange(name, dated)
		saved, err := writer.SaveSession(ctx, SaveSessionRequest{Title: "The " + name + " conversation", Messages: turns})
		if err != nil {
			t.Fatalf("save the %s conversation: %v", name, err)
		}
		records = append(records, restoredRecord{name: name, id: saved.ID, kind: record.KindSession, carries: turns[0].Created})
	}
	memory("harbour")
	talk("survey", true)
	talk("undated", false)
	memory("river")
	memory("estuary")

	network := FlushToNetworkForTest(t, writer)
	slices.Reverse(network)
	home := t.TempDir()
	device := ledgerVault(t, home)
	RestoreForTest(t, device, network, HydrateMetadata)
	for _, r := range records {
		if r.kind != record.KindSession {
			continue
		}
		head, err := device.Session(r.id)
		if err != nil {
			t.Fatalf("read the rebuilt head of the %s conversation: %v", r.name, err)
		}
		if !head.Created.Equal(r.carries.Time) || head.Created.IsZero() != r.carries.IsZero() {
			t.Fatalf("the %s conversation's rebuilt head is dated %s, want %s, so this checks nothing",
				r.name, head.Created, r.carries)
		}
	}
	if err := device.Close(); err != nil {
		t.Fatalf("close the device: %v", err)
	}
	asAnEarlierBuildLeftIt(t, home, records)
	return home, records
}

// listingOf lists everything a device holds, every page of it.
func listingOf(t *testing.T, v *Vault) []BrowseRow {
	t.Helper()
	var rows []BrowseRow
	var cursor local.Cursor
	for {
		page, err := v.Browse(BrowseRequest{Limit: local.MaxRecordLimit, Cursor: cursor})
		if err != nil {
			t.Fatalf("browse: %v", err)
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == "" {
			return rows
		}
		cursor = page.NextCursor
	}
}

// rewriteRecorded reads what the device recorded of dating its listing.
func rewriteRecorded(t *testing.T, v *Vault) local.ListingRewrite {
	t.Helper()
	value, err := v.local.GetMeta(listingDatesKey)
	if err != nil {
		t.Fatalf("the device recorded nothing of dating its listing: %v", err)
	}
	var rewrite local.ListingRewrite
	if err := json.Unmarshal([]byte(value), &rewrite); err != nil {
		t.Fatalf("the device recorded %q, which is not a rewrite: %v", value, err)
	}
	return rewrite
}

// datedAs checks that a listing dates each record as want says. It names the
// first few rows dated otherwise and counts the rest.
func datedAs(t *testing.T, rows []BrowseRow, records []restoredRecord, want func(restoredRecord) record.Time) {
	t.Helper()
	if len(rows) != len(records) {
		t.Fatalf("the device lists %d record(s), want the %d it restored", len(rows), len(records))
	}
	const named = 5
	var wrong int
	for _, row := range rows {
		i := slices.IndexFunc(records, func(r restoredRecord) bool { return r.id == row.ID })
		if i < 0 {
			t.Fatalf("the device lists %s, which nobody stored", row.ID)
		}
		r := records[i]
		if w := want(r); !row.Created.Equal(w.Time) || row.Created.IsZero() != w.IsZero() {
			if wrong++; wrong <= named {
				t.Errorf("the %s %s is listed as created %s, want %s", r.name, r.kind, row.Created, w)
			}
		}
	}
	if wrong > named {
		t.Errorf("and %d more of the %d rows are dated otherwise", wrong-named, len(rows))
	}
}

func ownTime(r restoredRecord) record.Time { return r.carries }

// A device restored by a build that dated each listing row by when the restore
// reached its record lists every record by the time the record itself carries
// once a build that lists records that way opens it, and newest first by those
// times.
//
// Before, such a device kept the restore's dates for good: a row keeps the
// first time written for it, so it listed what it restored then in the order
// the restore walked the account, every date the restore's. A conversation whose
// turns record no time ends up with none, and is listed after every record that
// has one. The device records that the rewrite is made, and what it did.
func TestADeviceRestoredByAnEarlierBuildListsEachRecordByItsOwnTimeOnceItIsOpened(t *testing.T) {
	home, records := restoredByAnEarlierBuild(t)

	device := ledgerVault(t, home)
	defer device.Close()

	rows := listingOf(t, device)
	datedAs(t, rows, records, ownTime)

	newestFirst := slices.Clone(records)
	slices.SortStableFunc(newestFirst, func(a, b restoredRecord) int {
		if c := b.carries.Compare(a.carries.Time); c != 0 {
			return c
		}
		return bytes.Compare(a.id[:], b.id[:])
	})
	var listed, ordered []string
	for i := range rows {
		listed = append(listed, records[slices.IndexFunc(records, func(r restoredRecord) bool { return r.id == rows[i].ID })].name)
		ordered = append(ordered, newestFirst[i].name)
	}
	if !slices.Equal(listed, ordered) {
		t.Errorf("the device lists %v, want %v, newest first by the time each record carries", listed, ordered)
	}
	if last := rows[len(rows)-1]; listed[len(listed)-1] != "undated" || !last.Created.IsZero() {
		t.Errorf("the device lists %s last, created %s, want the undated conversation, with no time",
			listed[len(listed)-1], last.Created)
	}

	rewrite := rewriteRecorded(t, device)
	made := local.ListingRewrite{Rows: len(records), Rewritten: len(records)}
	if counted(rewrite) != counted(made) || rewrite.At.IsZero() {
		t.Errorf("the device recorded %s, made at %s; want %s and when it was made",
			counted(rewrite), rewrite.At, counted(made))
	}
}

// counted says in words what a rewrite counted, so that two can be compared
// and either one read in a failure.
func counted(r local.ListingRewrite) string {
	return fmt.Sprintf("%d rows read, %d rewritten, %d not held, %d unreadable",
		r.Rows, r.Rewritten, r.NotHeld, r.Unreadable)
}

// The listing is dated once. A later open reads that it was and leaves every
// row as it finds it, so a row that changed after the first open stays as it
// was changed, and what the device recorded of the rewrite is what the first
// open made.
func TestALaterOpenLeavesTheListingAsItFindsIt(t *testing.T) {
	home, records := restoredByAnEarlierBuild(t)

	first := ledgerVault(t, home)
	marker, err := first.local.GetMeta(listingDatesKey)
	if err != nil {
		t.Fatalf("the first open recorded nothing of dating the listing: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	changed := records[0]
	later := record.At(time.Date(2031, time.January, 1, 0, 0, 0, 0, time.UTC))
	if _, err := deviceDB(t, home).Exec(`UPDATE record_meta SET created_at = ? WHERE record_id = ?`,
		later.String(), changed.id.String()); err != nil {
		t.Fatalf("change the %s row: %v", changed.name, err)
	}

	second := ledgerVault(t, home)
	defer second.Close()
	datedAs(t, listingOf(t, second), records, func(r restoredRecord) record.Time {
		if r.id == changed.id {
			return later
		}
		return r.carries
	})
	if again, err := second.local.GetMeta(listingDatesKey); err != nil || again != marker {
		t.Errorf("after the second open the device records %q (%v), want %q, what the first open recorded",
			again, err, marker)
	}
}

// A row whose record this device does not hold, a memory whose body is gone or
// a conversation whose head is, keeps the date it had, and the device records
// how many such rows it left.
func TestARowWhoseRecordThisDeviceDoesNotHoldKeepsItsDateAndIsCounted(t *testing.T) {
	home, records := restoredByAnEarlierBuild(t)
	byName := func(name string) restoredRecord {
		return records[slices.IndexFunc(records, func(r restoredRecord) bool { return r.name == name })]
	}
	river, survey := byName("river"), byName("survey")
	db := deviceDB(t, home)
	if _, err := db.Exec(`DELETE FROM bodies WHERE record_id = ?`, river.id.String()); err != nil {
		t.Fatalf("drop the river memory's body: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM session_heads WHERE session_id = ?`, survey.id.String()); err != nil {
		t.Fatalf("drop the survey conversation's head: %v", err)
	}

	device := ledgerVault(t, home)
	defer device.Close()
	datedAs(t, listingOf(t, device), records, func(r restoredRecord) record.Time {
		if r.id == river.id || r.id == survey.id {
			return r.restored
		}
		return r.carries
	})
	want := local.ListingRewrite{Rows: len(records), Rewritten: len(records) - 2, NotHeld: 2}
	if rewrite := rewriteRecorded(t, device); counted(rewrite) != counted(want) {
		t.Errorf("the device recorded %s, want %s", counted(rewrite), counted(want))
	}
}

// An open whose rewrite stops part way opens all the same, and carries why the
// listing was not dated, for whoever opened the vault to say. It changes
// nothing: every row keeps the date it had, a conversation a release rebuilt
// keeps the dates that release gave it, and nothing records the rewrite as
// made. The next open, once the cause is gone, makes all of it. It is stopped
// here at its last step, recording that it is made, after every row and head
// has been given its new dates.
func TestAnOpenWhoseRewriteStopsPartWayOpensAnywayChangesNothingAndTheNextMakesAllOfIt(t *testing.T) {
	home, records := restoredByAnEarlierBuild(t)
	undated := records[slices.IndexFunc(records, func(r restoredRecord) bool { return r.name == "undated" })]
	rebuiltAt := undated.restored
	datedByTheRebuild(t, home, undated.id, rebuiltAt, rebuiltAt)
	db := deviceDB(t, home)
	if _, err := db.Exec(`CREATE TRIGGER refuse_marker BEFORE INSERT ON meta WHEN NEW.key = '` +
		listingDatesKey + `' BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`); err != nil {
		t.Fatalf("make the device refuse the marker: %v", err)
	}

	stopped, err := Open(context.Background(), Options{
		Home: home, Phrase: ledgerPhrase, Embedder: embedtest.NewStub(), Offline: true,
	})
	if err != nil {
		t.Fatalf("the vault did not open when its rewrite was stopped: %v", err)
	}
	reason := stopped.ListingNotDatedBecause()
	if reason == nil || !strings.Contains(reason.Error(), "the disk refused the write") {
		t.Errorf("the open whose rewrite was stopped says the listing was not dated because of %v", reason)
	}
	for _, r := range records {
		var created string
		if err := db.QueryRow(`SELECT created_at FROM record_meta WHERE record_id = ?`, r.id.String()).Scan(&created); err != nil {
			t.Fatalf("read the %s row: %v", r.name, err)
		}
		if created != r.restored.String() {
			t.Errorf("the stopped rewrite left the %s row dated %s, want %s, the date it had", r.name, created, r.restored)
		}
	}
	if head := headOn(t, home, undated.id); head.Version != 1 || !head.Created.Equal(rebuiltAt.Time) ||
		!head.Updated.Equal(rebuiltAt.Time) {
		t.Errorf("the stopped rewrite left the rebuilt head at version %d, created %s, updated %s; "+
			"want it as the release left it, at version 1 and dated %s", head.Version, head.Created, head.Updated, rebuiltAt)
	}
	var marked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = ?`, listingDatesKey).Scan(&marked); err != nil {
		t.Fatalf("look for the marker: %v", err)
	}
	if marked != 0 {
		t.Error("the stopped rewrite was recorded as made")
	}
	if err := stopped.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := db.Exec(`DROP TRIGGER refuse_marker`); err != nil {
		t.Fatalf("lift the refusal: %v", err)
	}
	device := ledgerVault(t, home)
	defer device.Close()
	if reason := device.ListingNotDatedBecause(); reason != nil {
		t.Errorf("the next open says the listing was not dated because of %v", reason)
	}
	datedAs(t, listingOf(t, device), records, ownTime)
	if head := headOn(t, home, undated.id); head.Version != 2 || !head.Created.IsZero() || !head.Updated.IsZero() {
		t.Errorf("the rebuilt head is at version %d, created %s, updated %s; want version 2 and no dates",
			head.Version, head.Created, head.Updated)
	}
	if rewrite := rewriteRecorded(t, device); rewrite.Rewritten != len(records) || rewrite.Undated != 1 {
		t.Errorf("the next open recorded %s and %d head(s) undated, want every one of the %d rows rewritten "+
			"and the 1 head undated", counted(rewrite), rewrite.Undated, len(records))
	}
}

// Two processes that open a restored device at the same moment, as a running
// server and a command run beside it do, both open it, neither has a listing it
// could not date to report, and the listing is dated once between them. They are
// held until both are ready, so each reaches the rewrite before either has
// recorded it, and the device holds enough rows that one rewrite is still under
// way when the other process comes to it.
func TestTwoProcessesOpeningARestoredDeviceAtOnceBothOpenItAndDateItsListingOnce(t *testing.T) {
	if home := os.Getenv(openerHomeEnv); home != "" {
		openWhenReleased(t, home)
		return
	}
	const (
		openers  = 2
		memories = 300
	)
	home := t.TempDir()
	device := ledgerVault(t, home)
	stored := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	records := make([]restoredRecord, 0, memories)
	for i := range memories {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		created := record.At(stored.Add(time.Duration(i) * time.Minute))
		memory := &record.Memory{
			ID: id, Kind: record.KindMemory, Type: record.TypeFact, SchemaVersion: record.SchemaVersion,
			Version: 1, Statement: fmt.Sprintf("Tidepool station %d reports every hour.", i),
			Context: "Restored by the listing tests.", CreatedAt: created, UpdatedAt: created,
		}
		body, err := record.Marshal(memory)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := device.local.PutBody(id, record.KindMemory, body); err != nil {
			t.Fatalf("store the body: %v", err)
		}
		if err := device.local.PutRankingMeta(rankingMeta(memory)); err != nil {
			t.Fatalf("list the memory: %v", err)
		}
		records = append(records, restoredRecord{
			name: fmt.Sprintf("station %d", i), id: id, kind: record.KindMemory, carries: created,
		})
	}
	if err := device.Close(); err != nil {
		t.Fatalf("close the device: %v", err)
	}
	asAnEarlierBuildLeftIt(t, home, records)

	gate := t.TempDir()
	var wg sync.WaitGroup
	failures := make([]error, openers)
	for o := range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run",
				"^TestTwoProcessesOpeningARestoredDeviceAtOnceBothOpenItAndDateItsListingOnce$")
			cmd.Env = append(os.Environ(),
				openerHomeEnv+"="+home, openerGateEnv+"="+gate, openerEnv+"="+strconv.Itoa(o))
			if out, err := cmd.CombinedOutput(); err != nil {
				failures[o] = fmt.Errorf("opener %d: %w\n%s", o, err, out)
			}
		}()
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
		ready, err := os.ReadDir(gate)
		if err != nil {
			t.Fatalf("read the gate: %v", err)
		}
		if len(ready) >= openers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d processes said they were ready", len(ready), openers)
		}
	}
	if err := os.WriteFile(filepath.Join(gate, "go"), nil, 0o600); err != nil {
		t.Fatalf("release the openers: %v", err)
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}

	reopened := ledgerVault(t, home)
	defer reopened.Close()
	datedAs(t, listingOf(t, reopened), records, ownTime)
	if rewrite := rewriteRecorded(t, reopened); rewrite.Rewritten != memories {
		t.Errorf("the device recorded %s, want the one rewrite of all %d rows", counted(rewrite), memories)
	}
}

// openWhenReleased is the child half of the concurrency test: one process that
// says it is ready, waits until every process is, and then opens the vault.
func openWhenReleased(t *testing.T, home string) {
	gate := os.Getenv(openerGateEnv)
	if err := os.WriteFile(filepath.Join(gate, "ready-"+os.Getenv(openerEnv)), nil, 0o600); err != nil {
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
	v := ledgerVault(t, home)
	// The process that comes second finds the rewrite made, so neither has a
	// listing it could not date to report.
	if reason := v.ListingNotDatedBecause(); reason != nil {
		t.Errorf("this process says the listing was not dated because of %v", reason)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

const (
	openerEnv     = "SENNIT_TEST_OPENER"
	openerHomeEnv = "SENNIT_TEST_OPENER_HOME"
	openerGateEnv = "SENNIT_TEST_OPENER_GATE"
)
