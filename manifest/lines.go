package manifest

import (
	"bufio"
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

// readEntries replays a stream of sealed lines into the current entry per
// record and the order records first appeared.
//
// A truncated final line is tolerated: an append interrupted by a crash leaves
// a partial record, and losing that one entry is correct where refusing to open
// the vault would not be.
func readEntries(r io.Reader, sealer *Sealer, name string, entries map[record.ID]Entry, order []record.ID) ([]record.ID, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for line := 1; scanner.Scan(); line++ {
		text := scanner.Bytes()
		if len(text) == 0 {
			continue
		}
		sealed, err := base64.StdEncoding.DecodeString(string(text))
		if err != nil {
			return order, fmt.Errorf("%s line %d: %w", name, line, err)
		}
		plaintext, err := sealer.Open(sealed)
		if err != nil {
			return order, fmt.Errorf("%s line %d: %w", name, line, err)
		}
		var entry Entry
		if err := json.Unmarshal(plaintext, &entry); err != nil {
			return order, fmt.Errorf("%s line %d: %w", name, line, err)
		}
		if _, seen := entries[entry.ID]; !seen {
			order = append(order, entry.ID)
		}
		entries[entry.ID] = entry
	}
	if err := scanner.Err(); err != nil {
		return order, fmt.Errorf("read %s: %w", name, err)
	}
	return order, nil
}
