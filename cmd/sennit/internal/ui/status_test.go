package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func liveText(t *testing.T, name string, line Line, markup string) {
	t.Helper()
	painter := Painter{Color: true}
	if got, want := painter.Render(line), uitest.Colored(markup); got != want {
		t.Errorf("%s, colour on:\n got %q\nwant %q", name, got, want)
	}
	if got, want := (Painter{}).Render(line), uitest.Plain(markup); got != want {
		t.Errorf("%s, colour off:\n got %q\nwant %q", name, got, want)
	}
}

// The states of one live line, as the design was approved. The glyphs here are
// the ones those samples were drawn with; the cycle the product animates is
// checked separately, and the text and layout are what this pins.
func TestTheApprovedLiveStates(t *testing.T) {
	liveText(t, "unlocking", LiveLine("✢", "Unlocking vault", "", 0, false),
		"[[glyph:✢]] Unlocking vault…")
	liveText(t, "connecting", LiveLine("✶", "Connecting to Sia", "", 4*time.Second, false),
		"[[glyph:✶]] Connecting to Sia… [[faint:(4s · ctrl+c to cancel)]]")
	liveText(t, "searching", LiveLine("✻", "Searching 1,284 memories", "", 10*time.Second, false),
		"[[glyph:✻]] Searching 1,284 memories… [[faint:(10s · ctrl+c to cancel)]]")
	liveText(t, "found", FinalLine(MarkSuccess, "Found 3 memories", FinalElapsed(seconds(11.1))),
		"[[ok:✓]] Found 3 memories [[faint:(11.1s)]]")
	liveText(t, "uploading", LiveLine("✶", "Uploading to Sia", "40%", 86*time.Second, false),
		"[[glyph:✶]] Uploading to Sia… [[faint:(40% · 1m26s · ctrl+c to cancel)]]")
}

// The three states a remember ends in, in the time format decision 2B fixed.
func TestTheThreeRememberEndStates(t *testing.T) {
	liveText(t, "stored", FinalLine(MarkSuccess, "Remembered, and stored on Sia", FinalElapsed(seconds(204))),
		"[[ok:✓]] Remembered, and stored on Sia [[faint:(3m24s)]]")
	liveText(t, "queued", FinalLine(MarkSuccess, "Remembered on this device, queued for Sia", FinalElapsed(seconds(0.6))),
		"[[ok:✓]] Remembered on this device, queued for Sia [[faint:(0.6s)]]")
	liveText(t, "device only", FinalLine(MarkWarning,
		"Remembered on this device only: the indexer did not answer, the next connected flush uploads it", FinalElapsed(seconds(0.6))),
		"[[warn:!]] Remembered on this device only: the indexer did not answer, the next connected flush uploads it [[faint:(0.6s)]]")
}

// The detail appears as it becomes true: the elapsed time from a second, the way
// out from three, a percentage only where something counts it.
func TestWhenTheDetailsAppear(t *testing.T) {
	for _, c := range []struct {
		line   Line
		markup string
	}{
		{LiveLine(".", "Unlocking vault", "", seconds(0.4), false), "[[glyph:.]] Unlocking vault…"},
		{LiveLine("◝", "Connecting to Sia", "", seconds(1.2), false), "[[glyph:◝]] Connecting to Sia… [[faint:(1s)]]"},
		{LiveLine("၀", "Connecting to Sia", "", seconds(3), false), "[[glyph:၀]] Connecting to Sia… [[faint:(3s · ctrl+c to cancel)]]"},
		{LiveLine("㊂", "Uploading to Sia", "12%", seconds(59.9), false), "[[glyph:㊂]] Uploading to Sia… [[faint:(12% · 59s · ctrl+c to cancel)]]"},
		{LiveLine("◍", "Uploading to Sia", "97%", seconds(65), false), "[[glyph:◍]] Uploading to Sia… [[faint:(97% · 1m05s · ctrl+c to cancel)]]"},
		{LiveLine("ဝ", "Restoring records", "8,200 of 10,000", seconds(3725), false), "[[glyph:ဝ]] Restoring records… [[faint:(8,200 of 10,000 · 1h02m · ctrl+c to cancel)]]"},
	} {
		liveText(t, "detail", c.line, c.markup)
	}
}

