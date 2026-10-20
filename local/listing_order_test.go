package local_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A listing is ordered by the time each record carries, newest first, and a
// record that carries no time comes after every record that does. A page
// boundary anywhere in that order, the records without a time included, neither
// repeats a record nor skips one.
//
// The records are written in an order that has nothing to do with their times,
// as a restore writes them, so an order taken from when each row was written
// would show here.
func TestAListingIsOrderedByTheTimeEachRecordCarriesWithUndatedRecordsLast(t *testing.T) {
	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	start := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	at := func(minutes int) record.Time { return record.At(start.Add(time.Duration(minutes) * time.Minute)) }
	written := []struct {
		name    string
		created record.Time
	}{
		{"middle", at(1)},
		{"first undated", record.Time{}},
		{"oldest", at(0)},
		{"second undated", record.Time{}},
		{"newest", at(2)},
		{"third undated", record.Time{}},
	}
	named := make(map[record.ID]string, len(written))
	created := make(map[string]record.Time, len(written))
	for _, w := range written {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		if err := store.PutRankingMeta(local.RankingMeta{
			ID: id, Kind: record.KindMemory, Type: record.TypeFact, Created: w.created,
		}); err != nil {
			t.Fatalf("store the %s record: %v", w.name, err)
		}
		named[id], created[w.name] = w.name, w.created
	}

	whole, next, err := store.ListRecords(local.RecordQuery{Limit: local.MaxRecordLimit})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if next != "" || len(whole) != len(written) {
		t.Fatalf("one page listed %d of %d records, with a cursor %q", len(whole), len(written), next)
	}
	var listed []string
	for _, row := range whole {
		name := named[row.ID]
		listed = append(listed, name)
		if want := created[name]; !row.Created.Equal(want.Time) || row.Created.IsZero() != want.IsZero() {
			t.Errorf("the %s record is listed as created %s, want %s", name, row.Created, want)
		}
	}
	if got, want := listed[:3], []string{"newest", "middle", "oldest"}; !slices.Equal(got, want) {
		t.Errorf("the dated records are listed %v, want %v, newest first and ahead of the rest", got, want)
	}
	if got, want := slices.Sorted(slices.Values(listed[3:])),
		[]string{"first undated", "second undated", "third undated"}; !slices.Equal(got, want) {
		t.Errorf("after the dated records the listing holds %v, want the three without a time", got)
	}

	for limit := 1; limit <= len(written); limit++ {
		var paged []string
		var cursor local.Cursor
		for pages := 0; ; pages++ {
			if pages > len(written) {
				t.Fatalf("pages of %d never reached the last one, after %v", limit, paged)
			}
			page, next, err := store.ListRecords(local.RecordQuery{Limit: limit, Cursor: cursor})
			if err != nil {
				t.Fatalf("list a page of %d: %v", limit, err)
			}
			for _, row := range page {
				paged = append(paged, named[row.ID])
			}
			if next == "" {
				break
			}
			cursor = next
		}
		if !slices.Equal(paged, listed) {
			t.Errorf("pages of %d list %v, want %v, the order one page gives", limit, paged, listed)
		}
	}
}
