package sia

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

// Only an answer that the object is not there makes a lookup report it gone:
// the indexer's own wording, or "not found" naming that object. A transport
// failure, a refusal, a server error, a timeout, or a not-found that names some
// other object or none, says nothing about whether the object exists, and a
// caller that took one for a deletion would give up on a record it could still
// read. Whichever it is, the message is the one a lookup always returned.
func TestOnlyAnAbsentObjectIsReportedGone(t *testing.T) {
	t.Parallel()

	var ref, other ObjectRef
	ref.ID[0], other.ID[0] = 0x4f, 0x77
	for _, c := range []struct {
		name string
		raw  error
		gone bool
	}{
		{"the indexer's own wording", errors.New("failed to get locked object: object not found"), true},
		{"not found, naming this object", fmt.Errorf("failed to get locked object: object %s not found", ref), true},
		{"a bare 404", errors.New("failed to get locked object: 404 page not found"), false},
		{"not found, naming another object",
			fmt.Errorf("failed to get locked object: object %s not found", other), false},
		{"a slab not found", errors.New("failed to get locked object: slab 9a0c41 not found"), false},
		{"a server error", errors.New("failed to get locked object: 500 Internal Server Error"), false},
		{"a refusal", fmt.Errorf("failed to get locked object: %w", ErrUnauthorized), false},
		{"a transport failure", fmt.Errorf("failed to get locked object: %w",
			&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}), false},
		{"a timeout", fmt.Errorf("failed to get locked object: %w", context.DeadlineExceeded), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := lookupFailed(ref, c.raw)
			if gone := errors.Is(err, ErrObjectGone); gone != c.gone {
				t.Errorf("reported gone %v, want %v: %v", gone, c.gone, err)
			}
			if want := fmt.Sprintf("look up object %s: %s", ref, c.raw); err.Error() != want {
				t.Errorf("the message is %q, want the one a lookup always returned, %q", err.Error(), want)
			}
			if !errors.Is(err, c.raw) {
				t.Error("the lookup's own answer is no longer in the error")
			}
		})
	}
}
