package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/term"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/vault"
)

// A session is one command's output: what this terminal may be shown, the live
// status line while the command waits, and which stream each kind of line goes
// to.
//
// Results go to stdout and everything a person watches goes to stderr, so that
// `sennit recall ... | jq` gets records and nothing else while the person still
// sees what it is doing.
type session struct {
	stdout, stderr io.Writer
	// outWidth and errWidth report each stream's width in cells, read again
	// each time because a window can be resized mid-command.
	outWidth, errWidth func() int
	probe              ui.Probe
	policy             ui.Policy
	out, err           ui.Painter
	status             *ui.Status
	verbose            bool
	interrupted        <-chan struct{}
	// phrases is where a recovery phrase may come from on this run, and it is
	// the only part of the session that reads rather than writes.
	phrases phraseSource
	// clock reads the time the status line measures with. Nil means the real
	// one.
	clock func() time.Time

	// namer turns a vault phase into the sentence shown for it. Each command
	// sets its own, because the same phase is a different sentence in a flush
	// and in a repack.
	namer func(vault.Progress) (text, detail string, show bool)
	// highest is the most progress seen in the current phase. Shards are
	// reported from several goroutines and can arrive out of order, and a
	// percentage that goes backwards reads as something having gone wrong.
	highest int64

	// cancelled is what an interrupt would leave at this point in the command.
	// It grows as the command reaches states worth naming.
	cancelled []string
	// closeWarning is the vault's close failure, held back so it prints after
	// the command's own outcome rather than in front of it.
	closeWarning error
	// printed records that something has already been written as a result, so
	// an error block that follows is separated from it.
	printed bool
}

// newSession reads what this run may draw. The colour mode is settled later by
// the command's own --color flag, which is parsed after this exists.
func newSession(interrupted <-chan struct{}) (*session, func()) {
	outEscapes, restoreOut := ui.EnableEscapes(os.Stdout)
	errEscapes, restoreErr := ui.EnableEscapes(os.Stderr)
	s := &session{
		stdout: os.Stdout, stderr: os.Stderr,
		outWidth:    func() int { return columnsOf(os.Stdout) },
		errWidth:    func() int { return columnsOf(os.Stderr) },
		interrupted: interrupted,
		phrases:     terminalPhrases(interrupted),
	}
	s.probe = ui.Probe{
		StdoutTerminal: outEscapes && term.IsTerminal(int(os.Stdout.Fd())),
		StderrTerminal: errEscapes && term.IsTerminal(int(os.Stderr.Fd())),
		StderrEscapes:  errEscapes,
		GOOS:           runtime.GOOS,
		Getenv:         os.Getenv,
	}
	s.setColor(ui.ColorAuto)
	return s, func() { restoreErr(); restoreOut() }
}

// setColor applies the --color flag once it has been parsed.
func (s *session) setColor(mode ui.ColorMode) {
	s.policy = ui.Decide(mode, s.probe)
	s.out = ui.Painter{Color: s.policy.StdoutColor, Tier: s.policy.Tier}
	s.err = ui.Painter{Color: s.policy.StderrColor, Tier: s.policy.Tier}
}

// line returns the status line, starting it on first use so that a command that
// fails before it does anything prints no phase at all.
func (s *session) line() *ui.Status {
	if s.status == nil {
		s.status = ui.NewStatus(ui.StatusOptions{
			Out:         s.stderr,
			Animate:     s.policy.Animate,
			Painter:     s.err,
			Width:       s.errWidth,
			Interrupted: s.interrupted,
			Now:         s.clock,
		})
	}
	return s.status
}

// phase says what the command is doing now.
func (s *session) phase(text string) {
	s.highest = 0
	s.line().Phase(text)
}

// watch installs the phase names for one command and returns the callback the
// vault reports through.
func (s *session) watch(namer func(vault.Progress) (text, detail string, show bool)) func(vault.Progress) {
	s.namer = namer
	return s.progress
}

