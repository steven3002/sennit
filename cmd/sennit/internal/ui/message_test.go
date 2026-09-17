package ui

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

func render(lines []Line, color bool) string {
	painter := Painter{Color: color}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = painter.Render(line)
	}
	return uitest.Lines(out...)
}

// blocks lays out several messages the way a command prints them: a blank line
// between blocks.
func blocks(badges bool, termWidth int, messages ...Message) []Line {
	var out []Line
	for i, m := range messages {
		if i > 0 {
			out = append(out, Line{})
		}
		out = append(out, m.Lines(badges, termWidth)...)
	}
	return out
}

// check asserts a rendering byte for byte with colour on, and that the same
// lines with colour off are the expectation's plain text.
func check(t *testing.T, name string, lines []Line, markup string) {
	t.Helper()
	if got, want := render(lines, true), uitest.Colored(markup); got != want {
		t.Errorf("%s, colour on:\n got %q\nwant %q", name, got, want)
	}
	if got, want := render(lines, false), uitest.Plain(markup); got != want {
		t.Errorf("%s, colour off:\n got %q\nwant %q", name, got, want)
	}
}

var noPhrase = Message{
	Kind:        KindError,
	Text:        "no recovery phrase",
	Explanation: []string{"Sennit derives the vault's keys from the phrase on every run and never stores it."},
	Hints: []string{
		"set SENNIT_PHRASE, or pipe the phrase in on stdin",
		"no phrase yet? `sennit init --new-phrase` prints one and stores nothing",
	},
}

// The badge preview as the owner selected it, which shortened one hint. The
// rules reproduce it at every width from 65 up.
func TestBadgesReproduceTheSelectedPreview(t *testing.T) {
	short := noPhrase
	short.Hints = []string{noPhrase.Hints[0], "no phrase yet? `sennit init --new-phrase` prints one"}
	degraded := Message{Kind: KindWarning, Text: "the indexer did not answer, this run works from this device only"}
	want := uitest.Lines(
		"[[badge-ERROR: ERROR ]] no recovery phrase",
		"  Sennit derives the vault's keys from the phrase on every run",
		"  and never stores it.",
		"",
		"[[badge-HINT: HINT ]]  set SENNIT_PHRASE, or pipe the phrase in on stdin",
		"[[badge-HINT: HINT ]]  no phrase yet? `sennit init --new-phrase` prints one",
		"",
		"[[badge-WARNING: WARNING ]] the indexer did not answer, this run works from this",
		"          device only",
	)
	for _, termWidth := range []int{65, 80, 120, 200} {
		check(t, "preview", blocks(true, termWidth, short, degraded), want)
	}
	plain := uitest.Lines(
		"error: no recovery phrase",
		"",
		"  Sennit derives the vault's keys from the phrase on every run and never stores it.",
		"",
		"hint: set SENNIT_PHRASE, or pipe the phrase in on stdin",
		"hint: no phrase yet? `sennit init --new-phrase` prints one",
		"",
		"warning: the indexer did not answer, this run works from this device only",
	)
	check(t, "labels", blocks(false, 80, short, degraded), plain)
}

// The labels the owner selected for a stream that is not a terminal, with the
// hint that says nothing is stored.
func TestLabelsReproduceTheSelectedFallback(t *testing.T) {
	check(t, "labels", noPhrase.Lines(false, 80), uitest.Lines(
		"error: no recovery phrase",
		"",
		"  Sennit derives the vault's keys from the phrase on every run and never stores it.",
		"",
		"hint: set SENNIT_PHRASE, or pipe the phrase in on stdin",
		"hint: no phrase yet? `sennit init --new-phrase` prints one and stores nothing",
	))
}

