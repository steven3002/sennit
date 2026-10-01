package packer_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store/packer"
)

// Several records are withdrawn together or not at all. A flush holding any one
// of them refuses the lot, and the ones it does not hold stay queued for it.
//
// They are the chunks of one conversation. Withdrawing the free ones and then
// meeting the claim would take part of the conversation off the queue and leave
// the rest to the flush, which is neither the conversation forgotten nor the
// conversation kept.
func TestSeveralRecordsAreWithdrawnTogetherOrNotAtAll(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	first, second, other := queued(t, 256), queued(t, 256), queued(t, 128)
	for _, item := range []packer.Queued{first, second, other} {
		if _, err := p.Add(t.Context(), item); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	// A flush holds the second record and nothing else.
	claimed, err := device.ClaimQueued("flush", packer.DefaultClaimTimeout, 0)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claim: %d record(s), %v", len(claimed), err)
	}
	if err := device.ReleaseQueued([]record.ID{first.ID, other.ID}); err != nil {
		t.Fatalf("release: %v", err)
	}

	withdrawn, err := p.WithdrawAll([]record.ID{first.ID, second.ID})
	if !errors.Is(err, local.ErrClaimed) || len(withdrawn) != 0 {
		t.Fatalf("withdrawing a set a flush partly holds returned %v, %v; want ErrClaimed and nothing", withdrawn, err)
	}
	if p.Pending() != 3 {
		t.Errorf("%d record(s) pending after a refused withdrawal, want all 3", p.Pending())
	}
	free, err := device.ClaimQueued("next flush", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var ids []record.ID
	for _, blob := range free {
		ids = append(ids, blob.ID)
	}
	if !slices.Equal(ids, []record.ID{first.ID, other.ID}) {
		t.Fatalf("the queue offers %v to the next flush, want the two no flush holds", ids)
	}
	if err := device.ReleaseQueued(ids); err != nil {
		t.Fatalf("release: %v", err)
	}

	// A claim past its timeout was left by a flush that is no longer running
	// and holds nothing back. A timeout of zero makes every claim one.
	withdrawn, err = device.WithdrawQueuedAll([]record.ID{first.ID, second.ID}, 0)
	if err != nil || !slices.Equal(withdrawn, []record.ID{first.ID, second.ID}) {
		t.Fatalf("withdrawing past an expired claim returned %v, %v", withdrawn, err)
	}
	state, err := device.QueueState()
	if err != nil || state.Records != 1 {
		t.Fatalf("the queue holds %d record(s) after the withdrawal, want the one left out of it: %v",
			state.Records, err)
	}
}

// Only what was queued is reported withdrawn, a record that is not queued is no
// error, and the packer's own counts drop with the rows.
func TestWithdrawingSeveralRecordsReportsWhichWereQueued(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(nil, device, packer.DefaultPolicy(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	kept, gone := queued(t, 256), queued(t, 512)
	for _, item := range []packer.Queued{kept, gone} {
		if _, err := p.Add(t.Context(), item); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	never := queued(t, 64)

	withdrawn, err := p.WithdrawAll([]record.ID{never.ID, gone.ID})
	if err != nil || !slices.Equal(withdrawn, []record.ID{gone.ID}) {
		t.Fatalf("withdrawing one queued record and one never queued returned %v, %v", withdrawn, err)
	}
	if p.Pending() != 1 || p.PendingBytes() != 256 {
		t.Errorf("%d record(s) of %d bytes pending after the withdrawal, want 1 of 256", p.Pending(), p.PendingBytes())
	}
	if withdrawn, err := p.WithdrawAll([]record.ID{gone.ID}); err != nil || len(withdrawn) != 0 {
		t.Errorf("withdrawing the same record again returned %v, %v", withdrawn, err)
	}
	if withdrawn, err := p.WithdrawAll(nil); err != nil || len(withdrawn) != 0 {
		t.Errorf("withdrawing nothing returned %v, %v", withdrawn, err)
	}
}
