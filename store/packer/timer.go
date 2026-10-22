package packer

import (
	"context"
	"time"
)

// Start begins firing the age deadlines in the background.
//
// Without it the idle and maximum-age triggers are inert: a record written and
// then left alone would sit on the device until something else happened to
// write, which is precisely the case the deadlines exist for. A vault that is
// open but quiet is the normal case, not an edge one.
//
// Errors from a background flush are handed to onError rather than dropped; the
// records stay queued, so the next tick tries again.
func (p *Packer) Start(ctx context.Context, onError func(error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil || p.store == nil {
		return
	}
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go p.run(ctx, p.stop, p.done, onError)
}

// Stop ends background flushing and waits for an in-flight one to finish.
// Whatever is still queued stays queued and durable.
func (p *Packer) Stop() {
	p.mu.Lock()
	stop, done := p.stop, p.done
	p.stop, p.done = nil, nil
	p.mu.Unlock()

	if stop == nil {
		return
	}
	close(stop)
	<-done
}

func (p *Packer) run(ctx context.Context, stop <-chan struct{}, done chan<- struct{}, onError func(error)) {
	defer close(done)

	ticker := time.NewTicker(p.policy.tick())
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if reason, due := p.Due(now); due {
				if _, err := p.flush(ctx, reason); err != nil && onError != nil {
					onError(err)
				}
			}
			if err := p.tick(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

// OnTick registers work for the background deadlines to do on every tick,
// whether or not a flush is due.
//
// A queued record is not all that a vault owes the network. The copy of a
// record forgotten on this device is owed as well, and a vault that is open but
// quiet has nothing queued to make a flush due, so without this it would owe
// that copy until something else happened to write. The work gets the
// background's context, its error goes to the same onError a failed flush does,
// and the next tick runs it again.
func (p *Packer) OnTick(fn func(context.Context) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onTick = fn
}

func (p *Packer) tick(ctx context.Context) error {
	p.mu.Lock()
	work := p.onTick
	p.mu.Unlock()
	if work == nil {
		return nil
	}
	return work(ctx)
}
