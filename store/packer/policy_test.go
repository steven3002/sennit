package packer_test

import (
	"testing"
	"time"

	"github.com/steven3002/sennit/store/packer"
)

// The cadence is a slab budget: flushing twice as often costs twice the quota
// for the same data, because a partially filled slab can never be extended.
func TestDefaultPolicyIsTheSettledCadence(t *testing.T) {
	policy := packer.DefaultPolicy(40 << 20)
	if policy.IdleAfter != 5*time.Minute {
		t.Fatalf("idle deadline is %v, want 5m", policy.IdleAfter)
	}
	if policy.MaxAge != time.Hour {
		t.Fatalf("age cap is %v, want 1h", policy.MaxAge)
	}
	if policy.MaxBytes != 40<<20 {
		t.Fatalf("byte ceiling is %d, want one slab", policy.MaxBytes)
	}
}

func TestImmediateFlushesEveryRecord(t *testing.T) {
	if got := packer.Immediate(40 << 20).MaxRecords; got != 1 {
		t.Fatalf("immediate policy batches %d records", got)
	}
}
