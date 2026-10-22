package vault

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// datedByTheRebuild dates a conversation's head on a closed device as a release
// that dated a rebuilt head by its rebuild did, in the head and in the columns
// copied from it, and leaves its revision as it is. A head that release rebuilt
// from turns that record no time carries the rebuild's moment as both its
// Created and its Updated.
func datedByTheRebuild(t *testing.T, home string, id record.ID, created, updated record.Time) {
	t.Helper()
	session := headOn(t, home, id)
	session.Created, session.Updated = created, updated
	head, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal the head of %s: %v", id, err)
	}
	if _, err := deviceDB(t, home).Exec(
		`UPDATE session_heads SET head = ?, created_at = ?, updated_at = ? WHERE session_id = ?`,
		head, created.String(), updated.String(), id.String()); err != nil {
		t.Fatalf("date the head of %s: %v", id, err)
	}
}

// headOn reads the head a device holds for a conversation, through a connection
// of the test's own.
func headOn(t *testing.T, home string, id record.ID) *record.Session {
	t.Helper()
	var held []byte
	if err := deviceDB(t, home).QueryRow(`SELECT head FROM session_heads WHERE session_id = ?`,
		id.String()).Scan(&held); err != nil {
		t.Fatalf("read the head of %s: %v", id, err)
	}
	session, err := record.UnmarshalSession(held)
	if err != nil {
		t.Fatalf("decode the head of %s: %v", id, err)
	}
	return session
}

