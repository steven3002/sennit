package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

// The records the approved previews are drawn from.
var (
	progressHit = hit{
		similarity: "0.56", full: "0.5642", kind: "memory", typ: "fact",
		text:    "Prefers one live status line over scrolling logs",
		context: "The user is choosing a CLI look for Sennit",
		tags:    []string{"cli", "ui"}, id: "4083dbdd9e9f0f816c797080eed61fba",
	}
	grantHit = hit{
		similarity: "0.46", full: "0.4607", kind: "memory", typ: "fact",
		text:    "The Sia grant ask is 26k over five months",
		context: "Grant planning",
		tags:    []string{"grant"}, id: "ea0df403908921a3f8ede37d406c4de4",
	}
	tmpdirHit = hit{
		similarity: "0.44", full: "0.4357", kind: "memory", typ: "preference",
		text:    "Go builds on this box need TMPDIR set to the home filesystem",
		context: "Sennit build tooling",
		tags:    []string{"go", "build"}, id: "e26c6b15b50d38d6edf89a250fbcadf9",
	}
	sessionHit = hit{
		similarity: "0.48", full: "0.4811", kind: "session", typ: "session",
		text:    "Deciding the flush cadence",
		context: "Worked out why the one-hour cap exists: it bounds how long a turn lives only on the device. Cost is controlled by repack instead.",
		tags:    []string{"storage", "flush"}, id: "9c41e07b2d5a4f86a1e3b0c7d8f25e61",
		holds: "4 messages in 1 chunk",
	}
)

func threeHits() []hit { return []hit{progressHit, grantHit, tmpdirHit} }

// searched is a screen after a search has finished, with the elapsed time the
// previews were drawn with.
func searched(t *testing.T, columns int, elapsed time.Duration, final string) *screen {
	t.Helper()
	s := terminal(t, columns)
	start := time.Now()
	first := true
	s.out.clock = func() time.Time {
		if first {
			first = false
			return start
		}
		return start.Add(elapsed)
	}
	s.out.done(ui.MarkSuccess, final)
	return s
}

const eleven = 11100 * time.Millisecond

// The table as decided, at the width the preview was drawn at.
func TestTheDecidedTable(t *testing.T) {
	s := terminal(t, 81)
	lines := recallTable(threeHits(), 81, false, false)
	if got, want := uitest.Strip(rendered(s, lines)), fixture(t, "recall-table.txt"); got != want {
		t.Errorf("the table:\n got %q\nwant %q", got, want)
	}
	// The header is the only styled part of it.
	painted := rendered(s, lines)
	if !strings.HasPrefix(painted, "\x1b[1mMATCH") {
		t.Errorf("the header is not bold: %q", painted)
	}
	if strings.Count(painted, "\x1b[") != 2 {
		t.Errorf("something other than the header is styled: %q", painted)
	}
}

