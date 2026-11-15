package keys

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
	"time"
	"unicode/utf16"
)

// A phrase is read whatever Windows wrote it as.
//
// The bytes below are what arrives. A file made with > in Windows PowerShell
// is UTF-16 with a byte order mark in front. A PowerShell session whose output
// encoding is UTF-8 puts a UTF-8 byte order mark in front of what it pipes to a
// program. Both used to be refused as a wrong phrase.
func TestAPhraseIsReadWhateverItWasWrittenAs(t *testing.T) {
	t.Setenv(PhraseEnv, "")

	const want = "abandon abandon abandon abandon abandon about"
	utf16Bytes := func(order binary.AppendByteOrder, text string) []byte {
		out := order.AppendUint16(nil, 0xFEFF)
		for _, unit := range utf16.Encode([]rune(text)) {
			out = order.AppendUint16(out, unit)
		}
		return out
	}
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
