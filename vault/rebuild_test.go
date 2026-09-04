package vault_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// TestAHeadRebuiltFromChunksIsMeasuredFieldByField answers M18.
//
// A session head is the one record this build never puts on the network, so a
// device that has lost its store has the transcript and not the session naming
// it. Whether the head can be rebuilt from the chunks was inferred when the
// record shape was designed and never measured. This measures it: a head with
// every field populated is written, reconstructed from its chunks alone, and the
// two are compared field by field.
//
// It asserts the verdict rather than merely printing it. The point of the
// exercise is that a portability claim is written against the fields that
// survive, so a change that quietly moves a field from one column to the other
// has to change this test to do it.
func TestAHeadRebuiltFromChunksIsMeasuredFieldByField(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	parent, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "The parent conversation",
		Messages: conversation("parent"),
	})
	if err != nil {
		t.Fatalf("save the parent session: %v", err)
	}

	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:   "Why the packer budgets in ciphertext",
		Summary: "Worked out that a slab is billed whole, so a flush of one record costs the same as a flush of a thousand.",
		Tags:    []string{"storage", "packing"},
		Project: record.Project{CWD: "/home/u/sennit", Repo: "steven3002/sennit", Branch: "main"},
		Agent:   record.Agent{Name: "claude-code", Version: "2.1.223"},
		Models:  []string{"claude-opus-5"},
		Kind:    record.SessionSubagent,
		AgentRef: &record.AgentRef{
			ID: "sub-7", Name: "substrate-reader", Role: "research",
		},
		Lineage:       record.Lineage{ParentSession: &parent.ID},
		Messages:      conversation("m18"),
		PreservedTail: []string{"m18-3", "m18-4"},
		TokensIn:      4100,
		TokensOut:     820,
		DurationMS:    91_000,
	})
	if err != nil {
		t.Fatalf("save the session: %v", err)
	}

	memory, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "A partially filled slab can never be extended.",
		Context:   "Established while working out the packer's flush policy.",
		Type:      record.TypeFact,
		Source:    record.Source{SessionID: saved.ID.String(), Span: "m18-1..m18-2"},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := v.LinkMemory(saved.ID, memory.ID); err != nil {
		t.Fatalf("link the memory to the session: %v", err)
	}

	original, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the head that was written: %v", err)
	}

	// Overwrite is off, so the reconstruction is returned and the real head is
	// left alone. That is what makes this measurable: both records exist at the
	// same moment and can be compared.
	report, err := v.RebuildSessions(ctx, vault.RebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.Sessions != 2 {
		t.Fatalf("rebuilt %d sessions, want the 2 that were written", report.Sessions)
	}
	if report.Stored != 0 || report.Skipped != 2 {
		t.Fatalf("stored %d and skipped %d; a rebuild must not replace a head this device still holds",
			report.Stored, report.Skipped)
	}

	rebuilt := headFor(t, report, saved.ID)
	if rebuilt.Session.ID != original.ID {
		t.Fatalf("rebuilt head is %s, want %s", rebuilt.Session.ID, original.ID)
	}

	// The verdict, asserted. Every field of a head appears exactly once.
	want := map[string]vault.FieldOrigin{
		"id": vault.OriginChunk, "schema": vault.OriginChunk, "schemaVersion": vault.OriginChunk,
		"version": vault.OriginSynthesised,
		"title":   vault.OriginSynthesised,
		"summary": vault.OriginLost, "tags": vault.OriginLost, "project": vault.OriginLost,
		"created": vault.OriginObserved, "updated": vault.OriginObserved,
		"archived": vault.OriginLost,
		"agent":    vault.OriginLost, "agentRef": vault.OriginLost,
		"models":          vault.OriginObserved,
		"counts.messages": vault.OriginDerived, "counts.chunks": vault.OriginDerived,
		"counts.bytes": vault.OriginDerived, "counts.tokens": vault.OriginLost,
		"counts.durationMs": vault.OriginLost,
		"kind":              vault.OriginSynthesised,
		"lineage":           vault.OriginLost,
		"headMessage":       vault.OriginDerived,
		"preservedTail":     vault.OriginLost,
		"chunks":            vault.OriginDerived,
		"links.memories":    vault.OriginReverse,
		"embedding":         vault.OriginLost,
	}
	if len(want) != len(vault.HeadFields) {
		t.Fatalf("the expectation covers %d fields and a head has %d", len(want), len(vault.HeadFields))
	}
	for _, field := range vault.HeadFields {
		if got := rebuilt.Origins[field]; got != want[field] {
			t.Errorf("field %s came back as %q, want %q", field, got, want[field])
		}
	}

	// What survives has to survive exactly. An origin of "derived" that did not
	// reproduce the written value would be worse than an origin of "lost".
	if !reflect.DeepEqual(rebuilt.Session.Chunks, original.Chunks) {
		t.Errorf("the chunk list did not come back identical:\n got %+v\nwant %+v",
			rebuilt.Session.Chunks, original.Chunks)
	}
	if rebuilt.Session.Counts.Messages != original.Counts.Messages {
		t.Errorf("message count %d, want %d", rebuilt.Session.Counts.Messages, original.Counts.Messages)
	}
	if rebuilt.Session.Counts.Chunks != original.Counts.Chunks {
		t.Errorf("chunk count %d, want %d", rebuilt.Session.Counts.Chunks, original.Counts.Chunks)
	}
	if rebuilt.Session.HeadMessage != original.HeadMessage {
		t.Errorf("head message %q, want %q", rebuilt.Session.HeadMessage, original.HeadMessage)
	}
	if !reflect.DeepEqual(rebuilt.Session.Models, original.Models) {
		t.Errorf("models %v, want %v", rebuilt.Session.Models, original.Models)
	}
	if !reflect.DeepEqual(rebuilt.Session.Links.Memories, original.Links.Memories) {
		t.Errorf("memory links %v, want %v, the reverse edge is the one head field with a "+
			"second copy on the network", rebuilt.Session.Links.Memories, original.Links.Memories)
	}
	if rebuilt.Session.Title == original.Title {
		t.Error("the title came back identical, which the chunks cannot support; a synthesised " +
			"title that happens to match would make the report say something it has not measured")
	}

	logFidelity(t, original, rebuilt)
}

