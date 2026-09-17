package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
)

// A screen is one command's output as a person or a pipe receives it.
//
// Both streams are captured on their own and together in the order they were
// written, because a shared terminal shows one sequence of lines and which
// stream each came from is a separate question.
type screen struct {
	out        *session
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	transcript bytes.Buffer
	columns    int
}

// a stream writes to its own record and to the shared one.
type stream struct{ own, shared *bytes.Buffer }

func (s stream) Write(p []byte) (int, error) {
	s.shared.Write(p)
	return s.own.Write(p)
}

// terminal is a session drawing to a terminal: colour on, the live line
// animating, both streams the same window.
func terminal(t *testing.T, columns int) *screen {
	t.Helper()
	return sessionFor(t, columns, ui.Probe{
		StdoutTerminal: true, StderrTerminal: true, StderrEscapes: true, GOOS: "linux",
		Getenv: func(name string) string {
			if name == "LANG" {
				return "C.UTF-8"
			}
			return ""
		},
	})
}

func sessionFor(t *testing.T, columns int, probe ui.Probe) *screen {
	t.Helper()
	s := &screen{columns: columns}
	s.out = &session{
		probe:    probe,
		outWidth: func() int { return columns },
		errWidth: func() int { return columns },
	}
	s.out.stdout = stream{own: &s.stdout, shared: &s.transcript}
	s.out.stderr = stream{own: &s.stderr, shared: &s.transcript}
	s.out.setColor(ui.ColorAuto)
	return s
}

// text is what a terminal would show: the escape sequences taken out, and each
// line reduced to what survived the last carriage return, because the live line
// is drawn over itself in place.
func (s *screen) text() string {
	var out strings.Builder
	for i, line := range strings.Split(uitest.Strip(s.transcript.String()), "\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		if redrawn := strings.LastIndex(line, "\r"); redrawn >= 0 {
			line = line[redrawn+1:]
		}
		out.WriteString(line)
	}
	return out.String()
}

// assert compares what the screen showed with what was approved.
func (s *screen) assert(t *testing.T, name, want string) {
	t.Helper()
	if got := s.text(); got != want {
		t.Errorf("%s:\n got %q\nwant %q", name, got, want)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read the expected output: %v", err)
	}
	return string(text)
}

// The policy matrix, through the session a command actually writes with:
// colour appears only where the policy allows colour, and the cursor and erase
// sequences the live line needs appear only where it animates.
func TestWhatReachesEachStreamOverThePolicyMatrix(t *testing.T) {
	for _, c := range []struct {
		name             string
		terminals        bool
		mode             ui.ColorMode
		env              map[string]string
		color, animation bool
	}{
		{name: "piped", mode: ui.ColorAuto},
		{name: "a terminal", terminals: true, mode: ui.ColorAuto, color: true, animation: true},
		{name: "NO_COLOR keeps the line and drops the colour", terminals: true, mode: ui.ColorAuto,
			env: map[string]string{"NO_COLOR": "1"}, animation: true},
		{name: "FORCE_COLOR through a pipe", mode: ui.ColorAuto,
			env: map[string]string{"FORCE_COLOR": "1"}, color: true},
		{name: "NO_COLOR beats FORCE_COLOR", terminals: true, mode: ui.ColorAuto,
			env: map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, animation: true},
		{name: "TERM=dumb", terminals: true, mode: ui.ColorAuto, env: map[string]string{"TERM": "dumb"}},
		{name: "SENNIT_NO_PROGRESS keeps the colour and drops the line", terminals: true, mode: ui.ColorAuto,
			env: map[string]string{ui.NoProgressEnv: "1"}, color: true},
		{name: "--color=never on a terminal", terminals: true, mode: ui.ColorNever, animation: true},
		{name: "--color=always through a pipe", mode: ui.ColorAlways, color: true},
		{name: "--color=auto through a pipe", mode: ui.ColorAuto},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := sessionFor(t, 80, ui.Probe{
				StdoutTerminal: c.terminals, StderrTerminal: c.terminals, StderrEscapes: true, GOOS: "linux",
				Getenv: func(name string) string { return c.env[name] },
			})
			s.out.setColor(c.mode)
			s.out.phase("Connecting to Sia")
			s.out.done(ui.MarkSuccess, "Found 3 memories")
			s.out.note(ui.Message{Kind: ui.KindHint, Text: "1 superseded version held back; `--history` returns it"})
			s.out.result("4083dbdd9e9f0f816c797080eed61fba")

			written := s.stderr.String() + s.stdout.String()
			if styled := strings.Contains(written, "\x1b[0m"); styled != c.color {
				t.Errorf("colour %v, want %v: %q", styled, c.color, written)
			}
			if drawn := strings.Contains(written, "\x1b[K"); drawn != c.animation {
				t.Errorf("the live line was drawn %v, want %v: %q", drawn, c.animation, written)
			}
			if !c.color && !c.animation && uitest.HasEscape(written) {
				t.Errorf("a plain stream received an escape byte: %q", written)
			}
		})
	}
}

// With stdout piped and stderr a terminal there is no animation, because
// redrawing one stream in place while another program writes the same screen
// interleaves them.
func TestStdoutPipedAndStderrATerminalDoesNotAnimate(t *testing.T) {
	s := sessionFor(t, 80, ui.Probe{
		StderrTerminal: true, StderrEscapes: true, GOOS: "linux",
		Getenv: func(string) string { return "" },
	})
	s.out.phase("Connecting to Sia")
	s.out.done(ui.MarkSuccess, "Found 3 memories")
	if uitest.HasEscape(s.stderr.String()) {
		t.Errorf("stderr was animated: %q", s.stderr.String())
	}
	if got, want := s.stderr.String(), "Connecting to Sia...\nFound 3 memories in 0.0s\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
