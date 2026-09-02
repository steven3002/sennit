package local

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The migration set is the schema's history, and two ways of getting it wrong
// are invisible until somebody is relying on the part that is missing: a step
// with no down is only found by whoever tries to reverse it, and a gap in the
// numbering means one version covers two different schemas.
func TestTheMigrationSetIsWellFormed(t *testing.T) {
	steps, err := migrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("no migrations are embedded")
	}
	for i, step := range steps {
		if step.version != i+1 {
			t.Errorf("migration %d is numbered %04d", i+1, step.version)
		}
		if strings.TrimSpace(step.up) == "" {
			t.Errorf("migration %04d_%s has no up", step.version, step.label)
		}
		if strings.TrimSpace(step.down) == "" {
			t.Errorf("migration %04d_%s has no down", step.version, step.label)
		}
	}
}

// A vault this build creates carries the version it was created at. Without the
// stamp nothing downstream can tell a fresh database from one written before
// versioning existed, and the two need different treatment.
func TestAFreshVaultIsStampedWithTheSchemaVersion(t *testing.T) {
	store := openTemp(t)

	steps, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	latest := steps[len(steps)-1].version

	version, err := store.schemaVersion()
	if err != nil {
		t.Fatalf("read the version: %v", err)
	}
	if version != latest {
		t.Fatalf("a fresh vault is at schema version %d, want %d", version, latest)
	}
}

// A database written before the schema carried a version is a file on somebody's
// disk. It has to be recognised as already holding the baseline rather than
// replayed from nothing, and then stamped so the question is not asked again.
func TestAVaultWrittenBeforeVersioningIsBaselined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	// The shape record_meta had before ranking needed a kind, and before the
	// database recorded a version at all.
	if _, err := old.Exec(`CREATE TABLE record_meta (
		record_id  TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		supersedes TEXT,
		created_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the old table: %v", err)
	}
	var stamped int
	if err := old.QueryRow(`PRAGMA user_version`).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped != 0 {
		t.Fatalf("the fixture is already stamped at %d", stamped)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open the old vault: %v", err)
	}
	defer store.Close()

	version, err := store.schemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != baselineVersion {
		t.Fatalf("a pre-versioning vault came out at %d, want the baseline %d", version, baselineVersion)
	}
	// The baseline's tail: the column the old table never had.
	assertColumn(t, store, "record_meta", "kind", true)
}

// The failure this scheme exists to prevent. A database written by a build that
// knows more than this one must be refused, because the damage from proceeding
// is silent: reads that miss what they cannot see and writes that drop it, over
// the user's only local copy.
func TestADatabaseFromANewerBuildIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")

	store := openAt(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatalf("stamp a future version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err == nil {
		reopened.Close()
		t.Fatal("a vault from a newer build opened without complaint")
	}
	// The message has to name both numbers: "upgrade" is only actionable if the
	// user can see which way the gap runs.
	for _, want := range []string{"99", "newer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A down direction that is never run is a claim, not a mechanism. This steps the
// whole sequence back to nothing and forward again, against a real database.
func TestEveryMigrationReversesItsOwnWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reversible.db")
	store := openAt(t, path)
	defer store.Close()

	assertTable(t, store, "record_meta", true)
	assertTable(t, store, "session_heads", true)

	if err := store.migrateDownTo(0); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	version, err := store.schemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("after stepping down the version is %d, want 0", version)
	}
	assertTable(t, store, "record_meta", false)
	assertTable(t, store, "session_heads", false)
	// fts5 carries shadow tables; dropping the virtual table must take them too.
	assertTable(t, store, "record_lexical", false)
	assertTable(t, store, "record_lexical_data", false)

	if err := store.migrate(); err != nil {
		t.Fatalf("migrate back up: %v", err)
	}
	assertTable(t, store, "record_meta", true)
	assertTable(t, store, "session_heads", true)
	assertColumn(t, store, "slabs", "origin", true)
}

// Every MCP client launches its own process against the same vault, so two
// opens racing to migrate a fresh database is the ordinary case rather than a
// hardening scenario. Both must succeed and the version must be stamped once.
func TestConcurrentOpensMigrateExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raced.db")

	const openers = 4
	var wait sync.WaitGroup
	errs := make([]error, openers)
	stores := make([]*Store, openers)
	wait.Add(openers)
	for i := range openers {
		go func() {
			defer wait.Done()
			stores[i], errs[i] = Open(path)
		}()
	}
	wait.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d failed: %v", i, err)
			continue
		}
		defer stores[i].Close()
	}
	if t.Failed() {
		return
	}

	steps, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	version, err := stores[0].schemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if want := steps[len(steps)-1].version; version != want {
		t.Fatalf("after %d concurrent opens the version is %d, want %d", openers, version, want)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "vault.db"))
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func assertTable(t *testing.T, store *Store, table string, want bool) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("look for %s: %v", table, err)
	}
	if got := count > 0; got != want {
		t.Errorf("table %s present = %v, want %v", table, got, want)
	}
}

func assertColumn(t *testing.T, store *Store, table, column string, want bool) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(
		`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
		t.Fatalf("inspect %s: %v", table, err)
	}
	if got := count > 0; got != want {
		t.Errorf("column %s.%s present = %v, want %v", table, column, got, want)
	}
}