// TestARebuiltHeadIsStoredWhenTheDeviceHasNone is the other half of M18: a
// rebuild is only worth measuring if its output is a usable session.
func TestARebuiltHeadIsStoredWhenTheDeviceHasNone(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "A conversation whose head will be lost",
		Messages: conversation("gone"),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	original, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load before: %v", err)
	}

	if err := v.ForgetSessionHead(saved.ID); err != nil {
		t.Fatalf("drop the head: %v", err)
	}
	if _, err := v.Session(saved.ID); err == nil {
		t.Fatal("the head is still on the device, so nothing is being measured")
	}

	report, err := v.RebuildSessions(ctx, vault.RebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.Stored != 1 {
		t.Fatalf("stored %d heads, want 1", report.Stored)
	}
	if report.Gaps != 0 {
		t.Fatalf("%d sessions came back with a hole in the transcript", report.Gaps)
	}

	// The head is back on the device and the transcript replays through it, in
	// order, with every message identical. This is pass mark 3's mechanism.
	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load after: %v", err)
	}
	if len(loaded.Messages) != len(original.Messages) {
		t.Fatalf("replayed %d messages, want %d", len(loaded.Messages), len(original.Messages))
	}
	for i := range loaded.Messages {
		if !reflect.DeepEqual(loaded.Messages[i], original.Messages[i]) {
			t.Fatalf("message %d differs after the rebuild:\n got %+v\nwant %+v",
				i, loaded.Messages[i], original.Messages[i])
		}
	}
	if loaded.Session.Version != 1 {
		t.Errorf("rebuilt head is at version %d, want 1", loaded.Session.Version)
	}
}

