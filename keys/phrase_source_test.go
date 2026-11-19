package keys

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
)

// A terminal is refused rather than read, so a first run gets the instructions
// instead of a silent block it cannot interrupt.
//
// The device stands in for a terminal because it is a character device and can
// be opened anywhere; what ReadPhrase tests for is exactly that.
func TestAPhraseIsNotReadFromACharacterDevice(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	tty, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer tty.Close()

	phrase, err := ReadPhrase(tty)
	if err == nil {
		t.Fatalf("a character device was read for a phrase, and returned %q", phrase)
	}
	if err != ErrNoPhrase {
		t.Errorf("gave %v, not the instructions a first run needs", err)
	}
}

// The supported path is unaffected: a pipe is how a phrase is actually handed
// over, and refusing terminals must not touch it.
func TestAPhraseStillArrivesOnAPipe(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()

	const want = "abandon abandon abandon abandon abandon about"
	go func() {
		defer w.Close()
		w.WriteString("  " + want + "  \n")
	}()

	got, err := ReadPhrase(r)
	if err != nil {
		t.Fatalf("a phrase on a pipe was refused: %v", err)
	}
	if got != want {
		t.Errorf("read %q, wanted %q", got, want)
	}
}

// The environment still wins over any reader, terminal or not, so setting it
// remains the way to avoid the question entirely.
func TestTheEnvironmentIsPreferredEvenOverATerminal(t *testing.T) {
	t.Setenv(PhraseEnv, " from the environment ")

	tty, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer tty.Close()

	got, err := ReadPhrase(tty)
	if err != nil {
		t.Fatalf("read phrase: %v", err)
	}
	if got != "from the environment" {
		t.Errorf("read %q", got)
	}
}

// ReadPhraseFrom reads the stream it is given and nothing else, so a caller
// that has settled where the phrase comes from gets that phrase or none: the
// environment is not consulted, and only the first line is the phrase.
func TestReadPhraseFromReadsOnlyItsStream(t *testing.T) {
	t.Setenv(PhraseEnv, "from the environment")

	got, err := ReadPhraseFrom(strings.NewReader("  from the stream  \nand the line after it\n"))
	if err != nil || got != "from the stream" {
		t.Errorf("read %q (%v), want the stream's first line", got, err)
	}
	for name, stream := range map[string]io.Reader{
		"no stream":                         nil,
		"an empty stream":                   strings.NewReader(""),
		"a line of nothing but white space": strings.NewReader(" \t\r\nfrom the second line\n"),
	} {
		if got, err := ReadPhraseFrom(stream); err != ErrNoPhrase {
			t.Errorf("%s gave %q (%v), want %v", name, got, err, ErrNoPhrase)
		}
	}
}

// A first line that runs on past 64 KiB is refused as too long to be a phrase,
// in words of its own rather than the reader's, and the read stops there. A
// line of exactly 64 KiB is still read: whether it is a phrase is for the
// derivation to say.
func TestAFirstLineTooLongForAPhraseIsRefused(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	atTheLimit := strings.Repeat("a", 64<<10)
	got, err := ReadPhraseFrom(strings.NewReader(atTheLimit + "\nthe line after it\n"))
	if err != nil || got != atTheLimit {
		t.Errorf("a first line of 64 KiB gave %d bytes (%v), want the line", len(got), err)
	}

	for name, stream := range map[string][]byte{
		"one byte over":                     []byte(atTheLimit + "a\nthe line after it\n"),
		"70,000 bytes, then the phrase":     []byte(strings.Repeat("a", 70000) + "\nabandon about\n"),
		"behind a byte order mark":          append([]byte{0xEF, 0xBB, 0xBF}, atTheLimit+"a\n"...),
		"in UTF-16, one unit over":          utf16Bytes(binary.LittleEndian, strings.Repeat("a", 32<<10+1)+"\n"),
		"with no newline before it ends":    []byte(atTheLimit + "a"),
		"in UTF-16 the other way round":     utf16Bytes(binary.BigEndian, strings.Repeat("a", 40000)+"\n"),
		"a megabyte with no newline at all": bytes.Repeat([]byte("b"), 1<<20),
	} {
		r := bytes.NewReader(stream)
		got, err := ReadPhraseFrom(r)
		if err != ErrPhraseTooLong {
			t.Errorf("%s gave %d bytes (%v), want %v", name, len(got), err, ErrPhraseTooLong)
			continue
		}
		if read := len(stream) - r.Len(); read > 64<<10+4 {
			t.Errorf("%s: the refused read went on to take %d bytes", name, read)
		}
	}
	// The limit is on the line's own bytes before its newline, in UTF-16 as in
	// UTF-8, so 32 Ki units are read.
	inUnits := strings.Repeat("a", 32<<10)
	if got, err := ReadPhraseFrom(bytes.NewReader(utf16Bytes(binary.LittleEndian, inUnits+"\n"))); err != nil || got != inUnits {
		t.Errorf("a UTF-16 first line of 64 KiB gave %d bytes (%v), want the line", len(got), err)
	}
}

// A reader that is not a file cannot be a terminal, so the in-process callers
// that pass a plain reader keep working.
func TestAPlainReaderIsNotMistakenForATerminal(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	got, err := ReadPhrase(strings.NewReader("a phrase from memory\n"))
	if err != nil {
		t.Fatalf("read phrase: %v", err)
	}
	if got != "a phrase from memory" {
		t.Errorf("read %q", got)
	}
}
