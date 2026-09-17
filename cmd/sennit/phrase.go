package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/steven3002/sennit/keys"
)

// phrasePrompt is what a person is asked. It goes to stderr with the rest of
// what a person watches, so a command's result is still the only thing on
// stdout.
const phrasePrompt = "Recovery phrase: "

// A phraseSource is where a recovery phrase can come from beyond the
// environment: the stream a piped one arrives on, whether there is anybody at
// the other end of it, and how to read from that person without putting what
// they type on the screen.
//
// It is described here rather than read from the operating system where it is
// used, because a test process has no terminal and this is the one piece of
// the command that only exists when there is one.
type phraseSource struct {
	stdin io.Reader
	// interactive reports both halves of there being somebody to ask: a person
	// typing at stdin, and a terminal to put the question on. Asking with only
	// the first would wait for the answer to a question that went into a file,
	// which is the silent hang this has to avoid.
	interactive bool
	// ask reads one line with the terminal's echo turned off. It reports
	// context.Canceled if the run was interrupted while it waited.
	ask func() (string, error)
}

// terminalPhrases describes the real terminal, or the absence of one.
func terminalPhrases(interrupted <-chan struct{}) phraseSource {
	// Both streams are tested with the same call the rest of the CLI uses,
	// rather than through the output policy, because the question is whether
	// there is a terminal at all and not what may be drawn on it.
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	return phraseSource{
		stdin:       os.Stdin,
		interactive: interactive,
		ask:         func() (string, error) { return askAtTheTerminal(os.Stdin, interrupted) },
	}
}

// readPhrase takes the recovery phrase from the environment or a pipe, and
// asks for it when neither supplied one and there is somebody to ask.
//
// The order is the point and does not change. The environment comes first, a
// pipe second, and a question only when both were silent, so nothing that runs
// without a person behaves differently than it did before there was a prompt.
// A run with no phrase and no terminal still refuses, with the message it
// always gave.
func (s *session) readPhrase() (string, error) {
	phrase, err := keys.ReadPhrase(s.phrases.stdin)
	if !errors.Is(err, keys.ErrNoPhrase) || !s.phrases.interactive {
		return phrase, err
	}

	fmt.Fprint(s.stderr, phrasePrompt)
	typed, err := s.phrases.ask()
	// The terminal echoed nothing, the return included, so the line the person
	// typed on is ended here whichever way the read went.
	fmt.Fprintln(s.stderr)
	switch {
	case errors.Is(err, io.EOF):
		// End-of-file at the prompt is somebody deciding not to answer, which
		// is the same position as never having been asked.
		return "", keys.ErrNoPhrase
	case err != nil:
		return "", err
	}
	if typed = strings.TrimSpace(typed); typed == "" {
		return "", keys.ErrNoPhrase
	}
	return typed, nil
}

// askAtTheTerminal reads one line from the terminal with its echo turned off.
//
// Three things have to hold however this ends: the echo is back on, nothing
// typed reached the screen or the scrollback, and ctrl+c ends the command.
//
// The last is why the read runs in a goroutine. x/term leaves ISIG set, so
// ctrl+c does still raise SIGINT, but this program handles that signal instead
// of dying of it and the runtime restarts the read underneath, so the read on
// its own would never come back and the terminal could not be escaped. Waiting
// on the interrupt beside it, and putting the terminal back from here rather
// than from the read that still holds it, is what lets the command end. The
// goroutine is left blocked on a read the exit collects; it holds nothing the
// exit does not already release.
func askAtTheTerminal(in *os.File, interrupted <-chan struct{}) (string, error) {
	fd := int(in.Fd())
	before, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	type answer struct {
		typed string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		typed, err := term.ReadPassword(fd)
		done <- answer{string(typed), err}
	}()

	select {
	case <-interrupted:
		if err := term.Restore(fd, before); err != nil {
			return "", err
		}
		return "", context.Canceled
	case got := <-done:
		return got.typed, got.err
	}
}