// A search on a terminal: the past-tense line, a blank, and the table.
func TestARecallOnATerminal(t *testing.T) {
	for _, c := range []struct {
		name, file string
		hits       []hit
		final      string
		memoryText bool
		notes      []ui.Message
	}{
		{name: "with a conversation", file: "recall-with-conversation.txt",
			hits:  []hit{progressHit, sessionHit, tmpdirHit},
			final: "Found 2 memories and 1 conversation"},
		{name: "expanded", file: "recall-memory-text.txt",
			hits: []hit{progressHit, sessionHit, tmpdirHit}, memoryText: true,
			final: "Found 2 memories and 1 conversation"},
		{name: "superseded versions held back", file: "recall-superseded.txt",
			hits: threeHits(), final: "Found 3 memories",
			notes: []ui.Message{{Kind: ui.KindHint, Text: "1 superseded version held back; `--history` returns it"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := searched(t, 81, eleven, c.final)
			s.out.block(recallTable(c.hits, 81, c.memoryText, false))
			for _, note := range c.notes {
				s.out.note(note)
			}
			s.assert(t, c.name, fixture(t, c.file))
		})
	}
}

// Ranking notes and a record the vector pass never scored.
func TestRankingNotesAndARecordFoundByItsWordsAlone(t *testing.T) {
	grant := grantHit
	grant.note = "also ranked up by words it shares with the query and the tags and types asked for"
	words := hit{
		similarity: "-", full: "0.0000", kind: "memory", typ: "fact",
		text: "Budget reviews happen every quarter", context: "Grant planning",
		tags: []string{"grant"}, id: "5b0f3c9ad2e14f7c8b6a1d0e9f3c2a71",
		note: "found only by words it shares with the query",
	}
	s := searched(t, 81, eleven, "Found 2 memories")
	s.out.block(recallTable([]hit{grant, words}, 81, true, false))
	s.assert(t, "notes", fixture(t, "recall-notes.txt"))
}

// An empty vault is a different answer from no match, and says so.
func TestAnEmptyVaultSaysSo(t *testing.T) {
	s := searched(t, 80, 400*time.Millisecond, "No records matched")
	s.out.note(ui.Message{Kind: ui.KindHint,
		Text: "this vault holds nothing searchable yet; `sennit remember` stores the first record"})
	s.assert(t, "empty vault", fixture(t, "recall-empty-vault.txt"))
}

// The table at three widths: the MEMORY column takes what the others leave, and
// below twenty-four cells of it the tags are dropped rather than the statement
// cut to nothing.
func TestTheTableAtEachWidth(t *testing.T) {
	for _, c := range []struct {
		columns int
		file    string
	}{{60, "recall-60-columns.txt"}, {80, "recall-80-columns.txt"}, {120, "recall-120-columns.txt"}} {
		s := terminal(t, c.columns)
		got := uitest.Strip(rendered(s, recallTable(threeHits(), c.columns, false, false)))
		if want := fixture(t, c.file); got != want {
			t.Errorf("width %d:\n got %q\nwant %q", c.columns, got, want)
		}
		for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
			// A row uses at most one cell less than the width. The spare cell is
			// what a cut statement's ellipsis takes where a terminal draws that
			// character two cells wide, so the columns still line up there.
			if cells := width.String(line); cells > c.columns-1 {
				t.Errorf("width %d: a line of %d cells: %q", c.columns, cells, line)
			}
			if cells := width.StringWith(line, 2); cells > c.columns {
				t.Errorf("width %d: a line of %d cells where ambiguous characters draw wide: %q", c.columns, cells, line)
			}
		}
	}
}

// --verbose adds the read tier as a last column.
func TestTheVerboseTable(t *testing.T) {
	hits := threeHits()
	hits[0].read, hits[1].read, hits[2].read = "local 6 ms", "local 0.12 ms", "cached 42 ms"
	s := searched(t, 101, eleven, "Found 3 memories")
	s.out.block(recallTable(hits, 101, false, true))
	s.assert(t, "verbose", fixture(t, "recall-verbose.txt"))
}

// What a pipe receives: no header, nothing cut off, and every field escaped so
// it can never break a line or invent a column.
func TestThePipedForm(t *testing.T) {
	if got, want := uitest.Lines(recallTSV(threeHits())...), fixture(t, "recall-piped.txt"); got != want {
		t.Errorf("piped:\n got %q\nwant %q", got, want)
	}
	awkward := hit{full: "0.1000", typ: "fact", tags: []string{"a"}, id: "x",
		text: "one\ttwo\nthree\\four\rfive", context: "and\there"}
	line := recallTSV([]hit{awkward})[0]
	if strings.Count(line, "\t") != 5 {
		t.Errorf("a field broke the columns: %q", line)
	}
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("a field broke the line: %q", line)
	}
}

func TestTheFinalLineCountsBothKindsOfRecord(t *testing.T) {
	for _, c := range []struct {
		memories, conversations int
		want                    string
	}{
		{0, 0, "No records matched"},
		{1, 0, "Found 1 memory"},
		{3, 0, "Found 3 memories"},
		{0, 1, "Found 1 conversation"},
		{2, 1, "Found 2 memories and 1 conversation"},
		{1, 2, "Found 1 memory and 2 conversations"},
	} {
		if got := found(c.memories, c.conversations); got != c.want {
			t.Errorf("found(%d, %d) = %q, want %q", c.memories, c.conversations, got, c.want)
		}
	}
}
