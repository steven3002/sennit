package main

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/profile"
	"github.com/steven3002/sennit/profile/profiletest"
	"github.com/steven3002/sennit/sia"
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
		got, err := s.out.readPhrase(false)
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

	t.Run("a pipe the flag names is read and never asked", func(t *testing.T) {
		s, calls := asked(t, strings.NewReader("phrase from a pipe\n"), true, "typed instead", nil)
		got, err := s.out.readPhrase(true)
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

	t.Run("the environment comes before the prompt, and leaves stdin unread", func(t *testing.T) {
		t.Setenv(keys.PhraseEnv, "phrase from the environment")
		stdin := strings.NewReader("phrase from a pipe\n")
		s, calls := asked(t, stdin, true, "typed instead", nil)
		got, err := s.out.readPhrase(false)
		if err != nil {
			t.Fatalf("read phrase: %v", err)
		}
		if got != "phrase from the environment" {
			t.Errorf("phrase came back as %q, want the environment's", got)
		}
		if *calls != 0 {
			t.Errorf("the environment supplied the phrase and the terminal was still asked %d times", *calls)
		}
		if stdin.Len() != len("phrase from a pipe\n") {
			t.Errorf("stdin was read unasked")
		}
	})

	// A script or a CI job with no phrase anywhere is still refused, and is
	// neither asked nor written to before the refusal.
	t.Run("no terminal still refuses", func(t *testing.T) {
		s, calls := asked(t, characterDevice(t), false, "typed instead", nil)
		_, err := s.out.readPhrase(false)
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
		if _, err := s.out.readPhrase(false); !errors.Is(err, keys.ErrNoPhrase) {
			t.Fatalf("an empty answer failed with %v, want %v", err, keys.ErrNoPhrase)
		}
	})

	t.Run("end of file at the prompt is no phrase", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", io.EOF)
		if _, err := s.out.readPhrase(false); !errors.Is(err, keys.ErrNoPhrase) {
			t.Fatalf("ctrl+d at the prompt failed with %v, want %v", err, keys.ErrNoPhrase)
		}
	})

	// An interrupt has to reach the command rather than be swallowed by a read
	// that the signal handler never lets return.
	t.Run("an interrupt ends the command", func(t *testing.T) {
		s, _ := asked(t, characterDevice(t), true, "", context.Canceled)
		if _, err := s.out.readPhrase(false); !errors.Is(err, context.Canceled) {
			t.Fatalf("an interrupt at the prompt failed with %v, want it cancelled", err)
		}
	})
}

// With --phrase-stdin and nothing stored, the phrase is the first line of
// stdin, and nobody is asked.
func TestTheFlagReadsStdinWithNothingStored(t *testing.T) {
	t.Setenv(keys.PhraseEnv, "")
	s, asks := asked(t, strings.NewReader("twelve words from a pipe\n"), true, "a typed phrase", nil)
	s.out.profiles = profile.At(t.TempDir(), profiletest.NewKeychain())

	phrase, err := s.out.readPhrase(statusFlags(t, s, "--phrase-stdin").phraseStdin)
	if err != nil || phrase != "twelve words from a pipe" || s.out.sources.phrase != fromStdin {
		t.Errorf("read %q from %q (%v), want the piped phrase", phrase, s.out.sources.phrase, err)
	}
	if *asks != 0 {
		t.Errorf("a pipe supplied the phrase and the terminal was still asked %d time(s)", *asks)
	}
}

