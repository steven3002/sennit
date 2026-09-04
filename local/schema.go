package local

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationsDir = "migrations"

// baselineVersion is the migration that stands for every schema change made
// before the schema carried a version at all.
const baselineVersion = 1

// A migration is one numbered, reversible step of the on-device schema.
//
// Both directions are SQL files rather than Go string literals so that the
// schema is readable by anything that reads SQL, and so that changing a table is
// a diff against the statement that declares it. Each file is executed as a
// single script inside one transaction, so a step that fails part way leaves the
// database exactly as it found it.
type migration struct {
	version int
	label   string
	up      string
	down    string
}

// migrations is the schema's whole history, in order, read once.
//
// It is a function rather than a variable because a malformed set is an error to
// report at the open that met it, not a panic during package initialisation.
var migrations = sync.OnceValues(loadMigrations)

// migrate brings the database up to the schema version this build expects.
//
// The version lives in PRAGMA user_version, which SQLite stores in the database
// header and carries with the file. That is what makes the two questions this
// scheme has to answer answerable at all: which steps still have to run, and
// whether the file in front of us was written by a build that knew more than
// this one does.
func (s *Store) migrate() error {
	steps, err := migrations()
	if err != nil {
		return err
	}
	latest := steps[len(steps)-1].version

	have, err := s.schemaVersion()
	if err != nil {
		return err
	}
	// A database from a newer build is refused rather than opened. Its tables
	// may carry columns and constraints this build does not write, and the
	// damage from proceeding is silent: reads that quietly miss data and writes
	// that quietly drop it, over the user's only local copy.
	if have > latest {
		return fmt.Errorf(
			"vault schema is version %d and this build understands %d: it was written by a "+
				"newer sennit, and opening it here would read and write it as though the "+
				"difference were not there", have, latest)
	}

	for _, step := range steps {
		if step.version <= have {
			continue
		}
		if err := s.applyUp(step); err != nil {
			return err
		}
	}
	return nil
}

// applyUp runs one migration and stamps the version it reaches, together.
//
// Both happen inside one write transaction so that a schema and the number
// describing it cannot disagree: a crash between them would otherwise leave a
// database that reports a version it does not have.
func (s *Store) applyUp(step migration) error {
	return s.writing(func(ctx context.Context, conn *sql.Conn) error {
		// Re-read under the write lock. Another process opening the same vault
		// may have applied this step between our read and our turn.
		have, err := connSchemaVersion(ctx, conn)
		if err != nil {
			return err
		}
		if step.version <= have {
			return nil
		}
		if _, err := conn.ExecContext(ctx, step.up); err != nil {
			return fmt.Errorf("migration %04d_%s up: %w", step.version, step.label, err)
		}
		if step.version == baselineVersion {
			if err := addPreVersionColumns(ctx, conn); err != nil {
				return err
			}
		}
		return setSchemaVersion(ctx, conn, step.version)
	})
}

// migrateDownTo reverses migrations until the schema is at target.
//
// Nothing in the shipped product calls this, and the CLI offers no downgrade:
// the down direction over the user's only local copy is not something to reach
// for by accident. It exists so that the sequence is reversible in fact rather
// than in principle, and so a test can require each step to undo its own work
// against a real database.
func (s *Store) migrateDownTo(target int) error {
	steps, err := migrations()
	if err != nil {
		return err
	}
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.version <= target {
			continue
		}
		err := s.writing(func(ctx context.Context, conn *sql.Conn) error {
			have, err := connSchemaVersion(ctx, conn)
			if err != nil {
				return err
			}
			if step.version > have {
				return nil
			}
			if _, err := conn.ExecContext(ctx, step.down); err != nil {
				return fmt.Errorf("migration %04d_%s down: %w", step.version, step.label, err)
			}
			return setSchemaVersion(ctx, conn, step.version-1)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// preVersionColumns are columns added to tables that existed before them.
//
// SQLite has no "add column if absent", and a vault written by an earlier build
// is a file on somebody's disk rather than something that can be recreated, so
// the presence of each one is checked before it is added.
//
// These are the tail of the baseline and nothing else. Migration 0001 declares
// both columns with the rest of their tables, so on a database this build
// created they are already present and every check here is a no-op. They exist
// for a database written before the schema carried a version, where 0001's
// CREATE TABLE IF NOT EXISTS is the no-op and the column is genuinely absent.
// A new column belongs in a new numbered migration, never in this list.
var preVersionColumns = []struct{ table, column, definition string }{
	{"record_meta", "kind", "TEXT NOT NULL DEFAULT 'memory'"},
	// A ledger written before this column existed predates hydration filing
	// anything into it, so every row in one was pinned by that installation.
	// The default says so; it is the only reading that does not silently strip
	// an existing vault of the right to reclaim its own storage.
	{"slabs", "origin", "TEXT NOT NULL DEFAULT 'pinned'"},
}

func addPreVersionColumns(ctx context.Context, conn *sql.Conn) error {
	for _, addition := range preVersionColumns {
		present, err := hasColumn(ctx, conn, addition.table, addition.column)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := conn.ExecContext(ctx,
			`ALTER TABLE `+addition.table+` ADD COLUMN `+addition.column+` `+addition.definition); err != nil {
			return fmt.Errorf("add %s.%s: %w", addition.table, addition.column, err)
		}
	}
	return nil
}

// writing runs fn inside a transaction that holds the write lock from the start.
//
// IMMEDIATE rather than the default: a deferred transaction begins as a reader
// and has to upgrade, which SQLite refuses outright if another connection wrote
// in the meantime. busy_timeout does not rescue that: it covers waiting for a
// lock, not being told the read the transaction already took is now stale. Two
// processes opening the same vault at once is the ordinary case here, since
// every MCP client launches its own.
func (s *Store) writing(fn func(context.Context, *sql.Conn) error) error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("take a connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(ctx, conn); err != nil {
		if _, rollbackErr := conn.ExecContext(ctx, `ROLLBACK`); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback: %w", rollbackErr))
		}
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) schemaVersion() (int, error) {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read the schema version: %w", err)
	}
	return version, nil
}

func connSchemaVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read the schema version: %w", err)
	}
	return version, nil
}