func (s *session) progress(p vault.Progress) {
	text, detail, show := s.namer(p)
	if !show {
		return
	}
	if text != s.line().Current() {
		s.phase(text)
	}
	if detail != "" {
		s.line().Detail(detail)
	}
}

// percent is a phase's progress as a percentage, never over a hundred and never
// backwards.
func (s *session) percent(done, total int64) string {
	if total <= 0 {
		return ""
	}
	s.highest = max(s.highest, min(done, total))
	return fmt.Sprintf("%d%%", 100*s.highest/total)
}

// counted is a phase's progress as a count of what it has finished.
func (s *session) counted(done int64, what string) string {
	if done <= 0 {
		return ""
	}
	s.highest = max(s.highest, done)
	return ui.Commas(int(s.highest)) + " " + what
}

// done ends the live line with what the command achieved.
func (s *session) done(mark ui.Mark, text string) {
	s.line().Done(mark, text)
	s.printed = true
}

// failed ends the live line with the phase that failed and what it left behind.
func (s *session) failed(explanation ...string) {
	s.line().Fail(explanation...)
	s.printed = true
}

// cancelledBy ends the live line after an interrupt, explaining the state the
// run reached rather than the phase that was on screen: an interrupted remember
// has already stored the memory, whatever the line was saying at the time.
func (s *session) cancelledBy() {
	s.line().Cancel(s.cancelled...)
	s.printed = true
}

// leaves records what an interrupt would leave from here on.
func (s *session) leaves(explanation ...string) { s.cancelled = explanation }

// clear takes the live line down without saying anything, for an outcome that
// is about to be told as a message.
func (s *session) clear() { s.line().Clear() }

// warn prints a message above the live line, for something a reader must see
// while the command is still running.
func (s *session) warn(m ui.Message) { s.line().Above(s.messageLines(m)...) }

// above prints plain lines on stderr, lifting the live line out of the way and
// putting it back underneath them.
func (s *session) above(lines ...string) { s.line().Above(lines...) }

// note prints a message after the result: advice, or a caution about what the
// result does not cover. A blank line sets each one apart from what came
// before, and the line is lifted out of the way when one still runs.
func (s *session) note(m ui.Message) {
	s.line().Above(append([]string{""}, s.messageLines(m)...)...)
	s.printed = true
}

func (s *session) messageLines(m ui.Message) []string {
	lines := m.Lines(s.policy.StderrColor, s.errWidth())
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = s.err.Render(line)
	}
	return out
}

// result prints lines of data to stdout, straight after the line that
// introduced them.
func (s *session) result(lines ...string) {
	for _, line := range lines {
		fmt.Fprintln(s.stdout, line)
	}
	s.printed = true
}

// report prints a block of labelled values to stdout, separated from the status
// line above it when they share a terminal.
func (s *session) report(rows [][2]string) {
	if s.policy.StdoutTerminal {
		fmt.Fprintln(s.stdout)
	}
	s.result(ui.KeyValues(rows)...)
}

// block prints a set of styled lines to stdout, set apart from the status line
// above them when they share a terminal. Through a pipe there is no blank line,
// because whatever reads it counts lines.
func (s *session) block(lines []ui.Line) {
	if s.policy.StdoutTerminal {
		fmt.Fprintln(s.stdout)
	}
	for _, line := range lines {
		s.styled(line)
	}
}

// styled prints one styled line to stdout.
func (s *session) styled(line ui.Line) {
	fmt.Fprintln(s.stdout, s.out.Render(line))
	s.printed = true
}

// detail prints the lines that --verbose adds, faint and on stderr, because
// they are for someone diagnosing rather than for the answer.
func (s *session) detail(lines ...string) {
	if !s.verbose {
		return
	}
	for _, line := range lines {
		fmt.Fprintln(s.stderr, s.err.Render(ui.Line{ui.S(ui.Faint, line)}))
	}
}

// columnsOf reports a stream's width in cells, falling back to the width a
// terminal has when it will not say.
func columnsOf(f *os.File) int {
	if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}
