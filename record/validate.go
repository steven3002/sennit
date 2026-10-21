package record

import (
	"errors"
	"fmt"
	"strings"
)

// Limits on the fields a caller supplies. They exist so a malformed record is
// rejected at the boundary rather than after it is sealed and uploaded, at
// which point it is immutable.
const (
	MaxStatementBytes = 8 << 10
	MaxContextBytes   = 8 << 10
	MaxTags           = 32
	MaxTagBytes       = 64
)

// ErrInvalid is the class of every validation failure, so callers can
// distinguish a bad record from a transport or storage failure.
var ErrInvalid = errors.New("invalid record")

// Validate reports whether a memory is well formed enough to store. It is
// called before embedding, so nothing malformed reaches the index or the wire.
//
// It is a check on what is about to be written, and nothing that reads a stored
// record calls it. A record already stored is immutable, so refusing to read one
// would lose it rather than correct it.
func (m *Memory) Validate() error {
	switch {
	case m.ID.IsZero():
		return fmt.Errorf("%w: missing id", ErrInvalid)
	case m.Kind != KindMemory:
		return fmt.Errorf("%w: kind is %q, want %q", ErrInvalid, m.Kind, KindMemory)
	case m.SchemaVersion <= 0:
		return fmt.Errorf("%w: missing schemaVersion", ErrInvalid)
	case m.CreatedAt.IsZero():
		// When the vault learned a statement is what dates it wherever it is
		// shown. The time is sealed into the body, which never changes once it
		// is written, so a write that left it out would store a memory that
		// stays undated.
		return fmt.Errorf("%w: missing createdAt", ErrInvalid)
	case !m.Type.Valid():
		return fmt.Errorf("%w: type %q is not one of %s", ErrInvalid, m.Type, strings.Join(TypeNames(), ", "))
	}

	if strings.TrimSpace(m.Statement) == "" {
		return fmt.Errorf("%w: statement is empty", ErrInvalid)
	}
	if len(m.Statement) > MaxStatementBytes {
		return fmt.Errorf("%w: statement is %d bytes, limit %d", ErrInvalid, len(m.Statement), MaxStatementBytes)
	}
	// Context is required, and it is required here rather than defaulted
	// somewhere convenient. A statement stored without it is measurably harder
	// to retrieve than the same statement with it, seventeen points of hit@5,
	// half of all top-five failures, and because records are immutable the loss
	// cannot be repaired later. Rejecting the write is the only point at which
	// it is still cheap.
	if strings.TrimSpace(m.Context) == "" {
		return fmt.Errorf("%w: context is empty, and it is what makes the statement findable once "+
			"it is separated from the conversation it came from", ErrInvalid)
	}
	if len(m.Context) > MaxContextBytes {
		return fmt.Errorf("%w: context is %d bytes, limit %d", ErrInvalid, len(m.Context), MaxContextBytes)
	}

	if len(m.Tags) > MaxTags {
		return fmt.Errorf("%w: %d tags, limit %d", ErrInvalid, len(m.Tags), MaxTags)
	}
	for _, tag := range m.Tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("%w: empty tag", ErrInvalid)
		}
		if len(tag) > MaxTagBytes {
			return fmt.Errorf("%w: tag %q is %d bytes, limit %d", ErrInvalid, tag, len(tag), MaxTagBytes)
		}
	}

	if m.Importance < 0 || m.Importance > 1 {
		return fmt.Errorf("%w: importance %v is outside [0,1]", ErrInvalid, m.Importance)
	}
	if m.Confidence < 0 || m.Confidence > 1 {
		return fmt.Errorf("%w: confidence %v is outside [0,1]", ErrInvalid, m.Confidence)
	}

	if m.ValidFrom != nil && m.ValidUntil != nil && m.ValidUntil.Before(m.ValidFrom.Time) {
		return fmt.Errorf("%w: validUntil precedes validFrom", ErrInvalid)
	}
	return nil
}
