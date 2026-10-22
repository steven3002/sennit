package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// A server whose vault could not date its listing by its records as it opened
// opens all the same, and tells its host's log so in one line, before the line
// that says what is served. The next start, once the cause is gone, dates the
// listing and says only what it serves.
//
// The marker's name is stored on every device, so it is spelled out here.
func TestTheServerTellsItsHostOfAListingItCouldNotDate(t *testing.T) {
	home := t.TempDir()
	if first := announced(t, home); len(first) != 1 || !strings.HasPrefix(first[0], "sennit-mcp: serving ") {
		t.Fatalf("a server over a new vault said %q", first)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "vault.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open the vault's database: %v", err)
	}
	defer db.Close()
	// As a build before records were listed by their own times left the vault,
	// on a device that refuses the write recording the listing dated.
	for _, statement := range []string{
		`DELETE FROM meta WHERE key = 'listingDatedByRecords'`,
		`CREATE TRIGGER refuse_marker BEFORE INSERT ON meta WHEN NEW.key = 'listingDatedByRecords'
		 BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("make the device refuse the rewrite: %v", err)
		}
	}

	lines := announced(t, home)
	if len(lines) != 2 {
		t.Fatalf("the server said %d line(s), want the listing not dated and then what it serves: %q", len(lines), lines)
	}
	for _, says := range []string{
		"sennit-mcp: ", "the listing could not be dated by its records", "tried again at the next open",
		"the disk refused the write",
	} {
		if !strings.Contains(lines[0], says) {
			t.Errorf("the line about the listing does not say %q: %q", says, lines[0])
		}
	}
	if !strings.HasPrefix(lines[1], "sennit-mcp: serving ") {
		t.Errorf("the last line is not the one that says what is served: %q", lines[1])
	}

	if _, err := db.Exec(`DROP TRIGGER refuse_marker`); err != nil {
		t.Fatalf("lift the refusal: %v", err)
	}
	if again := announced(t, home); len(again) != 1 || !strings.HasPrefix(again[0], "sennit-mcp: serving ") {
		t.Errorf("a server over a listing it could date said %q", again)
	}
	var marked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'listingDatedByRecords'`).Scan(&marked); err != nil || marked != 1 {
		t.Errorf("the next start left %d marker(s), want the listing recorded as dated: %v", marked, err)
	}
}
