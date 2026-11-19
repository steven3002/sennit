package keys

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"
)

// utf16Bytes is text as UTF-16 in the given byte order, behind the byte order
// mark that says which order it is.
func utf16Bytes(order binary.AppendByteOrder, text string) []byte {
	out := order.AppendUint16(nil, 0xFEFF)
	for _, unit := range utf16.Encode([]rune(text)) {
		out = order.AppendUint16(out, unit)
	}
	return out
}

// A phrase is read whatever Windows wrote it as.
//
// The bytes below are what arrives. A file made with > in Windows PowerShell
// is UTF-16 with a byte order mark in front. A PowerShell session whose output
// encoding is UTF-8 puts a UTF-8 byte order mark in front of what it pipes to a
// program. Both used to be refused as a wrong phrase.
func TestAPhraseIsReadWhateverItWasWrittenAs(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	const want = "abandon abandon abandon abandon abandon about"
	for name, raw := range map[string][]byte{
		"plain": []byte(want + "\n"),
		"plain, with the line ended as Windows ends one": []byte(want + "\r\n"),
		"UTF-8 behind a byte order mark":                 append([]byte{0xEF, 0xBB, 0xBF}, want+"\r\n"...),
		"UTF-16, as Windows PowerShell redirects it":     utf16Bytes(binary.LittleEndian, want+"\r\n"),
		"UTF-16 the other way round":                     utf16Bytes(binary.BigEndian, want+"\n"),
	} {
		got, err := ReadPhrase(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("%s: refused: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s: read %q, wanted %q", name, got, want)
		}
	}
}

// A phrase is read at the end of its line, without waiting for the pipe to be
// closed, because whatever hands the phrase over may keep its end open.
func TestAPhraseIsReadFromAPipeLeftOpen(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	const want = "abandon abandon abandon abandon abandon about"
	if _, err := w.WriteString(want + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	type answer struct {
		phrase string
		err    error
	}
	read := make(chan answer, 1)
	go func() {
		phrase, err := ReadPhrase(r)
		read <- answer{phrase, err}
	}()
	select {
	case got := <-read:
		if got.err != nil {
			t.Fatalf("a phrase on an open pipe was refused: %v", got.err)
		}
		if got.phrase != want {
			t.Errorf("read %q, wanted %q", got.phrase, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read waited for the pipe to close")
	}
}

// The phrase's line is all that is taken from the stream, whatever it was
// written as, so whatever reads the stream next finds every byte after it.
//
// A pipe cannot be given back what a read took from it, so a read that ran on
// past the line would leave the next reader of the same stdin, such as the
// shell running a script, without the lines that followed the phrase. It is
// checked on a pipe and a file, the two things stdin is when a phrase is handed
// over, as well as on a reader in memory.
func TestOnlyThePhraseLineIsTaken(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	const want = "abandon abandon abandon abandon abandon about"
	const rest = "the next line of a script\nand the one after it\n"
	streams := map[string]func(*testing.T, []byte) io.Reader{
		"memory": func(_ *testing.T, raw []byte) io.Reader { return bytes.NewReader(raw) },
		"a pipe": func(t *testing.T, raw []byte) io.Reader {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			t.Cleanup(func() { r.Close() })
			go func() {
				defer w.Close()
				w.Write(raw)
			}()
			return r
		},
		"a file": func(t *testing.T, raw []byte) io.Reader {
			path := filepath.Join(t.TempDir(), "phrase.txt")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
			t.Cleanup(func() { f.Close() })
			return f
		},
	}
	for name, line := range map[string][]byte{
		"plain": []byte(want + "\n"),
		"plain, with the line ended as Windows ends one": []byte(want + "\r\n"),
		"UTF-8 behind a byte order mark":                 append([]byte{0xEF, 0xBB, 0xBF}, want+"\r\n"...),
		"UTF-16, as Windows PowerShell redirects it":     utf16Bytes(binary.LittleEndian, want+"\r\n"),
		"UTF-16 the other way round":                     utf16Bytes(binary.BigEndian, want+"\n"),
	} {
		for kind, stream := range streams {
			t.Run(name+", from "+kind, func(t *testing.T) {
				r := stream(t, append(append([]byte{}, line...), rest...))
				got, err := ReadPhraseFrom(r)
				if err != nil || got != want {
					t.Fatalf("read %q (%v), want %q", got, err, want)
				}
				left, err := io.ReadAll(r)
				if err != nil {
					t.Fatalf("read what is left: %v", err)
				}
				if string(left) != rest {
					t.Errorf("the read took more than the phrase's line: %d of %d bytes left, %q",
						len(left), len(rest), left)
				}
			})
		}
	}
}
