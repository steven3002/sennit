package vault

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/packer"
)

// exchange is a short conversation whose turns record when they were said if
// dated is set, and record nothing of it otherwise. The dated turns are a minute
// apart and long before any test runs, so a time a head was written can never be
// mistaken for one of them.
func exchange(prefix string, dated bool) []record.Message {
	start := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	messages := make([]record.Message, 4)
	for i := range messages {
		messages[i] = record.Message{
			ID:    fmt.Sprintf("%s-%d", prefix, i),
			Role:  record.RoleUser,
			Parts: []record.Part{{Type: record.PartText, Text: fmt.Sprintf("The %s conversation, turn %d.", prefix, i)}},
		}
		if i%2 == 1 {
			messages[i].Role = record.RoleAssistant
		}
		if i > 0 {
			messages[i].Parent = messages[i-1].ID
		}
		if dated {
			messages[i].Created = record.At(start.Add(time.Duration(i) * time.Minute))
		}
	}
	return messages
}

// hydrateFrom puts what one device wrote onto a device that has never held the
// vault, the way a hydrate to metadata depth does, with the test standing in for
// the network.
//
// The writer's queue is flushed the way flushQueue flushes it, so what the
// network is handed is exactly what a flush would upload. The fresh device
// restores each of those frames through the step a hydrate applies to every frame
// it walks, and then rebuilds the heads the way a hydrate does once the walk is
// done.
func hydrateFrom(t *testing.T, writer, fresh *Vault) RebuildReport {
	t.Helper()
	ctx := context.Background()
	claimed, err := writer.local.ClaimQueued("test flush", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	land(t, writer, claimed)

	request := HydrateRequest{Depth: HydrateMetadata}
	var walked HydrateReport
	for _, blob := range claimed {
		object := sia.StoredObject{Slab: probeSlab, UpdatedAt: time.Now()}
		copy(object.Ref.ID[:], blob.ID[:])
		frames, err := seal.Frames(blob.Payload)
		if err != nil {
			t.Fatalf("read the frames of %s: %v", blob.ID, err)
		}
		for _, frame := range frames {
			if err := fresh.hydrateFrame(ctx, frame, object, request, &walked); err != nil {
				t.Fatalf("restore %s: %v", frame.ID, err)
			}
		}
	}
	if walked.Bodies == 0 || walked.Bodies != len(claimed) {
		t.Fatalf("the fresh device restored %d record(s) of the %d written", walked.Bodies, len(claimed))
	}

	report, err := fresh.RebuildSessions(ctx, RebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return report
}

// A conversation whose turns do not record when they were said is listed after
// one whose turns do, on a device that rebuilt both from the network. On the
// device that saved them, the one saved last is listed first, whatever its turns
// record, as it always was.
//
// A rebuilt head knows when its conversation happened only from its turns. One
// whose turns said nothing was dated by when the rebuild ran, which is later than
// any turn the network holds, so on the second device it was listed first however
// old it was, and `recent` resumes the first conversation listed. That device
// cannot tell when an undated conversation happened, so it lists it after the
// ones it can date, including when it was in fact saved last.
func TestAConversationWithoutTurnTimesIsListedAfterADatedOneWhereItWasRebuilt(t *testing.T) {
	for _, saved := range [][]string{{"undated", "dated"}, {"dated", "undated"}} {
		t.Run(strings.Join(saved, " then "), func(t *testing.T) {
			ctx := context.Background()
			writer := ledgerVault(t, t.TempDir())
			defer writer.Close()

			ids := make(map[string]record.ID, len(saved))
			for i, name := range saved {
				if i > 0 {
					// A head's times are kept to the millisecond, and two saves
					// inside one would tie, which an id that is random breaks.
					time.Sleep(2 * time.Millisecond)
				}
				result, err := writer.SaveSession(ctx, SaveSessionRequest{
					Title:    "The " + name + " conversation",
					Messages: exchange(name, name == "dated"),
				})
				if err != nil {
					t.Fatalf("save the %s conversation: %v", name, err)
				}
				ids[name] = result.ID
			}
			names := func(v *Vault) []string {
				t.Helper()
				rows, err := v.ListSessions(local.SessionQuery{})
				if err != nil {
					t.Fatalf("list sessions: %v", err)
				}
				var out []string
				for _, row := range rows {
					for name, id := range ids {
						if row.ID == id {
							out = append(out, name)
						}
					}
				}
				return out
			}

			if got, want := names(writer), []string{saved[1], saved[0]}; !slices.Equal(got, want) {
				t.Errorf("the device that saved them lists %v, want %v, the one saved last first", got, want)
			}

			fresh := ledgerVault(t, t.TempDir())
			defer fresh.Close()
			if report := hydrateFrom(t, writer, fresh); report.Stored != 2 {
				t.Fatalf("the fresh device rebuilt %d conversation(s), want both", report.Stored)
			}
			if got, want := names(fresh), []string{"dated", "undated"}; !slices.Equal(got, want) {
				t.Errorf("the device that rebuilt them lists %v, want %v, the dated one first", got, want)
			}
		})
	}
}

// gaugeSurveyNetwork has a writer store a memory and a conversation and flush
// both to the network the test stands in for. It returns their ids and the
// network.
func gaugeSurveyNetwork(t *testing.T) (memory, conversation record.ID, network *standInNetwork) {
	t.Helper()
	writer := OpenWithLedgerForTest(t, t.TempDir())
	memory = rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved, err := writer.SaveSession(context.Background(), SaveSessionRequest{
		Title:    "Tidepool gauge survey",
		Summary:  "Counted the stations reporting hourly.",
		Messages: exchange("survey", true),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	return memory, saved.ID, standIn(t, FlushToNetworkForTest(t, writer))
}

// unvectored names the records of the ones given that have no search vector on
// v.
func unvectored(v *Vault, records map[string]record.ID) []string {
	var names []string
	for name, id := range records {
		if !v.index.Has(id) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// A hydrate that is not told a depth goes to index depth, so the device it
// restores searches every record by meaning.
//
// It went to metadata depth, which brings back every record and rebuilds every
// conversation and writes no search vector. A recall on the device then found a
// record only by a word it shared with the question.
func TestAHydrateToldNoDepthGivesEveryRecordASearchVector(t *testing.T) {
	memory, conversation, network := gaugeSurveyNetwork(t)
	device := OpenWithLedgerForTest(t, t.TempDir())

	report, err := device.hydrateOver(context.Background(), HydrateRequest{}, network.walk, network.read)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if report.Depth != HydrateIndex {
		t.Errorf("a hydrate told no depth went to depth %s, want %s", report.Depth, HydrateIndex)
	}
	if missing := unvectored(device, map[string]record.ID{
		"the memory": memory, "the conversation": conversation,
	}); len(missing) > 0 {
		t.Errorf("after a hydrate told no depth, %v have no search vector", missing)
	}
}

// A hydrate at index depth, run on a device that a hydrate at metadata depth
// restored, gives every record a search vector, the conversations included, and
// keeps the heads the first one rebuilt.
//
// The summary of a hydrate at metadata depth, and every answer on the device it
// restored, name `--depth index` as what makes a record searchable by meaning.
// Run afterwards, it gave each memory its vector and no conversation one. The
// rebuild keeps a head the device already holds, since it cannot rebuild one as
// well as the one that was stored, and it kept the head's want of a vector with
// it, so each conversation was found by its words alone however often the
// hydrate ran.
func TestAHydrateAtIndexDepthAfterOneAtMetadataDepthGivesEveryRecordAVector(t *testing.T) {
	memory, conversation, network := gaugeSurveyNetwork(t)
	device := OpenWithLedgerForTest(t, t.TempDir())
	network.hydrateInto(device, HydrateMetadata)
	rebuilt, err := device.local.GetSessionHead(conversation)
	if err != nil {
		t.Fatalf("the head the hydrate at metadata depth rebuilt: %v", err)
	}

	report := network.hydrateInto(device, HydrateIndex)
	if report.Rebuild.Stored != 0 || report.Rebuild.Skipped != 1 {
		t.Errorf("the hydrate at index depth stored %d head(s) and kept %d, want none stored and the 1 "+
			"this device held kept", report.Rebuild.Stored, report.Rebuild.Skipped)
	}
	if missing := unvectored(device, map[string]record.ID{
		"the memory": memory, "the conversation": conversation,
	}); len(missing) > 0 {
		t.Errorf("after a hydrate at index depth, %v have no search vector", missing)
	}
	kept, err := device.local.GetSessionHead(conversation)
	if err != nil {
		t.Fatalf("the head after the hydrate at index depth: %v", err)
	}
	if !bytes.Equal(kept, rebuilt) {
		t.Error("the hydrate at index depth replaced the head this device held")
	}
}
