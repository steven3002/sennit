package sia

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.sia.tech/siastorage"
)

// ErrObjectGone reports that the indexer holds no object under a ref. It was
// deleted, by this installation or by another one on the same account, and
// asking again will not bring it back. Its text is the indexer's own wording.
var ErrObjectGone = errors.New("object not found")

// Download fetches a whole blob by object ref.
//
// It costs two round trips: one to the indexer to learn where the object lives,
// then the shard reads themselves. The first of the two dominates, which is why
// the layer above caches slab metadata rather than caching bodies alone.
//
// An object the indexer says it does not hold is reported as ErrObjectGone. See
// lookupFailed.
func (c *Client) Download(ctx context.Context, ref ObjectRef) ([]byte, error) {
	object, err := c.sdk.Object(ctx, ref.ID)
	if err != nil {
		return nil, lookupFailed(ref, err)
	}
	return c.read(&object, nil)
}

// DownloadRange fetches part of a blob. Reads are genuinely selective down to
// 64-byte leaves, so pulling one record out of a shared slab does not cost the
// slab.
func (c *Client) DownloadRange(ctx context.Context, ref ObjectRef, offset, length uint64) ([]byte, error) {
	object, err := c.sdk.Object(ctx, ref.ID)
	if err != nil {
		return nil, lookupFailed(ref, err)
	}
	return c.read(&object, []siastorage.DownloadOption{siastorage.WithDownloadRange(offset, length)})
}

// lookupFailed is the error a failed lookup of an object returns.
//
// An answer that the object is not there is told apart from every other
// failure, as ErrObjectGone, because it is the one a caller can act on. A
// transport failure, a refusal, a server error or a timeout says nothing about
// whether the object exists, and is returned as it was. The test is the one
// isNotFound makes for a deletion, and it is made on the SDK's answer before
// the ref goes in front of it: the message returned here names the ref, so with
// the ref in it a bare "404 page not found" would pass for the object's own.
//
// The message is the one this has always returned. Only errors.Is learns more.
func lookupFailed(ref ObjectRef, err error) error {
	if isNotFound(err, ref) {
		err = objectGone{err}
	}
	return fmt.Errorf("look up object %s: %w", ref, err)
}

// objectGone is a lookup's not-found answer, which keeps its own message and
// also matches ErrObjectGone.
type objectGone struct{ err error }

func (e objectGone) Error() string   { return e.err.Error() }
func (e objectGone) Unwrap() []error { return []error{ErrObjectGone, e.err} }

func (c *Client) read(object *siastorage.Object, opts []siastorage.DownloadOption) ([]byte, error) {
	reader, err := c.sdk.Download(*object, opts...)
	if err != nil {
		return nil, fmt.Errorf("open download: %w", err)
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read object body: %w", err)
	}
	return body, nil
}
