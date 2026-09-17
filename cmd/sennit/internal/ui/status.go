package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

const (
	// FrameInterval is how often the live line's glyph advances.
	FrameInterval = 120 * time.Millisecond
	// StillEvery is how long a run that is not animating goes without printing
	// a line before it says it is still working. A silent wait of more than ten
	// seconds reads as a hang.
	StillEvery = 10 * time.Second
	// heartbeatInterval is how often a plain run checks whether it owes a Still
	// line.
	heartbeatInterval = time.Second

	// The elapsed time appears after a second, when a wait has become
	// noticeable, and the way out after three, when it has become a wait.
	elapsedFrom = time.Second
	hintFrom    = 3 * time.Second
)

const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	beginSync  = "\x1b[?2026h"
	endSync    = "\x1b[?2026l"
	eraseLine  = "\x1b[K"
)

// StatusOptions configure a status line.
type StatusOptions struct {
	// Out is where the line is drawn: stderr, so that results on stdout stay
	// clean for whatever reads them.
	Out io.Writer
	// Animate redraws one line in place. Without it every phase is a line of its
	// own, which is what a pipe, a log or a screen reader can follow.
	Animate bool
	// Painter is stderr's colour and glyph tier.
	Painter Painter
	// Width reports the terminal's width in cells, read again for every frame so
	// a resized window is followed. Zero or less means unknown.
	Width func() int
	// Interrupted is closed when the run is interrupted.
	Interrupted <-chan struct{}
	// Now reads the clock. Nil means time.Now.
	Now func() time.Time
	// Ticks starts the clock that advances frames and the heartbeat, returning
	// its channel and a function that stops it. Nil means a real ticker. A caller
	// that drives the line itself returns a nil channel and calls Tick.
	Ticks func(time.Duration) (<-chan time.Time, func())
}

// A Status is the one line that says what a command is doing while it waits.
//
// It owns stderr while it runs. Everything else written to stderr in that time
// goes through Above, which lifts the line out of the way, prints, and puts it
// back, so a warning never lands in the middle of a half-drawn frame. It ends in
// exactly one of Done, Fail, Cancel or Clear, and every one of them gives the
// cursor back.
type Status struct {
	opts StatusOptions

	mu         sync.Mutex
	started    time.Time
	phase      string
	detail     string
	frame      int
	hidden     bool
	lastLine   time.Time
	cancelling bool
	ended      bool

	stop    chan struct{}
	stopped chan struct{}
}

// NewStatus prepares a status line. Its clock starts now, so the time on the
// final line covers the whole command rather than the first phase onwards.
func NewStatus(opts StatusOptions) *Status {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Width == nil {
		opts.Width = func() int { return 0 }
	}
	if opts.Ticks == nil {
		opts.Ticks = func(d time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(d)
			return ticker.C, ticker.Stop
		}
	}
	return &Status{opts: opts, started: opts.Now()}
}

// Elapsed reports how long the command has run.
func (s *Status) Elapsed() time.Duration { return s.opts.Now().Sub(s.started) }

// Current reports the phase on screen, empty before the first.
func (s *Status) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// Phase starts a phase, named as a sentence: "Uploading to Sia". A phase that is
// already current changes nothing, so a caller may report one more than once.
func (s *Status) Phase(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || text == s.phase {
		return
	}
	s.phase, s.detail = text, ""
	if s.opts.Animate {
		if !s.hidden {
			s.write(hideCursor)
			s.hidden = true
		}
		s.draw()
	} else {
		s.writeLine(text + "...")
	}
	s.startClock()
}

// Detail sets the current phase's countable progress, such as "40%". It is
// drawn with the next frame, and repeated in a plain run's Still line.
func (s *Status) Detail(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		s.detail = text
	}
}

// Above prints lines above the live line, which is then drawn again beneath
// them. The lines are printed as given, already rendered.
func (s *Status) Above(lines ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	redraw := s.opts.Animate && s.hidden && !s.ended
	if redraw {
		s.write("\r" + eraseLine)
	}
	for _, line := range lines {
		s.writeLine(line)
	}
	if redraw {
		s.draw()
	}
}

// Tick advances the live line by one frame, or in a plain run prints the Still
// line when one is owed. The status calls it on its own clock.
func (s *Status) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.phase == "" {
		return
	}
	if s.opts.Animate {
		s.frame++
		if !s.cancelling && s.opts.Interrupted != nil {
			select {
			case <-s.opts.Interrupted:
				s.cancelling = true
			default:
			}
		}
		s.draw()
		return
	}
	now := s.opts.Now()
	if now.Sub(s.lastLine) < StillEvery {
		return
	}
	detail := LiveElapsed(now.Sub(s.started)) + " elapsed"
	if s.detail != "" {
		detail += ", " + s.detail
	}
	s.writeLine("Still " + LowerFirst(s.phase) + " (" + detail + ")")
}

// Done replaces the line with the command's outcome, in the past tense.
func (s *Status) Done(mark Mark, text string) {
	s.finish(func(elapsed string) {
		if !s.opts.Animate {
			s.writeLine(text + " in " + elapsed)
			return
		}
		s.write("\r" + eraseLine + s.opts.Painter.Render(FinalLine(mark, text, elapsed)) + "\n")
	})
}

// FinalLine is a finished command's line on a terminal: its mark, what
// happened, and how long it took.
func FinalLine(mark Mark, text, elapsed string) Line {
	return Line{mark.span(), T(" " + text + " "), S(Faint, "("+elapsed+")")}
}

