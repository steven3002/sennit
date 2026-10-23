package mcp_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// leftParts are the words a refusal names a memory's parts by.
var leftParts = []string{"its body", "its search vector", "its metadata"}

// serveAt opens a vault in home and connects a client to a server over it, for
// a test that also reaches the vault's files as another process would.
func serveAt(t *testing.T, home string) (*sdk.ClientSession, *vault.Vault) {
	t.Helper()
	v := openVault(t, home)
	return connect(t, mcp.New(v)), v
}

// deviceAt opens the device store in home as a second process would.
func deviceAt(t *testing.T, home string) *local.Store {
	t.Helper()
	device, err := local.Open(filepath.Join(home, "vault.db"))
	if err != nil {
		t.Fatalf("open the device store as a second process would: %v", err)
	}
	t.Cleanup(func() { device.Close() })
	return device
}

// refuseWrites makes the vault's database in home refuse one kind of write, the
// way a disk error stops a write part way, and returns what lifts the refusal.
// event is when the refusal applies, in a trigger's words.
func refuseWrites(t *testing.T, home, event string) (lift func()) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "vault.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open the vault's database: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_write ` + event +
		` BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`); err != nil {
		db.Close()
		t.Fatalf("make the database refuse %s: %v", event, err)
	}
	return func() {
		t.Helper()
		if _, err := db.Exec(`DROP TRIGGER refuse_write`); err != nil {
			t.Fatalf("lift the refusal: %v", err)
		}
		db.Close()
	}
}

// idIn reads the record id out of an address.
func idIn(t *testing.T, uri string) record.ID {
	t.Helper()
	address, err := mcp.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	return address.ID
}

// listed reports whether browse lists an address.
func listed(t *testing.T, session *sdk.ClientSession, uri string) bool {
	t.Helper()
	var page mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{}, &page)
	return slices.ContainsFunc(page.Rows, func(row mcp.RowOut) bool { return row.URI == uri })
}

// namesOnly checks that a refusal names every part of a memory in want and no
// other.
func namesOnly(t *testing.T, refusal string, want []string) {
	t.Helper()
	for _, part := range leftParts {
		if named := strings.Contains(refusal, part); named != slices.Contains(want, part) {
			t.Errorf("the refusal names %q %v, want %v: %s", part, named, !named, refusal)
		}
	}
}

// Forgetting a memory part of which a remember or a forget left on this device
// never answers that there was nothing there. Either the forget removes what is
// left and says the memory is gone, or it refuses, names what is left, says that
// nothing was removed, and nothing is.
//
// Such a memory has neither a catalog entry nor a place in the upload queue,
// which is all a memory already gone lacks too, and it can still be listed,
// opened or recalled. Answered as a memory already gone is, the agent would tell
// the user it was gone.
func TestAMemoryPartOfWhichIsStillHereIsNeverSaidToHaveHadNothingThere(t *testing.T) {
	for _, c := range []struct {
		name string
		// leave stores a memory and stops part way, and returns its address.
		leave func(t *testing.T, home string, session *sdk.ClientSession) string
		// left is what the stopped write leaves, and opens whether its body is.
		left  []string
		opens bool
	}{
		{
			name: "a remember that stopped before it queued the memory",
			leave: func(t *testing.T, home string, session *sdk.ClientSession) string {
				lift := refuseWrites(t, home, `BEFORE INSERT ON queue`)
				result := callRaw(t, session, "remember", mcp.RememberIn{
					Statement: "The harbour gauge at Tidepool reads in centimetres.",
					Context:   "From the Tidepool field guide, written while the disk failed.",
					Type:      "fact",
					Tags:      []string{"harbour", "gauge"},
				})
				lift()
				if !result.IsError || !strings.Contains(resultText(result), "the disk refused the write") {
					t.Fatalf("a remember whose queueing the disk refused answered %s", resultText(result))
				}
				var page mcp.BrowseOut
				call(t, session, "browse", mcp.BrowseIn{}, &page)
				if len(page.Rows) != 1 {
					t.Fatalf("browse lists %d record(s), want the memory the failed remember stored", len(page.Rows))
				}
				return page.Rows[0].URI
			},
			left:  leftParts,
			opens: true,
		},
		{
			name: "a forget that stopped at the memory's ranking rows",
			leave: func(t *testing.T, home string, session *sdk.ClientSession) string {
				uri := storeMemory(t, session, mcp.RememberIn{
					Statement: "The harbour gauge at Tidepool reads in centimetres.",
					Context:   "From the Tidepool field guide, written to be forgotten while the disk failed.",
					Type:      "fact",
					Tags:      []string{"harbour", "gauge"},
				})
				lift := refuseWrites(t, home,
					fmt.Sprintf(`BEFORE DELETE ON record_meta WHEN OLD.record_id = '%s'`, idIn(t, uri)))
				result := callRaw(t, session, "forget", mcp.ForgetIn{URI: uri, Confirm: true})
				lift()
				if !result.IsError || !strings.Contains(resultText(result), "the disk refused the write") {
					t.Fatalf("a forget the disk stopped answered %s", resultText(result))
				}
				return uri
			},
			left: []string{"its metadata"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			session, _ := serveAt(t, home)
			uri := c.leave(t, home, session)
			if !listed(t, session, uri) {
				t.Fatalf("what the stopped write left is not listed, so this checks nothing about it")
			}

			result := callRaw(t, session, "forget", mcp.ForgetIn{URI: uri, Confirm: true})
			if !result.IsError {
				// A vault that can finish such a forget does, and the answer is the
				// ordinary one.
				var out mcp.ForgetOut
				decodeStructured(t, "forget", result, &out)
				if !out.Removed || !strings.Contains(out.Note, "The memory is gone from this vault.") {
					t.Fatalf("forgetting what the stopped write left answered %+v", out)
				}
				if listed(t, session, uri) {
					t.Error("the memory the forget called gone is still listed")
				}
				if opened := callRaw(t, session, "open", mcp.OpenIn{URI: uri}); !opened.IsError {
					t.Errorf("the memory the forget called gone still opens: %s", resultText(opened))
				}
				return
			}

			refusal := resultText(result)
			for _, want := range []string{uri, "was not forgotten, and nothing was removed",
				"Do not tell the user it is gone"} {
				if !strings.Contains(refusal, want) {
					t.Errorf("the refusal does not say %q: %s", want, refusal)
				}
			}
			namesOnly(t, refusal, c.left)
			if !listed(t, session, uri) {
				t.Error("the refused forget took the memory out of the listing")
			}
			if opened := callRaw(t, session, "open", mcp.OpenIn{URI: uri}); opened.IsError == c.opens {
				t.Errorf("the memory opens %v after the refused forget, want %v: %s", !opened.IsError, c.opens,
					resultText(opened))
			}
		})
	}
}