// TestARebuiltHeadTakesItsTitleFromTheFirstThingTheUserSaid pins the one
// invented value, because a synthesised title is what a list and a resume will
// show and it must be recognisable rather than an id.
func TestARebuiltHeadTakesItsTitleFromTheFirstThingTheUserSaid(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "A title that is about to be lost",
		Messages: conversation("title"),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := v.ForgetSessionHead(saved.ID); err != nil {
		t.Fatalf("drop the head: %v", err)
	}
	report, err := v.RebuildSessions(ctx, vault.RebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	title := report.Heads[0].Session.Title
	if want := "How many tide stations report hourly?"; title != want {
		t.Errorf("synthesised title %q, want %q", title, want)
	}
}

// TestARebuildReportsAHoleInTheMiddleOfATranscript covers the failure that does
// not announce itself: a transcript missing its last chunks is visibly short,
// and one missing a chunk from the middle reads as continuous.
//
// The chunk here is missing outright, neither on the device nor in the catalog,
// which is what a conversation whose middle never reached the network looks like
// from a machine that has just hydrated. The other shape, a chunk the catalog
// names and the network will not return, is a live path and is asserted there.
func TestARebuildReportsAHoleInTheMiddleOfATranscript(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title: "A transcript with three chunks", Messages: conversation("a"),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, prefix := range []string{"b", "c"} {
		if _, err := v.SaveSession(ctx, vault.SaveSessionRequest{
			ID: saved.ID, Messages: conversation(prefix),
		}); err != nil {
			t.Fatalf("append %s: %v", prefix, err)
		}
	}
	head, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	if len(head.Chunks) != 3 {
		t.Fatalf("the session has %d chunks, want 3", len(head.Chunks))
	}

	// Lose the middle chunk and the head, which is what a partial recovery from
	// the network looks like.
	if err := v.ForgetLocally(head.Chunks[1].ID); err != nil {
		t.Fatalf("drop the middle chunk body: %v", err)
	}
	if err := v.ForgetSessionHead(saved.ID); err != nil {
		t.Fatalf("drop the head: %v", err)
	}

	report, err := v.RebuildSessions(ctx, vault.RebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if report.Gaps != 1 {
		t.Fatalf("reported %d gaps, want 1, a missing middle chunk must not read as a "+
			"complete conversation", report.Gaps)
	}
	rebuilt := report.Heads[0].Session
	if len(rebuilt.Chunks) != 2 {
		t.Fatalf("the rebuilt head names %d chunks, want the 2 that were readable", len(rebuilt.Chunks))
	}
}

func headFor(t *testing.T, report vault.RebuildReport, id record.ID) vault.RebuiltHead {
	t.Helper()
	for _, head := range report.Heads {
		if head.Session != nil && head.Session.ID == id {
			return head
		}
	}
	t.Fatalf("the rebuild returned no head for %s", id)
	return vault.RebuiltHead{}
}

// logFidelity prints the field-by-field answer to M18, which is the deliverable
// the assertion above exists to keep honest.
func logFidelity(t *testing.T, original *record.Session, rebuilt vault.RebuiltHead) {
	t.Helper()
	values := headValues(original)
	got := headValues(rebuilt.Session)

	fields := append([]string(nil), vault.HeadFields...)
	sort.SliceStable(fields, func(i, j int) bool {
		return originOrder(rebuilt.Origins[fields[i]]) < originOrder(rebuilt.Origins[fields[j]])
	})

	t.Logf("M18, a session head rebuilt from its chunks alone, field by field")
	t.Logf("  %-18s %-13s %-34s %s", "field", "origin", "written", "rebuilt")
	for _, field := range fields {
		t.Logf("  %-18s %-13s %-34s %s",
			field, rebuilt.Origins[field], clip(values[field], 34), clip(got[field], 40))
	}
}

func originOrder(origin vault.FieldOrigin) int {
	for i, known := range []vault.FieldOrigin{
		vault.OriginChunk, vault.OriginDerived, vault.OriginObserved,
		vault.OriginReverse, vault.OriginSynthesised, vault.OriginLost,
	} {
		if known == origin {
			return i
		}
	}
	return 99
}

func headValues(s *record.Session) map[string]string {
	return map[string]string{
		"id": s.ID.String(), "schema": s.Schema,
		"schemaVersion": fmt.Sprint(s.SchemaVersion), "version": fmt.Sprint(s.Version),
		"title": s.Title, "summary": s.Summary,
		"tags": strings.Join(s.Tags, ","), "project": fmt.Sprintf("%+v", s.Project),
		"created": s.Created.String(), "updated": s.Updated.String(),
		"archived": fmt.Sprint(s.Archived),
		"agent":    fmt.Sprintf("%+v", s.Agent), "agentRef": fmt.Sprintf("%+v", s.AgentRef),
		"models":          strings.Join(s.Models, ","),
		"counts.messages": fmt.Sprint(s.Counts.Messages), "counts.chunks": fmt.Sprint(s.Counts.Chunks),
		"counts.bytes":      fmt.Sprint(s.Counts.Bytes),
		"counts.tokens":     fmt.Sprintf("in %d / out %d", s.Counts.TokensIn, s.Counts.TokensOut),
		"counts.durationMs": fmt.Sprint(s.Counts.DurationMS),
		"kind":              string(s.Kind), "lineage": fmt.Sprintf("%+v", s.Lineage),
		"headMessage": s.HeadMessage, "preservedTail": strings.Join(s.PreservedTail, ","),
		"chunks":         fmt.Sprintf("%d refs", len(s.Chunks)),
		"links.memories": fmt.Sprint(len(s.Links.Memories)) + " linked",
		"embedding":      fmt.Sprintf("%+v", s.Embedding),
	}
}

func clip(s string, n int) string {
	if s == "" {
		return "(none)"
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
