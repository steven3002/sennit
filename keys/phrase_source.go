package keys

import (
	"bytes"
	"encoding/binary"
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

// maxPhraseLine is the most a stream's first line may hold before its newline,
// past any byte order mark. A recovery phrase is twelve words of at most eight
// letters, so 64 KiB is far more than any phrase however it is spaced, and it
// still bounds what a stream that holds no phrase can make the read take.
const maxPhraseLine = 64 << 10

// ErrPhraseTooLong reports that the first line of a stream ran on past 64 KiB,
// which no recovery phrase does. The read stops there rather than following
// the line to its end. The command line shows its own message in its place.
var ErrPhraseTooLong = fmt.Errorf("the first line is longer than %d KiB, which no recovery phrase is", maxPhraseLine>>10)

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
// and when it is empty or its first line holds nothing but white space, and
// ErrPhraseTooLong when that line runs on past 64 KiB.
//
// The line is taken as soon as it ends, without waiting for the stream to close,
// because whatever hands the phrase over may keep its end open. Nothing after
// it is taken, so whatever reads the stream next finds every line that follows.
func ReadPhraseFrom(stream io.Reader) (string, error) {
	if stream == nil || isTerminal(stream) {
		return "", ErrNoPhrase
	}
	line, err := firstLine(stream)
	if err != nil {
		return "", err
	}
	text, err := io.ReadAll(asUTF8(bytes.NewReader(line)))
	if err != nil {
		return "", fmt.Errorf("read recovery phrase: %w", err)
	}
	phrase, _, _ := strings.Cut(string(text), "\n")
	if phrase = strings.TrimSpace(phrase); phrase == "" {
		return "", ErrNoPhrase
	}
	return phrase, nil
}

// The byte order marks a phrase can arrive behind.
var (
	utf8Mark      = []byte{0xEF, 0xBB, 0xBF}
	utf16Mark     = []byte{0xFF, 0xFE}
	utf16BackMark = []byte{0xFE, 0xFF}
)

// firstLine reads a stream's first line, its newline included, and not one
// byte after it.
//
// It reads a byte at a time, as sudo -S reads a password and gpg
// --passphrase-fd a passphrase. A buffered read takes whatever the stream holds
// past the line, and a pipe cannot be given it back, so on a stdin shared with
// something else, such as the shell running a script, every line after the
// phrase would be lost. A phrase is about a hundred bytes, so that is about a
// hundred reads.
//
// The newline is looked for in the encoding the stream is written in: behind
// a UTF-16 byte order mark it is a two-byte unit, and anywhere else it is one
// byte, as UTF-8 writes it. The whole line, mark included, is then decoded by
// asUTF8, so it reads as the same text whatever it was written as.
func firstLine(stream io.Reader) ([]byte, error) {
	var (
		line  []byte
		one   [1]byte
		wide  binary.ByteOrder // UTF-16's byte order, once its mark has been read
		start int              // where the line's text begins, past a mark
	)
	for {
		if _, err := io.ReadFull(stream, one[:]); err == io.EOF {
			return line, nil
		} else if err != nil {
			return nil, fmt.Errorf("read recovery phrase: %w", err)
		}
		line = append(line, one[0])
		if start == 0 {
			switch {
			case bytes.Equal(line, utf16Mark):
				wide, start = binary.LittleEndian, len(line)
				continue
			case bytes.Equal(line, utf16BackMark):
				wide, start = binary.BigEndian, len(line)
				continue
			case bytes.Equal(line, utf8Mark):
				start = len(line)
				continue
			}
		}
		text := line[start:]
		switch {
		case wide == nil && one[0] == '\n':
			return line, nil
		case wide != nil && len(text)%2 != 0:
			// Half of a two-byte unit, which can neither end the line nor make it
			// longer than a whole one would.
			continue
		case wide != nil && wide.Uint16(text[len(text)-2:]) == '\n':
			return line, nil
		}
		if len(text) > maxPhraseLine {
			return nil, ErrPhraseTooLong
		}
	}
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
