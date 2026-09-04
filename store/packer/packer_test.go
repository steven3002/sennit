package packer_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

func deviceStore(t *testing.T, dir string) *local.Store {
	t.Helper()
	device, err := local.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatalf("open device store: %v", err)
	}
	t.Cleanup(func() { device.Close() })
	return device
}

func queued(t *testing.T, size int) packer.Queued {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	return packer.Queued{
		ID:   id,
		Kind: record.KindMemory,
		Blob: store.Blob{CID: id.String(), Payload: make([]byte, size)},
	}
}

// A queued record is not on the network. The whole point of holding the queue
// on disk is that the process ending is not the same event as the record being
// lost, so a second packer over the same vault must find the work waiting.
func TestQueueSurvivesTheProcess(t *testing.T) {
	dir := t.TempDir()
	device := deviceStore(t, dir)

	first, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	const count = 5
	for range count {
		if _, err := first.Add(t.Context(), queued(t, 512)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if first.Pending() != count {
		t.Fatalf("%d records pending, want %d", first.Pending(), count)
	}
	first.Stop()

	second, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("reopen packer: %v", err)
	}
	if second.Pending() != count {
		t.Fatalf("a fresh packer found %d queued records, want %d", second.Pending(), count)
	}
	if got := second.PendingBytes(); got != count*512 {
		t.Fatalf("queue holds %d bytes, want %d", got, count*512)
	}
}

// An offline vault must still owe the network the record. Writing it to the
// device and queueing nothing is the failure this covers: the record would look
// stored forever and never be.
func TestOfflineWritesAreQueuedNotStranded(t *testing.T) {
	device := deviceStore(t, t.TempDir())

	offline, err := packer.New(nil, device, packer.Immediate(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	// Immediate would flush this at once if there were anywhere to flush to.
	result, err := offline.Add(t.Context(), queued(t, 128))
	if err != nil {
		t.Fatalf("add while offline: %v", err)
	}
	if !result.Empty() {
		t.Fatal("an offline packer reported a write")
	}
	if offline.Pending() != 1 {
		t.Fatalf("%d records queued while offline, want 1", offline.Pending())
	}
	if _, err := offline.Flush(t.Context()); !errors.Is(err, packer.ErrOffline) {
		t.Fatalf("flushing offline returned %v, want ErrOffline", err)
	}
	if offline.Pending() != 1 {
		t.Fatal("a failed offline flush lost the queued record")
	}
}

func TestPolicyTriggers(t *testing.T) {
	blob := 512
	for _, tc := range []struct {
		name   string
		policy packer.Policy
		add    int
		wait   time.Duration
		want   packer.Reason
		due    bool
	}{
		{
			name:   "idle",
			policy: packer.Policy{IdleAfter: 20 * time.Millisecond, MaxAge: time.Hour},
			add:    2,
			wait:   40 * time.Millisecond,
			want:   packer.ReasonIdle,
			due:    true,
		},
		{
			// The age cap is a durability control: it bounds how much recent
			// memory a lost device takes with it, and fires even while records
			// are still arriving.
			name:   "age cap",
			policy: packer.Policy{IdleAfter: time.Hour, MaxAge: 20 * time.Millisecond},
			add:    2,
			wait:   40 * time.Millisecond,
			want:   packer.ReasonAge,
			due:    true,
		},
		{
			name:   "no slab room",
			policy: packer.Policy{IdleAfter: time.Hour, MaxAge: time.Hour, MaxBytes: int64(blob) * 2},
			add:    2,
			want:   packer.ReasonBytes,
			due:    true,
		},
		{
			name:   "not yet due",
			policy: packer.DefaultPolicy(40 << 20),
			add:    3,
		},
		{
			name:   "empty queue never flushes",
			policy: packer.Policy{IdleAfter: time.Nanosecond, MaxAge: time.Nanosecond},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := deviceStore(t, t.TempDir())
			p, err := packer.New(nil, device, tc.policy)
			if err != nil {
				t.Fatalf("new packer: %v", err)
			}
			for range tc.add {
				if _, err := p.Add(t.Context(), queued(t, blob)); err != nil {
					t.Fatalf("add: %v", err)
				}
			}
			reason, due := p.Due(time.Now().Add(tc.wait))
			if due != tc.due {
				t.Fatalf("due=%v (%q), want %v", due, reason, tc.due)
			}
			if due && reason != tc.want {
				t.Fatalf("flushed for %q, want %q", reason, tc.want)
			}
		})
	}
}

// The budget is the sealed, framed payload, not the plaintext. Budgeting in
// plaintext would understate every record by the envelope and framing, and the
// ceiling it guards is a slab that cannot be extended once it is short.
func TestQueueBudgetsCiphertext(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}

	const sealedSize = 897 // a measured record: 857 sealed plus the 40 byte envelope
	if _, err := p.Add(t.Context(), queued(t, sealedSize)); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := p.PendingBytes(); got != sealedSize {
		t.Fatalf("queue counted %d bytes for a %d byte sealed record", got, sealedSize)
	}
}

// Two processes over one vault must not both pay for a slab holding the same
// records. The claim is what prevents it, and it is taken in one statement so
// there is no window between deciding and claiming.
func TestClaimingIsExclusiveAcrossPackers(t *testing.T) {
	dir := t.TempDir()
	device := deviceStore(t, dir)

	writer, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	for range 4 {
		if _, err := writer.Add(t.Context(), queued(t, 256)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	first, err := device.ClaimQueued("first", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("first claim took %d records, want 4", len(first))
	}
	second, err := device.ClaimQueued("second", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("a second claim took %d records that were already in flight", len(second))
	}

	// A process that dies mid-flush must not hold its records forever.
	stale, err := device.ClaimQueued("third", 0, 0)
	if err != nil {
		t.Fatalf("stale claim: %v", err)
	}
	if len(stale) != 4 {
		t.Fatalf("an expired claim released %d records, want 4", len(stale))
	}
}

// A flush that fails leaves the records queued. They are the user's memories
// and the network has not got them, so dropping them is the one outcome that
// is not allowed.
func TestFailedFlushReturnsRecordsToTheQueue(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	for range 3 {
		if _, err := p.Add(t.Context(), queued(t, 256)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	claimed, err := device.ClaimQueued("flush", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	ids := make([]record.ID, len(claimed))
	for i, blob := range claimed {
		ids[i] = blob.ID
	}
	if err := device.ReleaseQueued(ids); err != nil {
		t.Fatalf("release: %v", err)
	}

	again, err := device.ClaimQueued("retry", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(again) != 3 {
		t.Fatalf("a released batch came back with %d records, want 3", len(again))
	}
}

// Records must reach a slab in the order they were remembered, whatever order
// the database hands them back in.
func TestQueuePreservesArrivalOrder(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}

	want := make([]record.ID, 0, 32)
	for range 32 {
		item := queued(t, 64)
		if _, err := p.Add(t.Context(), item); err != nil {
			t.Fatalf("add: %v", err)
		}
		want = append(want, item.ID)
	}

	claimed, err := device.ClaimQueued("order", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != len(want) {
		t.Fatalf("claimed %d of %d records", len(claimed), len(want))
	}
	for i, blob := range claimed {
		if blob.ID != want[i] {
			t.Fatalf("record %d is %s, want %s", i, blob.ID, want[i])
		}
	}
}

func TestStopIsSafeWithoutStart(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	p.Stop()
	p.Stop()
}

func TestPackerNeedsADeviceStore(t *testing.T) {
	if _, err := packer.New(nil, nil, packer.DefaultPolicy(40<<20)); err == nil {
		t.Fatal("a packer was built with nowhere durable to queue")
	}
}
