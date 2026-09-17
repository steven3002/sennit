package store

import (
	"sync"
	"testing"

	"github.com/steven3002/sennit/sia"
)

// An upload reports its shards from several goroutines at once. The count never
// passes the total, whatever order the reports arrive in, because a percentage
// over a hundred is a bug a reader cannot unsee.
func TestShardCountIsCappedUnderConcurrentReports(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	var s Store
	s.Observe(func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	const total = 30
	count := s.countShards(total)
	var wg sync.WaitGroup
	for range total + 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count(sia.ShardUploaded{})
		}()
	}
	wg.Wait()

	var highest int64
	for _, e := range events {
		if e.Kind != Uploading || e.Total != total {
			t.Fatalf("event %+v, want an upload of %d shards", e, total)
		}
		if e.Done > total {
			t.Errorf("reported %d of %d shards", e.Done, total)
		}
		highest = max(highest, e.Done)
	}
	if highest != total {
		t.Errorf("the highest report was %d of %d", highest, total)
	}
}

// Without an observer the write is exactly what it was before: no option is
// added to the upload.
func TestNoObserverAddsNoUploadOption(t *testing.T) {
	var s Store
	if opts := s.uploadProgress(1018); opts != nil {
		t.Errorf("%d upload option(s) added for a caller that observes nothing", len(opts))
	}
}

func TestTheTotalIsReportedBeforeTheFirstShard(t *testing.T) {
	var events []Event
	var s Store
	s.Observe(func(e Event) { events = append(events, e) })
	s.uploadProgress(1018)
	if len(events) != 1 || events[0].Total != int64(sia.ShardUploads(1018)) || events[0].Done != 0 {
		t.Errorf("events %+v, want one upload start carrying the total", events)
	}
}
