package sia

import (
	"bytes"
	"context"
	"fmt"

	"go.sia.tech/siastorage"
)

// A Placement is where one uploaded blob landed.
type Placement struct {
	Ref  ObjectRef
	Slab SlabID
	// Bytes is the payload length as written, before erasure coding.
	Bytes int
}

// A Batch is one finalized packed upload, not yet pinned.
//
// It holds the SDK's object descriptors so pinning can be split, the slabs
// once for the whole batch, then the objects, without those descriptors
// escaping this package.
type Batch struct {
	objects    []siastorage.Object
	placements []Placement
}

// Len reports how many blobs the batch carries.
func (b *Batch) Len() int { return len(b.objects) }

// Placements reports where each blob landed, in the order it was added.
func (b *Batch) Placements() []Placement { return b.placements }

// Slabs lists the distinct slabs the batch occupies.
func (b *Batch) Slabs() []SlabID {
	seen := make(map[SlabID]struct{}, 1)
	var out []SlabID
	for _, p := range b.placements {
		if p.Slab == "" {
			continue
		}
		if _, ok := seen[p.Slab]; ok {
			continue
		}
		seen[p.Slab] = struct{}{}
		out = append(out, p.Slab)
	}
	return out
}

// UploadPacked writes a batch of blobs into shared slabs and returns where each
// landed, unpinned.
//
// There is no single-blob variant on purpose. The indexer bills a whole slab
// however little it holds, so a lone small record costs roughly four orders of
// magnitude more than its own size. Packing is the write path, not an
// optimisation of it.
func (c *Client) UploadPacked(ctx context.Context, payloads [][]byte, opts ...UploadOption) (*Batch, error) {
	if len(payloads) == 0 {
		return &Batch{}, nil
	}
	options := make([]siastorage.UploadOption, 0, len(opts)+1)
	options = append(options, siastorage.WithRedundancy(DataShards, ParityShards))
	for _, opt := range opts {
		options = append(options, siastorage.UploadOption(opt))
	}

	upload, err := c.sdk.UploadPacked(options...)
	if err != nil {
		return nil, fmt.Errorf("open packed upload: %w", err)
	}
	defer upload.Close()

	for i, payload := range payloads {
		if _, err := upload.Add(ctx, bytes.NewReader(payload)); err != nil {
			return nil, fmt.Errorf("pack blob %d of %d: %w", i+1, len(payloads), err)
		}
	}

	objects, err := upload.Finalize(ctx)
	if err != nil {
		return nil, fmt.Errorf("finalize packed upload: %w", err)
	}
	if len(objects) != len(payloads) {
		return nil, fmt.Errorf("packed upload returned %d objects for %d blobs", len(objects), len(payloads))
	}

	batch := &Batch{objects: objects, placements: make([]Placement, len(objects))}
	for i := range objects {
		batch.placements[i] = Placement{
			Ref:   ObjectRef{ID: objects[i].ID()},
			Slab:  slabOf(&objects[i]),
			Bytes: len(payloads[i]),
		}
	}
	return batch, nil
}

// SlabPayloadSize reports how many payload bytes one slab holds, which is what
// a flush is billed for whether it uses them or not.
func (c *Client) SlabPayloadSize() (int64, error) {
	upload, err := c.sdk.UploadPacked(siastorage.WithRedundancy(DataShards, ParityShards))
	if err != nil {
		return 0, fmt.Errorf("open packed upload: %w", err)
	}
	defer upload.Close()
	return upload.OptimalDataSize(), nil
}

// DataShards and ParityShards are the erasure coding every write uses.
//
// They are the SDK's own defaults, and they are passed explicitly rather than
// left implicit so that the numbers are a fact of this code: the slab quantum is
// DataShards x 4 MiB, and a slab costs DataShards+ParityShards shard uploads, so
// a caller that reports upload progress has an exact denominator instead of an
// assumption about a default that could change under it. The bytes on the wire
// are unchanged.
const (
	DataShards   uint8 = 10
	ParityShards uint8 = 20
)

// SectorSize is what one shard holds, which with DataShards sets the slab
// quantum.
const SectorSize = 4 << 20

// ShardUploads reports how many shard uploads a packed write of this many
// payload bytes performs.
//
// It is exact rather than estimated. A packed upload fills whole slabs of
// DataShards x SectorSize payload bytes, encryption preserves length, and every
// slab is written as DataShards+ParityShards shards.
func ShardUploads(payloadBytes int64) int64 {
	quantum := int64(DataShards) * SectorSize
	slabs := (payloadBytes + quantum - 1) / quantum
	return slabs * int64(DataShards+ParityShards)
}

// A ShardUploaded reports one shard of one slab safely written to a host.
type ShardUploaded struct {
	// Slab is the slab's index within this upload, and Shard the shard's index
	// within the slab.
	Slab, Shard int
	// Bytes is the shard's size on the wire.
	Bytes uint64
}

// An UploadOption tunes a write. Erasure-coding parameters are the dominant
// cost lever, so they stay reachable from above without exposing the SDK.
type UploadOption siastorage.UploadOption

// WithShardProgress reports each shard as it completes, so a caller can show
// how far an upload has gone.
//
// The callback runs on the goroutine that finished the shard, so several may run
// at once and none of them may block: an upload's throughput is the cost of
// holding one up. A shard is reported once, after its first successful write.
func WithShardProgress(fn func(ShardUploaded)) UploadOption {
	return UploadOption(siastorage.WithUploadProgress(shardProgress(fn)))
}

// shardProgress adapts the SDK's progress type, which stops at this package's
// boundary like every other type it defines.
func shardProgress(fn func(ShardUploaded)) func(siastorage.ShardProgress) {
	return func(p siastorage.ShardProgress) {
		fn(ShardUploaded{Slab: p.SlabIndex, Shard: p.ShardIndex, Bytes: p.ShardSize})
	}
}

// WithRedundancy overrides the erasure coding for one upload. The slab quantum
// is dataShards x 4 MiB, so this changes what a flush costs.
func WithRedundancy(dataShards, parityShards uint8) UploadOption {
	return UploadOption(siastorage.WithRedundancy(dataShards, parityShards))
}

// slabOf names the slab a packed object sits in. Every object in one flush
// shares it, which is what makes pinning slabs once per flush correct.
func slabOf(obj *siastorage.Object) SlabID {
	slices := obj.Slabs()
	if len(slices) == 0 {
		return ""
	}
	return SlabID(slices[0].Digest().String())
}
