package sia

import (
	"sync"
	"testing"

	"go.sia.tech/siastorage"
)

// The denominator an upload's progress is reported against is arithmetic, not
// an estimate: whole slabs of DataShards x 4 MiB, each written as data plus
// parity shards.
func TestShardUploadsCountsWholeSlabs(t *testing.T) {
	slab := int64(DataShards) * SectorSize
	perSlab := int64(DataShards + ParityShards)
	for _, c := range []struct {
		bytes int64
		want  int64
	}{
		{0, 0},
		{1, perSlab},
		{1018, perSlab},
		{slab, perSlab},
		{slab + 1, 2 * perSlab},
		{4 * slab, 4 * perSlab},
	} {
		if got := ShardUploads(c.bytes); got != c.want {
			t.Errorf("ShardUploads(%d) = %d, want %d", c.bytes, got, c.want)
		}
	}
	if slab != 40<<20 {
		t.Errorf("a slab holds %d payload bytes, and the published figure is 40 MiB", slab)
	}
}

// The SDK calls back from each shard's own goroutine, so the adapter is used
// concurrently. It carries no SDK type across the boundary.
func TestShardProgressIsSafeUnderConcurrentCalls(t *testing.T) {
	var mu sync.Mutex
	var got []ShardUploaded
	adapt := shardProgress(func(s ShardUploaded) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
	})

	var wg sync.WaitGroup
	for shard := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			adapt(siastorage.ShardProgress{SlabIndex: 1, ShardIndex: shard, ShardSize: 4 << 20})
		}()
	}
	wg.Wait()

	if len(got) != 30 {
		t.Fatalf("%d shards reported, want 30", len(got))
	}
	seen := make(map[int]bool, 30)
	for _, s := range got {
		if s.Slab != 1 || s.Bytes != 4<<20 {
			t.Errorf("shard reported as %+v", s)
		}
		seen[s.Shard] = true
	}
	if len(seen) != 30 {
		t.Errorf("%d distinct shard indexes, want 30", len(seen))
	}
}
