package keys

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// PhraseEnv is the environment variable carrying the recovery phrase.
const PhraseEnv = "SENNIT_PHRASE"

// ErrNoPhrase reports that no recovery phrase was supplied.
//
// It names the two ways in that every program here has. sennit-mcp shows it as
// it stands, and its stdin carries the protocol, so a phrase can never be piped
// to it. The command line reads stdin only when --phrase-stdin asks, and shows
// its own message in place of this one, which names the flag.
var ErrNoPhrase = fmt.Errorf("no recovery phrase: set %s, or run sennit connect to store one", PhraseEnv)

// ReadPhrase takes the recovery phrase from the environment, falling back to
// the stream it is given, if any. sennit-mcp gives it none, because its stdin is
// the protocol. The vault itself never stores it: a command holds it for its own
// lifetime, and the one copy kept is the default profile sennit connect stores
// on this device. That is what keeps "keys never leave the device" enforceable
// rather than aspirational.
func ReadPhrase(stdin io.Reader) (string, error) {
	if phrase := strings.TrimSpace(os.Getenv(PhraseEnv)); phrase != "" {
		return phrase, nil
	}
	return ReadPhraseFrom(stdin)
}

// ReadPhraseFrom takes the recovery phrase from one stream and nowhere else: the
// first line of a pipe or a file, trimmed, whatever it was written as. It
// reports ErrNoPhrase when there is no stream, when the stream is a terminal,
// and when it is empty or its first line holds nothing but white space.
//
// The line is taken as soon as it ends, without waiting for the stream to close,
// because whatever hands the phrase over may keep its end open.
func ReadPhraseFrom(stream io.Reader) (string, error) {
	if stream == nil || isTerminal(stream) {
		return "", ErrNoPhrase
	}
	scanner := bufio.NewScanner(asUTF8(stream))
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read recovery phrase: %w", err)
		}
		return "", ErrNoPhrase
	}
	phrase := strings.TrimSpace(scanner.Text())
	if phrase == "" {
		return "", ErrNoPhrase
	}
	return phrase, nil
}

// asUTF8 reads text as UTF-8 whatever it was written as, going by the byte
// order mark it opens with, and takes the mark off.
//
// On Windows a phrase seldom arrives as plain bytes. Windows PowerShell writes
// a redirected file as UTF-16, and a session set to UTF-8 puts a byte order
// mark in front of whatever it pipes to a program. Read as they stood, the
// first gave the wrong number of words and the second a first word nobody
// typed, and both said the phrase was wrong when it was not.
func asUTF8(r io.Reader) io.Reader {
	return transform.NewReader(r, unicode.BOMOverride(unicode.UTF8.NewDecoder()))
}

// isTerminal reports whether the reader is an interactive terminal rather than
// a pipe or a file.
//
// A terminal is not a stream a phrase can be read from here. Nothing here turns
// echo off, so a phrase typed at it is printed to the screen and kept in
// scrollback; the read blocks with nothing prompted, so the run looks like it
// has hung; and the interrupt that would end it is claimed by a signal handler
// that the blocked read never consults, so the terminal cannot be escaped.
// Refusing costs nothing: a phrase handed over on stdin comes through a pipe or
// a file, and neither is a character device. Somebody sitting at a terminal is
// asked by the command line instead, with the echo turned off.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