// A block lines up on its widest badge, so a warning's hints start where the
// warning's text does.
func TestABlockLinesUpOnItsWidestBadge(t *testing.T) {
	repack := Message{Kind: KindWarning, Text: "reclaimable storage has built up",
		Explanation: []string{"Left alone it keeps accumulating until a write fails for want of room."},
		Hints:       []string{"`sennit reclaim --repack` returns it"}}
	check(t, "per block", blocks(true, 80, noPhrase, repack), uitest.Lines(
		"[[badge-ERROR: ERROR ]] no recovery phrase",
		"  Sennit derives the vault's keys from the phrase on every run",
		"  and never stores it.",
		"",
		"[[badge-HINT: HINT ]]  set SENNIT_PHRASE, or pipe the phrase in on stdin",
		"[[badge-HINT: HINT ]]  no phrase yet? `sennit init --new-phrase` prints one and",
		"        stores nothing",
		"",
		"[[badge-WARNING: WARNING ]] reclaimable storage has built up",
		"  Left alone it keeps accumulating until a write fails for want",
		"  of room.",
		"",
		"[[badge-HINT: HINT ]]    `sennit reclaim --repack` returns it",
	))
	check(t, "per block, labels", blocks(false, 80, noPhrase, repack), uitest.Lines(
		"error: no recovery phrase",
		"",
		"  Sennit derives the vault's keys from the phrase on every run and never stores it.",
		"",
		"hint: set SENNIT_PHRASE, or pipe the phrase in on stdin",
		"hint: no phrase yet? `sennit init --new-phrase` prints one and stores nothing",
		"",
		"warning: reclaimable storage has built up",
		"",
		"  Left alone it keeps accumulating until a write fails for want of room.",
		"",
		"hint: `sennit reclaim --repack` returns it",
	))
}

func TestAHintLeadingItsOwnBlockStartsAtColumnEight(t *testing.T) {
	check(t, "hint", Message{Kind: KindHint, Text: "`sennit reclaim --repack` returns it"}.Lines(true, 80),
		uitest.Lines("[[badge-HINT: HINT ]]  `sennit reclaim --repack` returns it"))
}

// On a narrow terminal a message wraps to the width rather than the measure, and
// a data line is cut rather than wrapped.
func TestAMessageOnANarrowTerminal(t *testing.T) {
	m := Message{Kind: KindWarning, Text: "a very similar memory is already in this vault",
		Data: []string{"Prefers one live status line over scrolling logs"}}
	var painter Painter
	lines := m.Lines(true, 40)
	for _, line := range lines {
		if cells := width.String(uitest.Strip(painter.Render(line))); cells > 39 {
			t.Errorf("a line of %d cells at width 40: %q", cells, painter.Render(line))
		}
	}
	if got := painter.Render(lines[len(lines)-1]); !strings.HasSuffix(got, "…") {
		t.Errorf("the data line was not cut: %q", got)
	}
}

func TestACodeSpanIsNeverBroken(t *testing.T) {
	got := Wrap("load the key into this shell without putting it in your history: `export SENNIT_APP_KEY=$(cat sennit.key)`", 8, 64)
	want := []string{"load the key into this shell without putting it in your", "history: `export SENNIT_APP_KEY=$(cat sennit.key)`"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBadgeColours(t *testing.T) {
	lines := []Line{
		{badge(KindError), T(" an error")},
		{badge(KindWarning), T(" a warning")},
		{badge(KindHint), T("  a hint")},
	}
	check(t, "badges", lines, uitest.Lines(
		"[[badge-ERROR: ERROR ]] an error",
		"[[badge-WARNING: WARNING ]] a warning",
		"[[badge-HINT: HINT ]]  a hint",
	))
}

func TestKeyValuesAlignOnTheWidestLabel(t *testing.T) {
	got := strings.Join(KeyValues([][2]string{{"Vault", "~/.sennit"}, {"Records", "3 queued"}, {"", ""}, {"Sia", "offline"}}), "\n")
	want := "Vault    ~/.sennit\nRecords  3 queued\n\nSia      offline"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1284: "1,284", 12480: "12,480", 1234567: "1,234,567", -1284: "-1,284"} {
		if got := Commas(n); got != want {
			t.Errorf("Commas(%d) = %q, want %q", n, got, want)
		}
	}
}