// Fail replaces the line with the phase that failed and what state the failure
// left, one sentence to a line.
func (s *Status) Fail(explanation ...string) { s.interrupted("Failed", explanation) }

// Cancel replaces the line with the phase an interrupt stopped and what state it
// left.
func (s *Status) Cancel(explanation ...string) { s.interrupted("Cancelled", explanation) }

func (s *Status) interrupted(verb string, explanation []string) {
	s.finish(func(elapsed string) {
		// Before the first phase there is nothing to name, which is where an
		// interrupt during the phrase or the key lands.
		what := verb
		if s.phase != "" {
			what += " while " + LowerFirst(s.phase)
		}
		if s.opts.Animate {
			s.write("\r" + eraseLine + s.opts.Painter.Render(FinalLine(MarkFailure, what, elapsed)) + "\n")
		} else {
			s.writeLine(what + " after " + elapsed)
		}
		for _, line := range explanation {
			s.writeLine("  " + line)
		}
	})
}

// Clear removes the line and says nothing, for an outcome told some other way.
func (s *Status) Clear() {
	s.finish(func(string) {
		if s.opts.Animate && s.hidden {
			s.write("\r" + eraseLine)
		}
	})
}

// finish stops the clock, writes the last line and gives the cursor back.
//
// The clock is stopped without holding the lock, because a tick in progress is
// waiting for it; marking the line ended first means that tick draws nothing.
func (s *Status) finish(last func(elapsed string)) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	stop, stopped := s.stop, s.stopped
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		<-stopped
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	last(FinalElapsed(s.Elapsed()))
	if s.hidden {
		s.write(showCursor)
		s.hidden = false
	}
}

// draw paints one frame. The caller holds the lock.
//
// Every frame sits in a slot as wide as the widest frame of its cycle, and the
// text after it is pinned to the column past the slot with CHA, so it never
// moves however wide a terminal draws a glyph: an ambiguous character drawn
// wide, or one borrowed from a fallback font that does not fit the grid. The
// line is cut to one cell less than the width, counting ambiguous characters as
// two, because a line that wraps cannot be redrawn in place. Each frame is
// bracketed as synchronized output so a terminal that supports it never shows a
// half-drawn line.
func (s *Status) draw() {
	painter := s.opts.Painter
	cycle := painter.Tier.Frames()
	glyph := cycle[s.frame%len(cycle)]
	slot := slotCells(cycle)
	column := slot + 2

	rest := LiveLine(glyph, s.phase, s.detail, s.Elapsed(), s.cancelling)[2:]
	body := painter.Render(rest)
	measured := width.Pad(glyph, slot) + " " + (Painter{Tier: painter.Tier}).Render(rest)
	if limit := columns(s.opts.Width()) - 1; width.StringWith(measured, 2) > limit {
		// A cut line keeps its glyph's colour and loses the rest of its
		// styling, which the cut could otherwise leave open.
		cut := width.Truncate(measured, limit, painter.Ellipsis(), 2)
		_, after, _ := strings.Cut(cut, " ")
		body = strings.TrimLeft(after, " ")
	}
	pad := max(column-1-width.String(glyph), 1)
	s.write(beginSync + "\r" + painter.Render(Line{S(Spinner, glyph)}) + strings.Repeat(" ", pad) +
		fmt.Sprintf("\x1b[%dG", column) + body + eraseLine + endSync)
}

// LiveLine is one frame of the live line before it is fitted to a terminal: the
// glyph, the phase as a sentence, and in parentheses the progress when there is
// a real count, the elapsed time, and how to stop.
func LiveLine(glyph, phase, detail string, elapsed time.Duration, cancelling bool) Line {
	text := phase
	var details []string
	if cancelling {
		text = "Cancelling while " + LowerFirst(phase)
	} else if detail != "" {
		details = append(details, detail)
	}
	if elapsed >= elapsedFrom {
		details = append(details, LiveElapsed(elapsed))
	}
	if !cancelling && elapsed >= hintFrom {
		details = append(details, "ctrl+c to cancel")
	}
	line := Line{S(Spinner, glyph), T(" "), T(text + "…")}
	if len(details) > 0 {
		line = append(line, T(" "), S(Faint, "("+strings.Join(details, " · ")+")"))
	}
	return line
}

// slotCells is the widest frame of a cycle, counting ambiguous characters as two
// cells as the cut does.
func slotCells(cycle []string) int {
	n := 1
	for _, frame := range cycle {
		n = max(n, width.StringWith(frame, 2))
	}
	return n
}

func columns(reported int) int {
	if reported <= 0 {
		return 80
	}
	return reported
}

func (s *Status) startClock() {
	if s.stop != nil {
		return
	}
	interval := heartbeatInterval
	if s.opts.Animate {
		interval = FrameInterval
	}
	ticks, release := s.opts.Ticks(interval)
	s.stop, s.stopped = make(chan struct{}), make(chan struct{})
	go func(stop, stopped chan struct{}) {
		defer close(stopped)
		defer release()
		for {
			select {
			case <-stop:
				return
			case <-ticks:
				s.Tick()
			}
		}
	}(s.stop, s.stopped)
}

func (s *Status) write(text string) { _, _ = io.WriteString(s.opts.Out, text) }

func (s *Status) writeLine(text string) {
	s.write(text + "\n")
	s.lastLine = s.opts.Now()
}