// With a phrase stored, the flag is the way to use another one, so it comes
// before the profile, and the run says the phrase came from stdin. From there
// on the piped phrase is what one from the environment is: the key still comes
// from the profile, with the indexer that issued it, and connect still keeps a
// different stored phrase unless told to replace it.
func TestTheFlagReadsStdinOverAStoredProfile(t *testing.T) {
	s, asks := asked(t, strings.NewReader("twelve other words\n"), true, "a typed phrase", nil)
	stored, _ := withStoredProfile(t, s, "https://indexer.example")

	flags := statusFlags(t, s, "--phrase-stdin")
	phrase, err := s.out.readPhrase(flags.phraseStdin)
	if err != nil || phrase != "twelve other words" || s.out.sources.phrase != fromStdin {
		t.Fatalf("read %q from %q (%v), want the piped phrase over the stored one", phrase, s.out.sources.phrase, err)
	}
	if *asks != 0 {
		t.Errorf("a pipe supplied the phrase and the terminal was still asked %d time(s)", *asks)
	}
	key, err := flags.appKey(s.out)
	if err != nil || hex.EncodeToString(key) != hex.EncodeToString(stored.AppKey) || s.out.sources.key != fromProfile {
		t.Errorf("the key came from %q (%v), want the stored profile", s.out.sources.key, err)
	}
	if flags.indexer != stored.Indexer {
		t.Errorf("the run uses %s, want the indexer that issued the stored key, %s", flags.indexer, stored.Indexer)
	}
	if got, want := secretsRow(s.out.sources, s.out.saved.summary.Place),
		"phrase from stdin, app key from the default profile, in the test keychain"; got != want {
		t.Errorf("status would say %q, want %q", got, want)
	}

	// connect is refused before it asks the indexer for anything.
	connecting, _ := asked(t, strings.NewReader("twelve other words\n"), false, "", nil)
	withStoredProfile(t, connecting, "https://indexer.example")
	err = runConnect(t.Context(), connecting.out, []string{"--phrase-stdin", "--indexer", unreachable})
	var p *problem
	if !errors.As(err, &p) || p.message.Text != phraseWouldBeReplaced().message.Text {
		t.Errorf("connect with a different phrase on stdin gave %v, want the refusal to replace the stored one", err)
	}
}

// SENNIT_PHRASE and --phrase-stdin together are two phrases, and the run
// refuses rather than pick one by an order. Neither stdin nor the terminal is
// read.
func TestTheFlagWithTheEnvironmentIsRefused(t *testing.T) {
	const piped = "twelve words from a pipe\n"
	stdin := strings.NewReader(piped)
	s, asks := asked(t, stdin, true, "a typed phrase", nil)
	withStoredProfile(t, s, sia.DefaultIndexer)
	t.Setenv(keys.PhraseEnv, "twelve words from the environment")

	phrase, err := s.out.readPhrase(true)
	var p *problem
	if !errors.As(err, &p) || p.message.Text != twoPhrases().message.Text {
		t.Fatalf("read %q from %q (%v), want the refusal of two phrases", phrase, s.out.sources.phrase, err)
	}
	if stdin.Len() != len(piped) || *asks != 0 {
		t.Errorf("a run refused for two phrases read %d bytes of stdin and asked %d time(s)",
			len(piped)-stdin.Len(), *asks)
	}
	said := messageFor(err)
	if !strings.Contains(said.Text, keys.PhraseEnv) || !strings.Contains(said.Text, "--phrase-stdin") {
		t.Errorf("the refusal does not name both phrases: %q", said.Text)
	}
}

// Asked for stdin, a run that finds no phrase there refuses. It does not fall
// back on the stored profile, or on the prompt even where there is somebody to
// ask: the person asked for stdin.
func TestTheFlagWithNoPhraseOnStdinIsRefused(t *testing.T) {
	for name, stdin := range map[string]func(*testing.T) io.Reader{
		"an empty stdin":                     func(*testing.T) io.Reader { return strings.NewReader("") },
		"a stdin of nothing but white space": func(*testing.T) io.Reader { return strings.NewReader(" \t\r\n\n") },
		"a terminal":                         func(t *testing.T) io.Reader { return characterDevice(t) },
	} {
		t.Run(name, func(t *testing.T) {
			s, asks := asked(t, stdin(t), true, "a typed phrase", nil)
			withStoredProfile(t, s, sia.DefaultIndexer)

			phrase, err := s.out.readPhrase(true)
			var p *problem
			if !errors.As(err, &p) || p.message.Text != nothingOnStdin().message.Text {
				t.Fatalf("read %q from %q (%v), want the refusal of a stdin with no phrase",
					phrase, s.out.sources.phrase, err)
			}
			if !errors.Is(err, keys.ErrNoPhrase) {
				t.Errorf("the refusal does not carry %v", keys.ErrNoPhrase)
			}
			if *asks != 0 || s.stderr.Len() != 0 {
				t.Errorf("a run asked for stdin asked %d time(s) and wrote %q", *asks, s.stderr.String())
			}
			if s.out.sources.phrase != "" {
				t.Errorf("a refused run says its phrase came from %q", s.out.sources.phrase)
			}
		})
	}
}

