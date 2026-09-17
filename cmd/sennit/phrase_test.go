package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
)

// asked builds a session whose phrase source is the terminal a test process
// does not have, so the prompt can be exercised without one.
func asked(t *testing.T, stdin io.Reader, interactive bool, answer string, err error) (*screen, *int) {
	t.Helper()
	s := terminal(t, 80)
	calls := 0
	s.out.phrases = phraseSource{
		stdin:       stdin,
		interactive: interactive,
		ask: func() (string, error) {
			calls++
			return answer, err
		},
	}
	return s, &calls
}

// A person at a terminal can supply the phrase, and nobody else's run changes.
//
// Before this, a terminal was the one place the phrase could not be given: the
// read refused the moment stdin was a character device, so the only ways in
// were an environment variable every child process inherits and a pipe.
func TestAPhraseCanBeTypedAtATerminal(t *testing.T) {
	t.Setenv(keys.PhraseEnv, "")

	t.Run("a terminal is asked", func(t *testing.T) {
		s, calls := asked(t, characterDevice(t), true, "  twelve words from the keyboard  ", nil)
		got, err := s.out.readPhrase()
		if err != nil {
			t.Fatalf("read phrase: %v", err)
		}
		if got != "twelve words from the keyboard" {
			t.Errorf("phrase came back as %q, want it trimmed", got)
		}
		if *calls != 1 {
			t.Errorf("the terminal was asked %d times, want once", *calls)
		}
		if !strings.Contains(s.stderr.String(), phrasePrompt) {
			t.Errorf("nothing asked for the phrase: %q", s.stderr.String())
		}
		if s.stdout.Len() != 0 {
			t.Errorf("the prompt reached stdout: %q", s.stdout.String())
		}
	})

	t.Run("a pipe is read and never asked", func(t *testing.T) {
		s, calls := asked(t, strings.NewReader("phrase from a pipe\n"), true, "typed instead", nil)
		got, err := s.out.readPhrase()
		if err != nil {
			t.Fatalf("read phrase: %v", err)
		}
		if got != "phrase from a pipe" {
			t.Errorf("phrase came back as %q, want the piped one", got)
		}
		if *calls != 0 {
			t.Errorf("a pipe supplied the phrase and the terminal was still asked %d times", *calls)
		}
	})

	t.Run("the environment wins over both", func(t *testing.T) {
		t.Setenv(keys.PhraseEnv, "phrase from the environment")
		s, calls := asked(t, strings.NewReader("phrase from a pipe\n"), true, "typed instead", nil)
		got, err := s.out.readPhrase()
		if err != nil {
			t.Fatalf("read phrase: %v", err)
		}
		if got != "phrase from the environment" {
			t.Errorf("phrase came back as %q, want the environment's", got)
		}
		if *calls != 0 {
			t.Errorf("the environment supplied the phrase and the terminal was still asked %d times", *calls)
		}
	})

	// The refusal a script or a CI job meets has to be the one it always met.
	t.Run("no terminal still refuses, unchanged", func(t *testing.T) {
		s, calls := asked(t, characterDevice(t), false, "typed instead", nil)
		_, err := s.out.readPhrase()
		if !errors.Is(err, keys.ErrNoPhrase) {
			t.Fatalf("a run with nowhere to ask failed with %v, want %v", err, keys.ErrNoPhrase)
		}
		if *calls != 0 {
			t.Errorf("a run with nowhere to ask still asked %d times", *calls)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("a run with nowhere to ask wrote %q", s.stderr.String())
		}
	})

	t.Run("nothing typed is no phrase", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "   ", nil)
		if _, err := s.out.readPhrase(); !errors.Is(err, keys.ErrNoPhrase) {
			t.Fatalf("an empty answer failed with %v, want %v", err, keys.ErrNoPhrase)
		}
	})

	t.Run("end of file at the prompt is no phrase", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", io.EOF)
		if _, err := s.out.readPhrase(); !errors.Is(err, keys.ErrNoPhrase) {
			t.Fatalf("ctrl+d at the prompt failed with %v, want %v", err, keys.ErrNoPhrase)
		}
	})

	// An interrupt has to reach the command rather than be swallowed by a read
	// that the signal handler never lets return.
	t.Run("an interrupt ends the command", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", context.Canceled)
		if _, err := s.out.readPhrase(); !errors.Is(err, context.Canceled) {
			t.Fatalf("an interrupt at the prompt failed with %v, want it cancelled", err)
		}
	})
}

// characterDevice is a stdin that carries nothing and is not a pipe, which is
// what keys.ReadPhrase sees when a person is sitting at a terminal. A test
// process has no terminal, and the null device is the character device every
// machine running these tests does have.
func characterDevice(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no character device to stand in for a terminal: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
