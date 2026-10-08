package manifest

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/steven3002/sennit/record"
)

// The catalog's durable form is one sealed entry per line. The log and the
// snapshot share it, so a snapshot is nothing more than a log with the
// superseded entries left out, and one reader serves both.
//
// The catalog names every record the vault holds, along with its type and tags,
// so it is at least as sensitive as the records themselves and is sealed with
// its own key.
const maxLineBytes = 1 << 20

// encodeEntry seals one entry into the line that represents it.
func encodeEntry(sealer *Sealer, entry Entry) ([]byte, error) {
	plaintext, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode manifest entry %s: %w", entry.ID, err)
	}
	sealed, err := sealer.Seal(plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal manifest entry %s: %w", entry.ID, err)
	}
	line := make([]byte, base64.StdEncoding.EncodedLen(len(sealed))+1)
	base64.StdEncoding.Encode(line, sealed)
	line[len(line)-1] = '\n'
	return line, nil
}

// decodeLine opens the entry one line holds. The line is given without its
// newline.
func decodeLine(sealer *Sealer, text []byte) (Entry, error) {
	sealed, err := base64.StdEncoding.DecodeString(string(text))
	if err != nil {
		return Entry{}, err
	}
	plaintext, err := sealer.Open(sealed)
	if err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := json.Unmarshal(plaintext, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// A replay is what reading one file of sealed lines found.
type replay struct {
	// order is the order records first appeared in, carried on from whatever
	// was read before this file.
	order []record.ID
	// end is where the last line that read cleanly ends on disk, its newline
	// included.
	end int64
	// cutOff is why the file's last line could not be read, when it could not.
	// Every line before it read cleanly, so it is the last change written and
	// the only one that can have been interrupted.
	cutOff error
}

// readEntries replays a stream of sealed lines into the current entry per
// record and the order records first appeared.
//
// A line that cannot be read stops the replay with an error, unless nothing
// but empty lines follows it. That one is handed back in cutOff rather than
// refused, because it is what an append interrupted by a crash leaves: whatever
// part of a line reached the disk, which nothing can open. Losing that one
// change is correct where refusing to open the vault would not be, but only for
// a file that is appended to, so whether to accept it is the caller's decision.
// A line with another line after it was not interrupted, since the append that
// wrote the next one went on to finish, and is damage wherever it falls.
//
// An empty line holds no change. It is skipped, and it counts neither as a
// line after one that failed nor as the last line.
func readEntries(r io.Reader, sealer *Sealer, name string, entries map[record.ID]Entry, order []record.ID) (replay, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	scanner.Split(scanLine)

	read := replay{order: order}
	var offset int64
	for line := 1; scanner.Scan(); line++ {
		token := scanner.Bytes()
		offset += int64(len(token))
		text := trimLine(token)
		if len(text) == 0 {
			continue
		}
		if read.cutOff != nil {
			return read, read.cutOff
		}
		entry, err := decodeLine(sealer, text)
		if err != nil {
			read.cutOff = fmt.Errorf("%s line %d: %w", name, line, err)
			continue
		}
		if _, seen := entries[entry.ID]; !seen {
			read.order = append(read.order, entry.ID)
		}
		entries[entry.ID] = entry
		read.end = offset
	}
	if err := scanner.Err(); err != nil {
		return read, fmt.Errorf("read %s: %w", name, err)
	}
	return read, nil
}

// scanLine splits a stream into lines as bufio.ScanLines does, but leaves each
// line its newline, so that the reader knows where on disk every line ends.
func scanLine(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// trimLine takes off a line's newline and the carriage return before it, which
// is what bufio.ScanLines takes off.
func trimLine(token []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(token, []byte{'\n'}), []byte{'\r'})
}