// restoredConversation has a writer save one conversation whose turns record no
// time, and restores it onto a fresh device as a hydrate at metadata depth does:
// its transcript catalogued in a slab the device adopts, and its head rebuilt
// from the transcript. change, when there is one, alters one thing about the
// device while it is open. The device is then closed and left as an earlier
// build left it, its listing row dated by the restore and nothing recorded
// about dating the listing; the caller dates the head. It returns the device's
// home, the conversation and the restore's time.
func restoredConversation(t *testing.T, change func(device *Vault, head *record.Session)) (string, record.ID, record.Time) {
	t.Helper()
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()
	saved, err := writer.SaveSession(context.Background(), SaveSessionRequest{
		Title:    "Which Tidepool gauges report hourly",
		Messages: exchange("gauges", false),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	network := FlushToNetworkForTest(t, writer)

	home := t.TempDir()
	device := ledgerVault(t, home)
	RestoreForTest(t, device, network, HydrateMetadata)
	if change != nil {
		head, err := device.Session(saved.ID)
		if err != nil {
			t.Fatalf("read the rebuilt head: %v", err)
		}
		change(device, head)
	}
	if err := device.Close(); err != nil {
		t.Fatalf("close the device: %v", err)
	}
	restored := []restoredRecord{{name: "gauges", id: saved.ID, kind: record.KindSession}}
	asAnEarlierBuildLeftIt(t, home, restored)
	return home, saved.ID, restored[0].restored
}

// A conversation that v0.1.0-beta-mvp rebuilt from turns that record no time is
// listed after every record that has a time once the vault is opened, in the
// listing and in the list a resume offers, and its head is stored without the
// dates that release gave it, as a rebuild stores such a head now.
//
// That release gave such a head the moment of the rebuild as both its Created
// and its Updated. Dated by the time it carries, the conversation was still
// listed first, dated by the restore, and a resume took it for the most recent.
// A conversation whose turns record times keeps the head it has.
func TestAConversationTheReleaseRebuiltWithoutTurnTimesIsUndatedAndListedLast(t *testing.T) {
	home, records := restoredByAnEarlierBuild(t)
	byName := func(name string) restoredRecord {
		return records[slices.IndexFunc(records, func(r restoredRecord) bool { return r.name == name })]
	}
	undated, survey := byName("undated"), byName("survey")
	datedByTheRebuild(t, home, undated.id, undated.restored, undated.restored)
	surveyed := headOn(t, home, survey.id)

	device := ledgerVault(t, home)
	defer device.Close()

	rows := listingOf(t, device)
	datedAs(t, rows, records, ownTime)
	if last := rows[len(rows)-1]; last.ID != undated.id || !last.Created.IsZero() {
		t.Errorf("the listing ends with %s, created %s, want the undated conversation, with no time", last.ID, last.Created)
	}

	sessions, err := device.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list the conversations: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != survey.id || sessions[1].ID != undated.id {
		t.Fatalf("a resume is offered %+v, want the survey conversation and then the undated one", sessions)
	}
	if last := sessions[1]; !last.Created.IsZero() || !last.Updated.IsZero() {
		t.Errorf("a resume is offered the undated conversation as created %s and updated %s, want neither",
			last.Created, last.Updated)
	}

	if head := headOn(t, home, undated.id); head.Version != 2 || !head.Created.IsZero() || !head.Updated.IsZero() {
		t.Errorf("the head the release rebuilt is at version %d, created %s, updated %s; want version 2 and no dates",
			head.Version, head.Created, head.Updated)
	}
	if head := headOn(t, home, survey.id); head.Version != surveyed.Version ||
		!head.Created.Equal(surveyed.Created.Time) || !head.Updated.Equal(surveyed.Updated.Time) {
		t.Errorf("the survey conversation's head is at version %d, created %s, updated %s; want it as it was, "+
			"at version %d, created %s, updated %s", head.Version, head.Created, head.Updated,
			surveyed.Version, surveyed.Created, surveyed.Updated)
	}
	if rewrite := rewriteRecorded(t, device); rewrite.Undated != 1 {
		t.Errorf("the device recorded %d head(s) undated, want 1", rewrite.Undated)
	}
}

// A conversation this device saved keeps its dates, flushed or still queued,
// even when its Created and its Updated are one time and none of its turns
// records a time, as a save made inside one millisecond leaves it. Its
// transcript is queued here or in a slab this device pinned, which a transcript
// a release rebuilt from the network never is.
func TestAConversationThisDeviceSavedItselfKeepsItsDates(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	device := ledgerVault(t, home)
	save := func(name string) record.ID {
		t.Helper()
		saved, err := device.SaveSession(ctx, SaveSessionRequest{
			Title:    "The " + name + " conversation",
			Messages: exchange(name, false),
		})
		if err != nil {
			t.Fatalf("save the %s conversation: %v", name, err)
		}
		return saved.ID
	}
	saved := map[string]record.ID{"flushed": save("flushed")}
	flushed, err := device.Session(saved["flushed"])
	if err != nil {
		t.Fatalf("read the flushed conversation's head: %v", err)
	}
	if written := flushQueue(t, device); !written[flushed.Chunks[0].ID] {
		t.Fatal("the flush did not write the flushed conversation's transcript, so this checks nothing")
	}
	saved["queued"] = save("queued")
	if err := device.Close(); err != nil {
		t.Fatalf("close the device: %v", err)
	}

	before := make(map[string]*record.Session, len(saved))
	for name, id := range saved {
		head := headOn(t, home, id)
		datedByTheRebuild(t, home, id, head.Created, head.Created)
		before[name] = headOn(t, home, id)
	}
	if _, err := deviceDB(t, home).Exec(`DELETE FROM meta WHERE key = ?`, listingDatesKey); err != nil {
		t.Fatalf("take the marker away: %v", err)
	}

	reopened := ledgerVault(t, home)
	defer reopened.Close()
	for name, id := range saved {
		after, was := headOn(t, home, id), before[name]
		if after.Version != was.Version || !after.Created.Equal(was.Created.Time) || !after.Updated.Equal(was.Updated.Time) {
			t.Errorf("the %s conversation's head is at version %d, created %s, updated %s; want it as it was, "+
				"at version %d, created and updated %s", name, after.Version, after.Created, after.Updated,
				was.Version, was.Created)
		}
	}
	if rewrite := rewriteRecorded(t, reopened); rewrite.Undated != 0 {
		t.Errorf("the device recorded %d head(s) undated, want none", rewrite.Undated)
	}
}

// A conversation a release rebuilt that has since had turns appended on this
// device keeps its dates: its Updated is the append's and no longer its Created,
// and the appended turns are queued here.
func TestAConversationTheReleaseRebuiltThenAppendedToHereKeepsItsDates(t *testing.T) {
	home, id, _ := restoredConversation(t, func(device *Vault, head *record.Session) {
		if _, err := device.SaveSession(context.Background(), SaveSessionRequest{
			ID: head.ID, Messages: exchange("more", false),
		}); err != nil {
			t.Fatalf("append to the conversation: %v", err)
		}
	})
	appended := headOn(t, home, id)
	rebuiltAt := record.At(appended.Updated.Add(-time.Hour))
	datedByTheRebuild(t, home, id, rebuiltAt, appended.Updated)

	device := ledgerVault(t, home)
	defer device.Close()
	if head := headOn(t, home, id); head.Version != appended.Version ||
		!head.Created.Equal(rebuiltAt.Time) || !head.Updated.Equal(appended.Updated.Time) {
		t.Errorf("the appended conversation's head is at version %d, created %s, updated %s; want it as it was, "+
			"at version %d, created %s, updated %s", head.Version, head.Created, head.Updated,
			appended.Version, rebuiltAt, appended.Updated)
	}
	if rewrite := rewriteRecorded(t, device); rewrite.Undated != 0 {
		t.Errorf("the device recorded %d head(s) undated, want none", rewrite.Undated)
	}
}

// A head that release rebuilt is told by three things together, and a head that
// fails any one of them is left exactly as it is. The first case holds all
// three, so every other case starts from a head the rewrite does take the dates
// off, and differs from it in one thing.
func TestAHeadTheReleaseRebuiltIsLeftAsItIsUnlessAllThreeHold(t *testing.T) {
	chunkOf := func(head *record.Session) record.ID { return head.Chunks[0].ID }
	for _, c := range []struct {
		name   string
		change func(t *testing.T, device *Vault, head *record.Session)
		// dates gives the head's Created and Updated from the rebuild's time.
		dates   func(rebuiltAt record.Time) (record.Time, record.Time)
		undated bool
	}{
		{name: "all three hold", undated: true},
		{name: "a turn on this device records a time", change: func(t *testing.T, device *Vault, head *record.Session) {
			_, body, err := device.local.GetRecord(chunkOf(head))
			if err != nil {
				t.Fatalf("read the chunk: %v", err)
			}
			chunk, err := record.UnmarshalChunk(body)
			if err != nil {
				t.Fatalf("decode the chunk: %v", err)
			}
			chunk.Messages[1].Created = record.At(time.Date(2025, time.March, 14, 9, 1, 0, 0, time.UTC))
			if body, err = record.MarshalChunk(chunk); err != nil {
				t.Fatalf("marshal the chunk: %v", err)
			}
			if err := device.local.PutBody(chunk.ID, record.KindChunk, body); err != nil {
				t.Fatalf("store the chunk: %v", err)
			}
		}},
		{name: "a chunk is not on this device", change: func(t *testing.T, device *Vault, head *record.Session) {
			if err := device.local.ForgetBody(chunkOf(head)); err != nil {
				t.Fatalf("drop the chunk: %v", err)
			}
		}},
		{name: "its Updated is not its Created", dates: func(at record.Time) (record.Time, record.Time) {
			return at, record.At(at.Add(time.Minute))
		}},
		{name: "it carries no time", dates: func(record.Time) (record.Time, record.Time) {
			return record.Time{}, record.Time{}
		}},
		{name: "its transcript is in a slab this device pinned", change: func(t *testing.T, device *Vault, _ *record.Session) {
			if taken, err := device.local.TakeSlabOwnership(); err != nil || taken == 0 {
				t.Fatalf("take the adopted slab over: %d taken, %v", taken, err)
			}
		}},
		{name: "its transcript is queued here", change: func(t *testing.T, device *Vault, head *record.Session) {
			if err := device.local.Enqueue(local.QueuedBlob{
				ID: chunkOf(head), Kind: record.KindChunk, CID: "a chunk written again", Payload: []byte("sealed"),
			}); err != nil {
				t.Fatalf("queue the chunk: %v", err)
			}
		}},
		{name: "the catalog does not name its transcript", change: func(t *testing.T, device *Vault, head *record.Session) {
			if err := device.manifest.Remove(chunkOf(head)); err != nil {
				t.Fatalf("take the chunk out of the catalog: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var change func(*Vault, *record.Session)
			if c.change != nil {
				change = func(device *Vault, head *record.Session) { c.change(t, device, head) }
			}
			home, id, rebuiltAt := restoredConversation(t, change)
			created, updated := rebuiltAt, rebuiltAt
			if c.dates != nil {
				created, updated = c.dates(rebuiltAt)
			}
			datedByTheRebuild(t, home, id, created, updated)
			before := headOn(t, home, id)

			device := ledgerVault(t, home)
			defer device.Close()
			after := headOn(t, home, id)
			rows := listingOf(t, device)
			if len(rows) != 1 || !rows[0].Created.Equal(after.Created.Time) || rows[0].Created.IsZero() != after.Created.IsZero() {
				t.Errorf("the conversation is listed %+v, want it dated %s, as its head is", rows, after.Created)
			}
			if c.undated {
				if after.Version != before.Version+1 || !after.Created.IsZero() || !after.Updated.IsZero() {
					t.Errorf("the head is at version %d, created %s, updated %s; want version %d and no dates",
						after.Version, after.Created, after.Updated, before.Version+1)
				}
				return
			}
			if after.Version != before.Version || !after.Created.Equal(created.Time) || after.Created.IsZero() != created.IsZero() ||
				!after.Updated.Equal(updated.Time) || after.Updated.IsZero() != updated.IsZero() {
				t.Errorf("the head is at version %d, created %s, updated %s; want it as it was, at version %d, "+
					"created %s, updated %s", after.Version, after.Created, after.Updated, before.Version, created, updated)
			}
			if rewrite := rewriteRecorded(t, device); rewrite.Undated != 0 {
				t.Errorf("the device recorded %d head(s) undated, want none", rewrite.Undated)
			}
		})
	}
}
