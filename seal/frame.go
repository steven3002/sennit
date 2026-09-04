package seal

import (
	"encoding/binary"
	"fmt"

	"github.com/steven3002/sennit/record"
)

// The framing header. A frame carries the record's own identity next to its
// ciphertext, so a vault can be reconstructed from the seed and the indexer
// alone: the catalog becomes a performance index rather than something whose
// loss destroys the data.
const (
	frameVersion = 1
	frameIDLen   = record.IDSize
)

var frameMagic = [2]byte{'M', 'n'}

// Kind codes are written instead of names so the per-record framing cost stays
// flat as the kind vocabulary grows.
const (
	kindCodeMemory  = 1
	kindCodeSession = 2
	kindCodeChunk   = 3
)

// A Frame is one framed record recovered from a blob.
type Frame struct {
	Kind   record.Kind
	ID     record.ID
	Sealed []byte
}

// FrameRecord wraps a sealed body with its identity.
func FrameRecord(kind record.Kind, id record.ID, sealed []byte) ([]byte, error) {
	code, err := kindCode(kind)
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, 5+frameIDLen+len(sealed))
	body = append(body, frameMagic[0], frameMagic[1], frameVersion, code, frameIDLen)
	body = append(body, id[:]...)
	body = append(body, sealed...)

	out := binary.AppendUvarint(make([]byte, 0, binary.MaxVarintLen64+len(body)), uint64(len(body)))
	return append(out, body...), nil
}

// Unframe reads the first frame in b and returns it with the remaining bytes,
// so a packed blob holding many records can be walked forward.
func Unframe(b []byte) (Frame, []byte, error) {
	length, n := binary.Uvarint(b)
	if n <= 0 {
		return Frame{}, nil, fmt.Errorf("%w: unreadable frame length", ErrCorrupt)
	}
	b = b[n:]
	if uint64(len(b)) < length {
		return Frame{}, nil, fmt.Errorf("%w: frame claims %d bytes, %d remain", ErrCorrupt, length, len(b))
	}
	body, rest := b[:length], b[length:]

	if len(body) < 5 {
		return Frame{}, nil, fmt.Errorf("%w: frame header truncated", ErrCorrupt)
	}
	if body[0] != frameMagic[0] || body[1] != frameMagic[1] {
		return Frame{}, nil, fmt.Errorf("%w: bad frame magic", ErrCorrupt)
	}
	if body[2] != frameVersion {
		return Frame{}, nil, fmt.Errorf("%w: frame version %d, this build writes %d", ErrCorrupt, body[2], frameVersion)
	}
	kind, err := kindFromCode(body[3])
	if err != nil {
		return Frame{}, nil, err
	}
	idLen := int(body[4])
	if len(body) < 5+idLen {
		return Frame{}, nil, fmt.Errorf("%w: frame id truncated", ErrCorrupt)
	}
	if idLen != frameIDLen {
		return Frame{}, nil, fmt.Errorf("%w: frame id is %d bytes, want %d", ErrCorrupt, idLen, frameIDLen)
	}
	var id record.ID
	copy(id[:], body[5:5+idLen])

	return Frame{Kind: kind, ID: id, Sealed: body[5+idLen:]}, rest, nil
}

// Frames walks a packed blob and returns every record in it.
//
// It returns what it recovered alongside the reason it stopped, rather than
// discarding the lot on the first bad byte. That is the whole point of framing:
// a blob that has been truncated or damaged still yields every record before
// the damage, so a fault costs the tail of one object instead of everything in
// it. Callers that require the blob to be intact check the error; callers
// rebuilding a vault take the records and note the loss.
func Frames(blob []byte) ([]Frame, error) {
	var out []Frame
	for len(blob) > 0 {
		frame, rest, err := Unframe(blob)
		if err != nil {
			return out, err
		}
		out = append(out, frame)
		blob = rest
	}
	return out, nil
}

// FrameOverhead is the framing cost for one record id of the current size.
func FrameOverhead(sealedLen int) int {
	body := 5 + frameIDLen + sealedLen
	return binary.PutUvarint(make([]byte, binary.MaxVarintLen64), uint64(body)) + body - sealedLen
}

func kindCode(kind record.Kind) (byte, error) {
	switch kind {
	case record.KindMemory:
		return kindCodeMemory, nil
	case record.KindSession:
		return kindCodeSession, nil
	case record.KindChunk:
		return kindCodeChunk, nil
	default:
		return 0, fmt.Errorf("frame: unknown kind %q", kind)
	}
}

func kindFromCode(code byte) (record.Kind, error) {
	switch code {
	case kindCodeMemory:
		return record.KindMemory, nil
	case kindCodeSession:
		return record.KindSession, nil
	case kindCodeChunk:
		return record.KindChunk, nil
	default:
		return "", fmt.Errorf("%w: unknown kind code %d", ErrCorrupt, code)
	}
}
