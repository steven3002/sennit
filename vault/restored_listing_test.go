package vault_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// A storedRecord is one record a writer stored, with the time the record itself
// carries: a memory's own creation time, and for a conversation the first of
// its turns that records when it was said, which is what a device that rebuilds
// the conversation's head dates it by. A conversation whose turns record no time
// carries none.
type storedRecord struct {
	name string
	id   record.ID
	kind record.Kind
	at   record.Time
}

// restoredInReverse has a writer store memories and conversations a few
// milliseconds apart, puts them on the network the test stands in for, and
// restores them onto a fresh device in the reverse of the order they were
// stored, as a hydrate walking the account's objects may. It returns both
// devices and the records in the order they were stored.
func restoredInReverse(t *testing.T) (writer, device *vault.Vault, stored []storedRecord) {
	t.Helper()
	ctx := context.Background()
	writer = vault.OpenWithLedgerForTest(t, t.TempDir())

	// Times are kept to the millisecond, so records stored inside one would
	// tie. Each is stored a few milliseconds after the one before it.
	apart := func() { time.Sleep(3 * time.Millisecond) }
	memory := func(name string) {
		t.Helper()
		apart()
		result, err := writer.Remember(ctx, vault.RememberRequest{
			Statement: "The " + name + " gauge at Tidepool reports every hour.",
			Context:   "Stored by the listing tests, one gauge at a time.",
			Type:      record.TypeFact,
			Tags:      []string{"gauge"},
		})
		if err != nil {
			t.Fatalf("remember the %s gauge: %v", name, err)
		}
		stored = append(stored, storedRecord{name: name, id: result.ID, kind: record.KindMemory, at: result.Stored})
	}
	talk := func(name string, dated bool) {
		t.Helper()
		apart()
		turns := conversation(strings.ReplaceAll(name, " ", "-"))
		if !dated {
			for i := range turns {
				turns[i].Created = record.Time{}
			}
		}
		saved, err := writer.SaveSession(ctx, vault.SaveSessionRequest{
			Title:    "The " + name + " conversation",
			Messages: turns,
		})
		if err != nil {
			t.Fatalf("save the %s conversation: %v", name, err)
		}
		var at record.Time
		if dated {
			at = turns[0].Created
		}
		stored = append(stored, storedRecord{name: name, id: saved.ID, kind: record.KindSession, at: at})
	}

	memory("harbour")
	talk("survey", true)
	talk("first undated", false)
	memory("river")
	talk("second undated", false)
	talk("third undated", false)
	memory("estuary")
	// A second device restores later than anything was stored, so a date the
	// restore gave a record is told apart from the record's own.
	apart()

	network := vault.FlushToNetworkForTest(t, writer)
	slices.Reverse(network)
	device = vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, device, network, vault.HydrateIndex)
	return writer, device, stored
}

// browseEverything lists everything a device holds on one page.
func browseEverything(t *testing.T, v *vault.Vault) []vault.BrowseRow {
	t.Helper()
	page, err := v.Browse(vault.BrowseRequest{Limit: local.MaxRecordLimit})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if page.NextCursor != "" {
		t.Fatalf("one page of %d did not hold the %d record(s) listed", local.MaxRecordLimit, len(page.Rows))
	}
	return page.Rows
}

// namesListed names each row by the record it lists.
func namesListed(rows []vault.BrowseRow, stored []storedRecord) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		name := "a record nobody stored, " + row.ID.String()
		for _, r := range stored {
			if r.id == row.ID {
				name = r.name
			}
		}
		out = append(out, name)
	}
	return out
}