// A memory of which only its search vector is left is refused with the vector
// named as what is left, and the vector stays where it was.
//
// Only the vector is left when a remember stopped between its vector and its
// ranking rows and the body went afterwards, by a forget that stopped part way
// or by dropping this device's copy. Recall still ranks it, so the forget must
// not answer that nothing was there.
func TestAMemoryWhoseVectorIsAllThatIsLeftIsRefusedWithTheVectorNamed(t *testing.T) {
	home := t.TempDir()
	session, _ := serveAt(t, home)

	uri := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the Tidepool field guide, written to leave its vector behind.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	})
	id := idIn(t, uri)
	device := deviceAt(t, home)
	if withdrawn, err := device.WithdrawQueued(id, 0); err != nil || !withdrawn {
		t.Fatalf("take the memory out of the queue: %v, %v", withdrawn, err)
	}
	if err := device.ForgetBody(id); err != nil {
		t.Fatalf("take the body: %v", err)
	}
	if err := device.ForgetRankingMeta(id); err != nil {
		t.Fatalf("take the ranking rows: %v", err)
	}
	before := vaultCounts(t, session)
	if before.Indexed != 1 || before.Records != 0 {
		t.Fatalf("the vault counts %d vector(s) and %d record(s), want the vector alone", before.Indexed, before.Records)
	}

	result := callRaw(t, session, "forget", mcp.ForgetIn{URI: uri, Confirm: true})
	if !result.IsError {
		t.Fatalf("a memory whose vector is still here was answered as forgotten: %s", resultText(result))
	}
	refusal := resultText(result)
	for _, want := range []string{uri, "was not forgotten, and nothing was removed", "Do not tell the user it is gone"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal does not say %q: %s", want, refusal)
		}
	}
	namesOnly(t, refusal, []string{"its search vector"})
	if after := vaultCounts(t, session); after.Indexed != before.Indexed {
		t.Errorf("the refused forget took the vector: %d searchable, want %d", after.Indexed, before.Indexed)
	}
}

// A memory's address that carries the id of a conversation, or of a piece of a
// conversation's transcript, is refused as not a memory, says what the id
// belongs to, and removes nothing.
//
// Nothing of a memory is there, but something is, and an agent told that
// nothing was there would report as gone a record that is still stored. The
// conversation's own address is given, because the likeliest way to arrive here
// is to have written the wrong one.
func TestAMemoryAddressWithAnotherRecordsIDIsRefusedAsWhatItIs(t *testing.T) {
	home := t.TempDir()
	session, v := serveAt(t, home)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	asMemory := mcp.URI(record.KindMemory, idIn(t, saved.URI))
	result := callRaw(t, session, "forget", mcp.ForgetIn{URI: asMemory, Confirm: true})
	if !result.IsError {
		t.Fatalf("a conversation's id given as a memory was answered as forgotten: %s", resultText(result))
	}
	for _, want := range []string{asMemory + " is not a memory, and nothing was removed", "a conversation, at " + saved.URI} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the refusal does not say %q: %s", want, resultText(result))
		}
	}
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.URI}, &opened)
	if !strings.Contains(opened.Content, "Tidepool gauge survey") {
		t.Errorf("the refused forget took the conversation: %s", opened.Content)
	}

	// A chunk with neither a catalog entry nor a place in the queue is what a
	// forget of its conversation that stopped part way leaves. Chunk ids are never
	// in an answer, so it is taken from the vault itself.
	run, err := v.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Tidepool registry audit",
		Summary:  "Audited the Tidepool station registry for duplicate gauges.",
		Messages: conversation("audit"),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	chunk := run.Chunks[0].ID
	if withdrawn, err := deviceAt(t, home).WithdrawQueued(chunk, 0); err != nil || !withdrawn {
		t.Fatalf("take the chunk out of the queue: %v, %v", withdrawn, err)
	}
	chunkAsMemory := mcp.URI(record.KindMemory, chunk)
	result = callRaw(t, session, "forget", mcp.ForgetIn{URI: chunkAsMemory, Confirm: true})
	if !result.IsError {
		t.Fatalf("a transcript chunk's id given as a memory was answered as forgotten: %s", resultText(result))
	}
	for _, want := range []string{chunkAsMemory + " is not a memory, and nothing was removed",
		"part of a conversation's transcript"} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the refusal does not say %q: %s", want, resultText(result))
		}
	}
	call(t, session, "open", mcp.OpenIn{URI: mcp.TranscriptURI(run.ID)}, &opened)
	if !strings.Contains(opened.Content, "Forty-one stations") {
		t.Errorf("the refused forget took part of the transcript: %s", opened.Content)
	}
}
