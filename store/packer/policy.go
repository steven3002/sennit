// Package packer batches sealed records into slab-sized flushes.
package packer

import (
	"time"

	"github.com/steven3002/sennit/local"
)

// A Policy decides when a queue of records becomes a flush.
//
// Every flush mints a brand-new slab, billed whole and never extendable, so the
// policy is a slab budget rather than a latency knob: flushing twice as often
// costs twice the quota for the same data.
type Policy struct {
	// IdleAfter flushes once no record has arrived for this long. It is the
	// trigger that actually fires in normal use.
	IdleAfter time.Duration
	// MaxAge bounds how long a record can exist only on this device.
	//
	// It is a durability control, not a cost control. Measured against a
	// realistic arrival process it accounts for about a fifth of slab churn,
	// so removing it would save little; what it buys is a hard ceiling on how
	// much recent memory a lost device takes with it.
	MaxAge time.Duration
	// MaxBytes flushes when the queue would no longer fit one slab.
	//
	// At realistic record sizes this fires on the order of years, a hundred
	// records a day fills a slab in over a year, so it is a bulk-import valve
	// rather than a trigger anyone will meet.
	MaxBytes int64
	// MaxRecords bounds the queue so a burst cannot grow it without limit.
	MaxRecords int
	// ClaimTimeout is how long another process's in-flight flush is respected
	// before its records are taken over. Zero means the default.
	ClaimTimeout time.Duration
}

// The settled cadence: flush after a short idle period, and in any case within
// the hour.
const (
	DefaultIdleAfter = 5 * time.Minute
	DefaultMaxAge    = time.Hour
	// DefaultClaimTimeout is generous next to a measured flush of a few
	// seconds. Taking a live flush's records over would pay for a second slab
	// holding the same records, so the cost of waiting too long is far lower
	// than the cost of waiting too little.
	DefaultClaimTimeout = 10 * time.Minute
)

// DefaultPolicy is the settled cadence for a slab of the given payload size.
func DefaultPolicy(slabBytes int64) Policy {
	return Policy{
		IdleAfter:    DefaultIdleAfter,
		MaxAge:       DefaultMaxAge,
		MaxBytes:     slabBytes,
		MaxRecords:   100_000,
		ClaimTimeout: DefaultClaimTimeout,
	}
}

// Immediate flushes after every record.
//
// It is the most expensive setting there is, one slab per record, and exists so
// an end-to-end path can be exercised without waiting on a timer, not as
// something to run a vault on.
func Immediate(slabBytes int64) Policy {
	return Policy{MaxBytes: slabBytes, MaxRecords: 1, ClaimTimeout: DefaultClaimTimeout}
}

// A Reason names why a flush fired, so the cost of a cadence can be attributed
// rather than guessed at.
type Reason string

const (
	ReasonIdle    Reason = "idle"
	ReasonAge     Reason = "age"
	ReasonBytes   Reason = "bytes"
	ReasonRecords Reason = "records"
	ReasonManual  Reason = "manual"
)

// due reports whether a queue in this state should flush, and why.
func (p Policy) due(state local.QueueState, now time.Time) (Reason, bool) {
	if state.Records == 0 {
		return "", false
	}
	if p.MaxRecords > 0 && state.Records >= p.MaxRecords {
		return ReasonRecords, true
	}
	if p.MaxBytes > 0 && state.Bytes >= p.MaxBytes {
		return ReasonBytes, true
	}
	if p.MaxAge > 0 && !state.Oldest.IsZero() && now.Sub(state.Oldest) >= p.MaxAge {
		return ReasonAge, true
	}
	if p.IdleAfter > 0 && !state.Newest.IsZero() && now.Sub(state.Newest) >= p.IdleAfter {
		return ReasonIdle, true
	}
	return "", false
}

func (p Policy) claimTimeout() time.Duration {
	if p.ClaimTimeout > 0 {
		return p.ClaimTimeout
	}
	return DefaultClaimTimeout
}

// tick is how often the deadlines are checked.
//
// The deadlines are minutes apart, so checking them often buys nothing but
// wakeups; checking them rarely turns a five-minute cadence into whatever the
// interval happens to be. A tenth of the shorter deadline keeps the error
// under a tenth of it.
func (p Policy) tick() time.Duration {
	shortest := p.IdleAfter
	if shortest <= 0 || (p.MaxAge > 0 && p.MaxAge < shortest) {
		shortest = p.MaxAge
	}
	if shortest <= 0 {
		return time.Minute
	}
	return min(max(shortest/10, time.Second), time.Minute)
}
