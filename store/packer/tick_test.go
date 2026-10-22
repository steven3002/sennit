package packer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

// The background does its tick work on a tick with nothing queued, and so no
// flush due, and hands the work's error to the same place a failed flush goes.
//
// The work is how a vault deletes the copy of a record forgotten on this device.
// A quiet server has nothing queued, and a vault that waited for a flush to be
// due would owe that deletion until something else happened to write.
func TestTheBackgroundDoesItsTickWorkWithNothingQueued(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	policy := packer.DefaultPolicy(40 << 20)
	policy.IdleAfter = time.Second
	// The store is never written through: with nothing queued no flush is due,
	// and none runs. It only has to exist for the background to start.
	p, err := packer.New(&store.Store{}, device, policy)
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}

	worked := make(chan struct{}, 1)
	refusal := errors.New("the indexer did not answer")
	p.OnTick(func(context.Context) error {
		select {
		case worked <- struct{}{}:
		default:
		}
		return refusal
	})
	reported := make(chan error, 1)
	p.Start(t.Context(), func(err error) {
		select {
		case reported <- err:
		default:
		}
	})
	defer p.Stop()

	select {
	case <-worked:
	case <-time.After(5 * time.Second):
		t.Fatal("no tick did the work in five seconds, with nothing queued")
	}
	select {
	case err := <-reported:
		if !errors.Is(err, refusal) {
			t.Errorf("the background reported %v, want the work's own error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the work's error was not reported")
	}
	if pending := p.Pending(); pending != 0 {
		t.Errorf("%d record(s) queued, want none", pending)
	}
}