// setSchemaVersion writes the version into the database header.
//
// PRAGMA takes no bind parameters, so the number is formatted in. It is an int
// that came from a filename this binary embeds, never from anything a caller
// supplies.
func setSchemaVersion(ctx context.Context, conn *sql.Conn, version int) error {
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return fmt.Errorf("stamp schema version %d: %w", version, err)
	}
	return nil
}

func hasColumn(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("inspect %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// loadMigrations reads the embedded set and puts it in order.
//
// It requires both directions of every step and a run of versions with no gaps,
// because both failures are silent otherwise: a missing down is only discovered
// by the person trying to reverse it, and a gap means a step that some databases
// have applied and others have skipped, with one number covering both.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	found := map[int]*migration{}
	for _, entry := range entries {
		version, label, direction, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(migrationFS, path.Join(migrationsDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		step, ok := found[version]
		if !ok {
			step = &migration{version: version, label: label}
			found[version] = step
		}
		if step.label != label {
			return nil, fmt.Errorf("migration %04d is named both %q and %q", version, step.label, label)
		}
		switch direction {
		case "up":
			step.up = string(body)
		case "down":
			step.down = string(body)
		}
	}

	steps := make([]migration, 0, len(found))
	for _, step := range found {
		steps = append(steps, *step)
	}
	slices.SortFunc(steps, func(a, b migration) int { return a.version - b.version })

	if len(steps) == 0 {
		return nil, errors.New("no migrations are embedded")
	}
	for i, step := range steps {
		if step.version != i+1 {
			return nil, fmt.Errorf("migrations skip a version: expected %04d, found %04d", i+1, step.version)
		}
		if strings.TrimSpace(step.up) == "" {
			return nil, fmt.Errorf("migration %04d_%s has no up direction", step.version, step.label)
		}
		if strings.TrimSpace(step.down) == "" {
			return nil, fmt.Errorf("migration %04d_%s has no down direction", step.version, step.label)
		}
	}
	return steps, nil
}

// parseMigrationName reads NNNN_label.up.sql, the one shape this directory takes.
func parseMigrationName(file string) (version int, label, direction string, err error) {
	rest, ok := strings.CutSuffix(file, ".sql")
	if !ok {
		return 0, "", "", fmt.Errorf("migration %q is not a .sql file", file)
	}
	rest, direction, ok = cutLast(rest, ".")
	if !ok || (direction != "up" && direction != "down") {
		return 0, "", "", fmt.Errorf("migration %q does not name a direction (.up.sql or .down.sql)", file)
	}
	number, label, ok := strings.Cut(rest, "_")
	if !ok || label == "" {
		return 0, "", "", fmt.Errorf("migration %q is not named NNNN_label.%s.sql", file, direction)
	}
	version, err = strconv.Atoi(number)
	if err != nil || version < 1 {
		return 0, "", "", fmt.Errorf("migration %q does not start with a version number", file)
	}
	return version, label, direction, nil
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}