func TestTimeFormats(t *testing.T) {
	for _, c := range []struct {
		seconds     float64
		live, final string
	}{
		{0.6, "0s", "0.6s"},
		{4, "4s", "4.0s"},
		{59.9, "59s", "59.9s"},
		{65, "1m05s", "1m05s"},
		{204, "3m24s", "3m24s"},
		{3725, "1h02m", "1h02m"},
	} {
		if got := LiveElapsed(seconds(c.seconds)); got != c.live {
			t.Errorf("live %.1fs: got %q, want %q", c.seconds, got, c.live)
		}
		if got := FinalElapsed(seconds(c.seconds)); got != c.final {
			t.Errorf("final %.1fs: got %q, want %q", c.seconds, got, c.final)
		}
	}
}

// A step that cannot be interrupted keeps running after Ctrl-C, so the line says
// so rather than going on offering a way out the user has already taken.
func TestTheCancellingState(t *testing.T) {
	liveText(t, "cancelling", LiveLine("◌", "Connecting to Sia", "", seconds(6), true),
		"[[glyph:◌]] Cancelling while connecting to Sia… [[faint:(6s)]]")
	liveText(t, "cancelled", FinalLine(MarkFailure, "Cancelled while uploading to Sia", FinalElapsed(seconds(86))),
		"[[err:✗]] Cancelled while uploading to Sia [[faint:(1m26s)]]")
}

// Without Unicode the marks and the separators fall back to characters every
// terminal can draw.
func TestTheASCIIFallback(t *testing.T) {
	painter := Painter{Color: true, Tier: TierASCII}
	for _, c := range []struct {
		line Line
		want string
	}{
		{LiveLine("/", "Connecting to Sia", "", seconds(4), false), "[[glyph:/]] Connecting to Sia... [[faint:(4s, ctrl+c to cancel)]]"},
		{LiveLine("-", "Uploading to Sia", "40%", seconds(86), false), "[[glyph:-]] Uploading to Sia... [[faint:(40%, 1m26s, ctrl+c to cancel)]]"},
		{FinalLine(MarkSuccess, "Found 3 memories", FinalElapsed(seconds(11.1))), "[[ok:ok]] Found 3 memories [[faint:(11.1s)]]"},
		{FinalLine(MarkFailure, "Cancelled while uploading to Sia", FinalElapsed(seconds(86))), "[[err:x]] Cancelled while uploading to Sia [[faint:(1m26s)]]"},
	} {
		if got, want := painter.Render(c.line), uitest.Colored(c.want); got != want {
			t.Errorf("ascii:\n got %q\nwant %q", got, want)
		}
	}
	if got := (Painter{Tier: TierBasic}).Render(FinalLine(MarkSuccess, "Connected", "9.1s")); got != "√ Connected (9.1s)" {
		t.Errorf("basic mark: %q", got)
	}
}

func drawn(t *testing.T, opts StatusOptions, phase, detail string, frame int) string {
	t.Helper()
	var out strings.Builder
	opts.Out = &out
	opts.Animate = true
	opts.Ticks = func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }
	s := NewStatus(opts)
	s.Phase(phase)
	if detail != "" {
		s.Detail(detail)
	}
	for range frame {
		s.Tick()
	}
	text := out.String()
	// Only the last frame matters: each one redraws the same line.
	if i := strings.LastIndex(text, beginSync); i >= 0 {
		text = text[i:]
	}
	return text
}

// Every frame fills a slot as wide as the widest in its cycle, and the text is
// pinned to the column after it, so the words never move sideways whatever width
// a terminal gives a glyph.
func TestEveryFrameFillsTheSlotAndPinsTheText(t *testing.T) {
	for tier, column := range map[Tier]int{TierFull: 4, TierBasic: 4, TierASCII: 3} {
		cycle := tier.Frames()
		for i, glyph := range cycle {
			opts := StatusOptions{Painter: Painter{Tier: tier}, Width: func() int { return 80 }, Now: fixedClock()}
			got := drawn(t, opts, "Connecting to Sia", "", i)
			phase := (Painter{Tier: tier}).Glyphs("Connecting to Sia…")
			pad := strings.Repeat(" ", column-1-width.String(glyph))
			want := beginSync + "\r" + glyph + pad + "\x1b[" + string(rune('0'+column)) + "G" + phase + eraseLine + endSync
			if got != want {
				t.Errorf("tier %d frame %q:\n got %q\nwant %q", tier, glyph, got, want)
			}
		}
	}
}

