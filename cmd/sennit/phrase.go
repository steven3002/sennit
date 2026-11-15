package main

import (
	"context"
	"errors"
	"flag"
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
// environment: the stream one arrives on when --phrase-stdin asks for it,
// whether there is anybody at the other end of it, and how to read from that
// person without putting what they type on the screen.
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

// bindPhraseStdin defines --phrase-stdin, which every command that reads a
// phrase takes, and which means the same on each of them.
func bindPhraseStdin(fs *flag.FlagSet, asked *bool) {
	fs.BoolVar(asked, "phrase-stdin", false, "read the recovery phrase from stdin")
}

// readPhrase takes the recovery phrase from the environment, or from stdin when
// --phrase-stdin asks for it, and otherwise from the profile sennit connect
// stored, and asks for it when none of them supplied one and there is somebody
// to ask.
//
// Stdin is read only when the flag asks. A command run inside a script, or over
// ssh, inherits a stdin that was never meant for it: read unasked, the next line
// of whatever was being fed in would be taken for the phrase, and the rest of it
// swallowed. That was found on a server, where `ssh host 'bash -s' < script`
// lost every line after the first sennit command. So no pipe is read on the
// chance that it holds a phrase, whether or not one is stored, which is the
// rule gh, docker and the AWS CLI keep for a secret on stdin as well.
//
// The flag and the environment together are refused rather than ranked: that
// is two phrases, and taking either would be a guess about which one was meant.
// The flag alone comes before the stored profile, because with a phrase stored
// the only reason to pass it is to use another one. Asked for, stdin is the only
// place looked, so a stdin with no phrase on it is refused rather than made up
// for from the profile or the prompt. A phrase that came in on stdin is from
// then on what one from the environment is: the key can still come from the
// profile, and connect still keeps a different stored phrase unless told to
// replace it.
//
// With neither, a question is asked only when the profile was silent too, so a
// run with no phrase anywhere and no terminal refuses, and says each way a
// phrase can be given.
func (s *session) readPhrase(phraseStdin bool) (string, error) {
	env := strings.TrimSpace(os.Getenv(keys.PhraseEnv))
	switch {
	case env != "" && phraseStdin:
		return "", twoPhrases()
	case env != "":
		s.sources.phrase = keys.PhraseEnv
		return env, nil
	case phraseStdin:
		phrase, err := keys.ReadPhraseFrom(s.phrases.stdin)
		switch {
		case errors.Is(err, keys.ErrNoPhrase):
			return "", nothingOnStdin()
		case err != nil:
			return "", err
		}
		s.sources.phrase = fromStdin
		return phrase, nil
	}

	stored, _, found, err := s.savedProfile()
	if err != nil {
		return "", err
	}
	if found {
		s.sources.phrase = fromProfile
		return stored.Phrase, nil
	}
	if !s.phrases.interactive {
		return "", keys.ErrNoPhrase
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
	s.sources.phrase = fromPrompt
	return typed, nil
}

// twoPhrases is the refusal of a run given a phrase in the environment and told
// to read one from stdin as well.
//
// Neither is picked by an order. Either could be the one meant: the variable
// may be left over from earlier, or the flag added with the variable forgotten,
// and a run that guessed wrong would go on to open, or connect, the wrong vault.
// gh refuses GH_TOKEN with --with-token, and docker refuses --password with
// --password-stdin, for the same reason.
func twoPhrases() *problem {
	return refuse(keys.PhraseEnv+" is set, and --phrase-stdin was given too").
		because("That is two recovery phrases, and taking either would be a guess about which one was meant.").
		try("remove "+keys.PhraseEnv+" from the environment to read the phrase from stdin",
			"or leave out `--phrase-stdin` to use "+keys.PhraseEnv)
}

// nothingOnStdin is the refusal of a run told to read the phrase from stdin
// that found none there. The person asked for stdin, so the stored profile and
// the prompt are not tried in its place.
func nothingOnStdin() *problem {
	return refuse("--phrase-stdin was given, and no phrase arrived on stdin").
		because("With the flag the phrase is read from the first line of stdin and from nowhere else. "+
			"That line was empty, or stdin is a terminal rather than a pipe or a file.").
		try("pipe the phrase in, or redirect a file that holds it",
			"or leave out `--phrase-stdin` to use "+keys.PhraseEnv+", the stored profile or the prompt").
		from(keys.ErrNoPhrase)
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