// A device that restored the vault lists its records in the order they were
// stored, and dates each by the time the record itself carries, whatever order
// the restore came across them in.
//
// A hydrate restores records in the order it walks the account's objects, which
// is not the order they were stored in. Listed and dated by when this device
// wrote each record's metadata, a restored vault came out in that walk order,
// every date the restore's. Here the network hands the records back in the
// reverse of the order they were stored. A memory carries its own creation time
// and comes back with it. A conversation's head stays on the device that saved
// it, so here it is dated as its rebuilt head is, by the first of its turns that
// records a time, and one whose turns record none has no date to list it by and
// comes after every record that has one, as it does when a resume lists it.
func TestARestoredDeviceListsRecordsInTheOrderTheyWereStoredWithTheirOwnDates(t *testing.T) {
	writer, device, stored := restoredInReverse(t)

	var newestFirst, dated, undated []string
	for i := len(stored) - 1; i >= 0; i-- {
		newestFirst = append(newestFirst, stored[i].name)
		if stored[i].at.IsZero() {
			undated = append(undated, stored[i].name)
		} else {
			dated = append(dated, stored[i].name)
		}
	}
	if got := namesListed(browseEverything(t, writer), stored); !slices.Equal(got, newestFirst) {
		t.Errorf("the device that stored them lists %v, want %v, the last one stored first", got, newestFirst)
	}

	rows := browseEverything(t, device)
	got := namesListed(rows, stored)
	if len(got) != len(stored) {
		t.Fatalf("the device that restored them lists %v, want all %d", got, len(stored))
	}
	if !slices.Equal(got[:len(dated)], dated) {
		t.Errorf("the device that restored them lists %v, want %v first, in the order they were stored, "+
			"newest first", got, dated)
	}
	if rest, want := slices.Sorted(slices.Values(got[len(dated):])), slices.Sorted(slices.Values(undated)); !slices.Equal(rest, want) {
		t.Errorf("the device that restored them lists %v, want %v after every record that has a date", got, want)
	}

	for _, row := range rows {
		i := slices.IndexFunc(stored, func(r storedRecord) bool { return r.id == row.ID })
		if i < 0 {
			continue
		}
		r := stored[i]
		if !row.Created.Equal(r.at.Time) || row.Created.IsZero() != r.at.IsZero() {
			t.Errorf("the %s %s is listed as created %s, want %s, the time the record carries",
				r.name, r.kind, row.Created, r.at)
		}
		if r.kind != record.KindSession {
			continue
		}
		head, err := device.Session(r.id)
		if err != nil {
			t.Fatalf("read the rebuilt head of the %s conversation: %v", r.name, err)
		}
		if !row.Created.Equal(head.Created.Time) || row.Created.IsZero() != head.Created.IsZero() {
			t.Errorf("the %s conversation is listed as created %s, and its rebuilt head says %s",
				r.name, row.Created, head.Created)
		}
	}
}