// The line is cut to one cell short of the width, counting ambiguous characters
// as two, so it can never wrap onto a second row.
func TestTheLineNeverExceedsTheWidth(t *testing.T) {
	for _, termWidth := range []int{20, 40, 80} {
		for tier := range map[Tier]bool{TierFull: true, TierBasic: true, TierASCII: true} {
			for i := range tier.Frames() {
				opts := StatusOptions{Painter: Painter{Tier: tier, Color: true}, Width: func() int { return termWidth }, Now: clockAt(4 * time.Second)}
				got := drawn(t, opts, "Connecting to Sia", "", i)
				visible := strings.TrimPrefix(uitest.Strip(got), "\r")
				if cells := width.StringWith(visible, 2); cells > termWidth-1 {
					t.Errorf("width %d, tier %d, frame %d: %d cells: %q", termWidth, tier, i, cells, visible)
				}
			}
		}
	}
}

// A statement in another script must not widen the line either.
func TestAWideOrEmojiPhaseIsStillCutToTheWidth(t *testing.T) {
	for _, phase := range []string{"Searching 日本語のメモを保存する memories", "Uploading 🚀🚀🚀 to Sia", "Reading ⚠\uFE0F⚠\uFE0F from Sia"} {
		for _, termWidth := range []int{20, 40, 80} {
			opts := StatusOptions{Painter: Painter{Tier: TierFull}, Width: func() int { return termWidth }, Now: clockAt(4 * time.Second)}
			got := drawn(t, opts, phase, "", 3)
			visible := strings.TrimPrefix(uitest.Strip(got), "\r")
			if cells := width.StringWith(visible, 2); cells > termWidth-1 {
				t.Errorf("width %d, %q: %d cells: %q", termWidth, phase, cells, visible)
			}
		}
	}
}

func fixedClock() func() time.Time {
	start := time.Now()
	return func() time.Time { return start }
}

func clockAt(elapsed time.Duration) func() time.Time {
	start := time.Now()
	first := true
	return func() time.Time {
		if first {
			first = false
			return start
		}
		return start.Add(elapsed)
	}
}

func animated(out *strings.Builder, interrupted <-chan struct{}) *Status {
	return NewStatus(StatusOptions{
		Out: out, Animate: true, Painter: Painter{Color: true}, Width: func() int { return 80 },
		Interrupted: interrupted, Now: fixedClock(),
		Ticks: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} },
	})
}

// However a run ends, the cursor comes back.
func TestTheCursorIsAlwaysRestored(t *testing.T) {
	for name, end := range map[string]func(*Status){
		"success":   func(s *Status) { s.Done(MarkSuccess, "Found 3 memories") },
		"failure":   func(s *Status) { s.Fail("The memory is saved on this device. It has not reached Sia yet.") },
		"interrupt": func(s *Status) { s.Cancel("The memory is saved on this device. It has not reached Sia yet.") },
		"cleared":   func(s *Status) { s.Clear() },
	} {
		var out strings.Builder
		s := animated(&out, nil)
		s.Phase("Uploading to Sia")
		end(s)
		text := out.String()
		if !strings.HasPrefix(text, hideCursor) {
			t.Errorf("%s: the cursor was not hidden: %q", name, text)
		}
		if !strings.HasSuffix(text, showCursor) {
			t.Errorf("%s: the run does not end by restoring the cursor: %q", name, text)
		}
		if strings.Count(text, showCursor) != 1 {
			t.Errorf("%s: the cursor was restored %d times", name, strings.Count(text, showCursor))
		}
	}
}

