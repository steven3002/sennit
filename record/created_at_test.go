package record_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steven3002/sennit/record"
)

// A memory with no creation time is refused before it is stored.
//
// The time is sealed into the body and the body never changes, so a write path
// that forgot to stamp it would store a memory that stays undated. The one write
// path today always stamps it; this is what catches the next one if it does not.
func TestValidateRefusesAMemoryWithNoCreationTime(t *testing.T) {
	memory := valid(t)
	memory.CreatedAt = record.Time{}

	err := memory.Validate()
	if !errors.Is(err, record.ErrInvalid) {
		t.Fatalf("a memory with no creation time returned %v, want an ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "createdAt") {
		t.Errorf("the refusal %q does not name the field", err)
	}
}