// Paging through a restored device's listing, a page boundary among the records
// that carry no time, or between them and the dated ones, neither repeats a
// record nor skips one, at every page size.
//
// A record without a time is listed by the zero time, and the cursor names the
// last row a page showed by its time and its id. A cursor that could not name a
// row without a time would start the next page again from the newest record.
func TestAPageBoundaryAmongRecordsWithoutATimeNeitherRepeatsNorSkipsOne(t *testing.T) {
	_, device, stored := restoredInReverse(t)
	whole := namesListed(browseEverything(t, device), stored)

	for limit := 1; limit <= len(stored); limit++ {
		var paged []vault.BrowseRow
		var cursor local.Cursor
		for pages := 0; ; pages++ {
			if pages > len(stored) {
				t.Fatalf("pages of %d never reached the last one, after %v", limit, namesListed(paged, stored))
			}
			page, err := device.Browse(vault.BrowseRequest{Limit: limit, Cursor: cursor})
			if err != nil {
				t.Fatalf("browse a page of %d: %v", limit, err)
			}
			paged = append(paged, page.Rows...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if got := namesListed(paged, stored); !slices.Equal(got, whole) {
			t.Errorf("pages of %d list %v, want %v, the order one page gives", limit, got, whole)
		}
	}
}

// Through the protocol, a restored device dates each record in a listing as
// `recall` and `open` date it, and leaves the date out of a record that has
// none, in the structured result and in the text a model reads, rather than
// giving the first day of year one.
func TestBrowseOnARestoredDeviceDatesEachRecordAsRecallAndOpenDo(t *testing.T) {
	_, device, stored := restoredInReverse(t)
	session := serveDevice(t, device)

	var listed struct {
		Rows []map[string]any `json:"rows"`
	}
	text := callTool(t, session, "browse", mcp.BrowseIn{Limit: local.MaxRecordLimit}, &listed)
	if len(listed.Rows) != len(stored) {
		t.Fatalf("browse listed %d row(s), want the %d restored", len(listed.Rows), len(stored))
	}
	listedAs := make(map[string]string, len(listed.Rows))
	for _, row := range listed.Rows {
		uri, _ := row["uri"].(string)
		created, dated := row["created"].(string)
		if _, present := row["created"]; present && (!dated || created == "") {
			t.Errorf("browse gives %s a created of %v, which is not a time", uri, row["created"])
		}
		listedAs[uri] = created
	}

	var undated int
	for _, r := range stored {
		uri := mcp.URI(r.kind, r.id)
		want := ""
		if r.at.IsZero() {
			undated++
		} else {
			want = r.at.String()
		}
		if got := listedAs[uri]; got != want {
			t.Errorf("browse dates the %s %s %q, want %q", r.name, r.kind, got, want)
		}
		var opened struct {
			Detail map[string]any `json:"detail"`
		}
		callTool(t, session, "open", mcp.OpenIn{URI: uri}, &opened)
		if got, _ := opened.Detail["created"].(string); got != listedAs[uri] {
			t.Errorf("open dates the %s %s %q, and browse %q", r.name, r.kind, got, listedAs[uri])
		}
	}

	var found struct {
		Results []map[string]any `json:"results"`
	}
	callTool(t, session, "recall", mcp.RecallIn{
		Query: "how many tide stations and gauges at Tidepool report every hour",
		Limit: len(stored),
	}, &found)
	if len(found.Results) == 0 {
		t.Fatal("recall found nothing on the device that restored the records")
	}
	for _, hit := range found.Results {
		uri, _ := hit["uri"].(string)
		if got, _ := hit["created"].(string); got != listedAs[uri] {
			t.Errorf("recall dates %s %q, and browse %q", uri, got, listedAs[uri])
		}
	}

	if strings.Contains(text, "0001-01-01") {
		t.Errorf("the listing a model reads puts a record in year one:\n%s", text)
	}
	if got := strings.Count(text, "date not known"); got != undated {
		t.Errorf("the listing a model reads marks %d record(s) as having no known date, want %d:\n%s",
			got, undated, text)
	}
}

// slowEmbedder is the stub embedder, made slow enough that a write which embeds
// begins and ends in different milliseconds, as every write does with a model.
type slowEmbedder struct{ embed.Vectorizer }

func (s slowEmbedder) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	time.Sleep(5 * time.Millisecond)
	return s.Vectorizer.EmbedOne(ctx, text)
}

// The device that stored a record lists it with the time the record carries: a
// memory with the time `remember` said it was stored, and a conversation with
// the time its record was first saved, which are the times `recall` and `open`
// give.
//
// A listing dated each record by when this device wrote the metadata that ranks
// it, which is after the write has embedded the record. With a model that is a
// fraction of a second, so the listing disagreed with the record by that much,
// and two writes made at once were listed in the order they finished rather than
// the order they were made in.
func TestTheDeviceThatStoredARecordListsItWithTheTimeTheRecordCarries(t *testing.T) {
	ctx := context.Background()
	v := openVault(t, t.TempDir(), slowEmbedder{embedtest.NewStub()})

	stored, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "The harbour gauge at Tidepool reports every hour.",
		Context:   "Stored by the listing tests on the device that writes it.",
		Type:      record.TypeFact,
		Tags:      []string{"gauge"},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "The harbour gauge conversation",
		Messages: conversation("harbour"),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	head, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the head: %v", err)
	}
	carried := map[record.ID]record.Time{stored.ID: stored.Stored, saved.ID: head.Created}

	rows := browseEverything(t, v)
	if len(rows) != 2 {
		t.Fatalf("browse listed %d record(s), want the memory and the conversation", len(rows))
	}
	listedAs := make(map[record.ID]record.Time, len(rows))
	for _, row := range rows {
		listedAs[row.ID] = row.Created
		if want := carried[row.ID]; !row.Created.Equal(want.Time) {
			t.Errorf("the %s is listed as created %s, and the record says %s", row.Kind, row.Created, want)
		}
	}

	found, err := v.Recall(ctx, recall.Request{Query: "the harbour gauge at Tidepool", Limit: 5})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(found.Hits) != 2 {
		t.Fatalf("recall found %d record(s), want the memory and the conversation", len(found.Hits))
	}
	for _, hit := range found.Hits {
		var recalled record.Time
		switch {
		case hit.Memory != nil:
			recalled = hit.Memory.CreatedAt
		case hit.Session != nil:
			recalled = hit.Session.Created
		}
		if listed := listedAs[hit.ID()]; !recalled.Equal(listed.Time) {
			t.Errorf("recall dates the %s %s, and the listing %s", hit.Kind(), recalled, listed)
		}
	}
}