func TestAnInterruptSwitchesTheLineToCancelling(t *testing.T) {
	var out strings.Builder
	interrupted := make(chan struct{})
	s := animated(&out, interrupted)
	s.Phase("Connecting to Sia")
	close(interrupted)
	s.Tick()
	if !strings.Contains(uitest.Strip(out.String()), "Cancelling while connecting to Sia…") {
		t.Errorf("the line does not say it is cancelling: %q", uitest.Strip(out.String()))
	}
	s.Cancel()
	if got := uitest.Strip(out.String()); !strings.HasSuffix(got, "Cancelled while connecting to Sia (0.0s)\n") {
		t.Errorf("the cancel line is wrong: %q", got)
	}
}

// A warning that arrives mid-run is printed above the line, which is drawn again
// underneath it.
func TestAMessageMidRunIsPrintedAboveTheLine(t *testing.T) {
	var out strings.Builder
	s := animated(&out, nil)
	s.Phase("Connecting to Sia")
	s.Above("warning: the indexer did not answer")
	s.Done(MarkSuccess, "Done")
	text := out.String()
	above := strings.Index(text, "warning:")
	if above < 0 {
		t.Fatalf("the warning was not printed: %q", text)
	}
	if !strings.Contains(text[:above], "\r"+eraseLine) {
		t.Error("the live line was not cleared before the warning")
	}
	if !strings.Contains(text[above:], "Connecting to Sia…") {
		t.Error("the live line was not drawn again under the warning")
	}
}

// Not animating: a line per phase, a heartbeat after ten quiet seconds, and one
// past-tense line at the end. This is what a pipe, a log and CI receive.
func TestThePlainRun(t *testing.T) {
	var out strings.Builder
	now := time.Now()
	clock := func() time.Time { return now }
	s := NewStatus(StatusOptions{
		Out: &out, Painter: Painter{}, Now: clock,
		Ticks: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} },
	})
	for _, phase := range []string{"Unlocking vault", "Loading embedding model", "Loading search index", "Connecting to Sia", "Embedding and encrypting", "Uploading to Sia"} {
		s.Phase(phase)
	}
	s.Detail("87%")
	now = now.Add(20 * time.Second)
	s.Tick()
	s.Phase("Pinning on Sia")
	now = now.Add(4900 * time.Millisecond)
	s.Done(MarkSuccess, "Remembered, and stored on Sia")

	want := uitest.Lines(
		"Unlocking vault...",
		"Loading embedding model...",
		"Loading search index...",
		"Connecting to Sia...",
		"Embedding and encrypting...",
		"Uploading to Sia...",
		"Still uploading to Sia (20s elapsed, 87%)",
		"Pinning on Sia...",
		"Remembered, and stored on Sia in 24.9s",
	)
	if got := out.String(); got != want {
		t.Errorf("plain run:\n got %q\nwant %q", got, want)
	}
	if uitest.HasEscape(out.String()) {
		t.Error("a plain run wrote an escape byte")
	}
}

func TestThePlainRunSaysWhatAnInterruptLeft(t *testing.T) {
	var out strings.Builder
	now := time.Now()
	s := NewStatus(StatusOptions{
		Out: &out, Painter: Painter{}, Now: func() time.Time { return now },
		Ticks: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} },
	})
	s.Phase("Uploading to Sia")
	now = now.Add(86 * time.Second)
	s.Cancel("The memory is saved on this device. It has not reached Sia yet.")
	want := uitest.Lines(
		"Uploading to Sia...",
		"Cancelled while uploading to Sia after 1m26s",
		"  The memory is saved on this device. It has not reached Sia yet.",
	)
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// An interrupt before the first phase has nothing to name, and says so without
// leaving a hole in the sentence.
func TestAnInterruptBeforeAnyPhase(t *testing.T) {
	var out strings.Builder
	s := NewStatus(StatusOptions{Out: &out, Painter: Painter{}, Now: fixedClock(),
		Ticks: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }})
	s.Cancel()
	if got, want := out.String(), "Cancelled after 0.0s\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAPhaseRepeatedChangesNothing(t *testing.T) {
	var out strings.Builder
	s := NewStatus(StatusOptions{Out: &out, Painter: Painter{}, Now: fixedClock(),
		Ticks: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }})
	s.Phase("Pinning on Sia")
	s.Phase("Pinning on Sia")
	if got, want := out.String(), "Pinning on Sia...\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