// Asked for stdin, a run whose first line there runs on past 64 KiB refuses in
// words, not with the reader's error, and as with no phrase on stdin it does
// not fall back on the stored profile or the prompt.
func TestTheFlagWithAFirstLineTooLongIsRefused(t *testing.T) {
	stdin := strings.NewReader(strings.Repeat("a", 70000) + "\ntwelve words from a pipe\n")
	s, asks := asked(t, stdin, true, "a typed phrase", nil)
	withStoredProfile(t, s, sia.DefaultIndexer)

	phrase, err := s.out.readPhrase(true)
	var p *problem
	if !errors.As(err, &p) || p.message.Text != firstLineTooLong().message.Text {
		t.Fatalf("read %d bytes from %q (%v), want the refusal of a first line too long to be a phrase",
			len(phrase), s.out.sources.phrase, err)
	}
	if !errors.Is(err, keys.ErrPhraseTooLong) {
		t.Errorf("the refusal does not carry %v", keys.ErrPhraseTooLong)
	}
	if *asks != 0 || s.stderr.Len() != 0 {
		t.Errorf("a run asked for stdin asked %d time(s) and wrote %q", *asks, s.stderr.String())
	}
	if s.out.sources.phrase != "" {
		t.Errorf("a refused run says its phrase came from %q", s.out.sources.phrase)
	}
}

// Every command that reads a phrase takes --phrase-stdin and hands it to the
// read: given SENNIT_PHRASE as well, each one refuses before it opens a vault
// or asks an indexer for anything, and leaves stdin as it was.
func TestEveryCommandThatReadsAPhraseTakesTheFlag(t *testing.T) {
	t.Setenv(keys.PhraseEnv, storedPhrase)
	// A command the flag never reached would go on with the variable's phrase,
	// so nothing it could go on to is a real service: the model is fetched from
	// a closed port, and connect is pointed at one.
	t.Setenv(embed.EndpointEnv, unreachable)
	home := t.TempDir()
	onVault := func(rest ...string) []string {
		return append([]string{"--phrase-stdin", "--offline", "--home", home}, rest...)
	}
	for _, c := range []struct {
		command string
		run     func(context.Context, *session, []string) error
		args    []string
	}{
		{"init", runInit, onVault()},
		{"connect", runConnect, []string{"--phrase-stdin", "--out", filepath.Join(home, "sennit.key"), "--indexer", unreachable}},
		{"remember", runRemember, onVault("--context", "why it matters", "the statement")},
		{"recall", runRecall, onVault("the query")},
		{"flush", runFlush, onVault()},
		{"status", runStatus, onVault()},
		{"reclaim", runReclaim, onVault()},
		{"recover", runRecover, onVault()},
		{"hydrate", runHydrate, onVault()},
	} {
		t.Run(c.command, func(t *testing.T) {
			const piped = "twelve words from a pipe\n"
			stdin := strings.NewReader(piped)
			s, _ := asked(t, stdin, false, "", nil)

			err := c.run(t.Context(), s.out, c.args)
			var p *problem
			if !errors.As(err, &p) || p.message.Text != twoPhrases().message.Text {
				t.Fatalf("sennit %s %s gave %v, want the refusal of two phrases",
					c.command, strings.Join(c.args, " "), err)
			}
			if stdin.Len() != len(piped) {
				t.Errorf("sennit %s read stdin before refusing", c.command)
			}
		})
	}
}

// unreachable is an address nothing listens on, for a test that must not reach
// a real service even if what it tests has broken.
const unreachable = "http://127.0.0.1:1"

// characterDevice is a stdin that carries nothing and is not a pipe, which is
// what keys.ReadPhraseFrom sees when a person is sitting at a terminal. A test
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
