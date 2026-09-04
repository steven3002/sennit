package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
)

// Fetch resolves a record id to the record it names, cheapest source first.
//
// A session head is answered from this device and nothing else, because a head
// is never packed into a slab: it changes on every append, and slabs hold
// immutable content. Everything else, memories, transcript chunks, is
// resolved through the three storage tiers.
func (v *Vault) Fetch(ctx context.Context, id record.ID) (recall.Found, recall.Tier, error) {
	if session, err := v.session(id); err == nil {
		return recall.Found{Session: session}, recall.TierLocal, nil
	} else if !errors.Is(err, local.ErrNotFound) {
		return recall.Found{}, "", err
	}

	kind, body, tier, err := v.fetchBody(ctx, id)
	if err != nil {
		return recall.Found{}, "", err
	}
	switch kind {
	case record.KindMemory:
		memory, err := record.Unmarshal(body)
		return recall.Found{Memory: memory}, tier, err
	default:
		return recall.Found{}, "", fmt.Errorf("record %s is a %s and is not something recall returns",
			id, kind)
	}
}

// FetchMemory resolves a record id that is expected to be a memory.
func (v *Vault) FetchMemory(ctx context.Context, id record.ID) (*record.Memory, recall.Tier, error) {
	found, tier, err := v.Fetch(ctx, id)
	if err != nil {
		return nil, tier, err
	}
	if found.Memory == nil {
		return nil, tier, fmt.Errorf("record %s is not a memory", id)
	}
	return found.Memory, tier, nil
}

// fetchBody resolves a record id to its decrypted body, cheapest source first.
//
// The order is not a cache hierarchy bolted on afterwards; the three sources
// cost about an order of magnitude apart, and which one answered is the main
// thing that explains a read's latency. Each is counted as it happens, because
// the hit rates are a property of how the vault is used and cannot be worked
// out later from anything else it stores.
func (v *Vault) fetchBody(ctx context.Context, id record.ID) (record.Kind, []byte, recall.Tier, error) {
	start := time.Now()

	if kind, body, err := v.local.GetRecord(id); err == nil {
		v.countRead(recall.TierLocal, start)
		return kind, body, recall.TierLocal, nil
	} else if !errors.Is(err, local.ErrNotFound) {
		return "", nil, "", err
	}
	v.countMiss(recall.TierLocal)

	entry, err := v.manifest.Lookup(id)
	if err != nil {
		return "", nil, "", err
	}
	if v.client == nil {
		return "", nil, "", fmt.Errorf("%s is not held on this device: %w", id, errOffline)
	}
	ref, err := sia.ParseObjectRef(entry.ObjectRef)
	if err != nil {
		return "", nil, "", err
	}

	payload, tier, err := v.fetchRemote(ctx, entry, ref)
	if err != nil {
		return "", nil, "", err
	}
	body, err := v.openBody(id, payload)
	if err != nil {
		return "", nil, "", err
	}
	// A record fetched from the network is now held here, so the next read of
	// it is free.
	if err := v.local.PutBody(id, entry.Kind, body); err != nil {
		return "", nil, "", err
	}
	v.countRead(tier, start)
	return entry.Kind, body, tier, nil
}

// fetchRemote reads a record's bytes from the network, using cached location
// metadata when it is fresh enough to trust.
func (v *Vault) fetchRemote(ctx context.Context, entry manifest.Entry, ref sia.ObjectRef) ([]byte, recall.Tier, error) {
	if payload, ok, err := v.fetchCached(ctx, entry, ref); err != nil {
		return nil, "", err
	} else if ok {
		return payload, recall.TierCached, nil
	}
	v.countMiss(recall.TierCached)

	location, shared, err := v.client.LocationFor(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	payload, err := v.client.Download(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	if err := v.cacheLocation(location, shared); err != nil {
		return nil, "", err
	}
	return payload, recall.TierNetwork, nil
}

// fetchCached tries the cached location, reporting whether it answered.
//
// Anything short of a clean success falls through to the indexer rather than
// failing the read: metadata that has stopped working is exactly what the
// expiry exists for, and the whole point of the tier is that missing it costs
// latency and nothing else. A location that failed is dropped rather than left
// to fail again on every subsequent read.
func (v *Vault) fetchCached(ctx context.Context, entry manifest.Entry, ref sia.ObjectRef) ([]byte, bool, error) {
	cached, err := v.local.GetLocation(entry.ObjectRef)
	if errors.Is(err, local.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if cached.Age() >= v.opts.SlabMetaTTL {
		return nil, false, nil
	}

	shared := make(map[sia.SlabID][]byte, len(cached.SlabIDs))
	slabIDs := make([]sia.SlabID, 0, len(cached.SlabIDs))
	for _, slabID := range cached.SlabIDs {
		slabIDs = append(slabIDs, sia.SlabID(slabID))
		slab, err := v.local.GetSlabMeta(slabID)
		if errors.Is(err, local.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		// The object's half and the slab's half expire independently, and the
		// older of the two is what the read is actually trusting.
		if slab.Age() >= v.opts.SlabMetaTTL {
			return nil, false, nil
		}
		shared[sia.SlabID(slabID)] = slab.Meta
	}

	payload, err := v.client.DownloadAt(ctx, sia.Location{
		Ref:   ref,
		Slabs: slabIDs,
		Bytes: cached.Meta,
	}, shared)
	if err != nil {
		return nil, false, v.forgetLocation(cached)
	}
	return payload, true, nil
}

func (v *Vault) cacheLocation(location sia.Location, shared []sia.SlabLocation) error {
	for _, slab := range shared {
		if err := v.local.PutSlabMeta(string(slab.ID), slab.Bytes); err != nil {
			return err
		}
	}
	slabIDs := make([]string, len(location.Slabs))
	for i, id := range location.Slabs {
		slabIDs[i] = string(id)
	}
	return v.local.PutLocation(location.Ref.String(), slabIDs, location.Bytes)
}

func (v *Vault) forgetLocation(cached local.CachedLocation) error {
	if err := v.local.ForgetLocation(cached.ObjectRef); err != nil {
		return err
	}
	for _, slabID := range cached.SlabIDs {
		if err := v.local.ForgetSlabMeta(slabID); err != nil {
			return err
		}
	}
	return nil
}

func (v *Vault) countRead(tier recall.Tier, start time.Time) {
	// A failure to count a read is not a reason to fail the read.
	_ = v.local.RecordRead(string(tier), time.Since(start))
}

func (v *Vault) countMiss(tier recall.Tier) { _ = v.local.RecordMiss(string(tier)) }

// ReadStats reports how each tier of the read hierarchy has performed over the
// life of this vault, not just this process.
func (v *Vault) ReadStats() ([]local.TierStat, error) { return v.local.ReadStats() }

// CacheSize measures what the location cache costs on disk.
func (v *Vault) CacheSize() (local.CacheSize, error) { return v.local.CacheSize() }

// openBody unframes and decrypts one stored record, returning the exact bytes
// that were sealed.
func (v *Vault) openBody(id record.ID, payload []byte) ([]byte, error) {
	frame, _, err := seal.Unframe(payload)
	if err != nil {
		return nil, fmt.Errorf("record %s: %w", id, err)
	}
	if frame.ID != id {
		return nil, fmt.Errorf("record %s: the stored blob identifies itself as %s", id, frame.ID)
	}
	body, err := v.sealer.Open(frame.Sealed)
	if err != nil {
		return nil, fmt.Errorf("record %s: %w", id, err)
	}
	return body, nil
}

// openPayload unframes, decrypts and parses one stored record.
func (v *Vault) openPayload(id record.ID, payload []byte) (*record.Memory, error) {
	body, err := v.openBody(id, payload)
	if err != nil {
		return nil, err
	}
	return record.Unmarshal(body)
}

// FetchMemoryFromNetwork reads a memory from Sia, bypassing this device's copy.
//
// It exists so a round trip can be checked against what was actually stored
// rather than against what was written locally a moment earlier; the two are
// only the same if the whole path works.
func (v *Vault) FetchMemoryFromNetwork(ctx context.Context, id record.ID) (*record.Memory, error) {
	payload, err := v.StoredBytes(ctx, id)
	if err != nil {
		return nil, err
	}
	return v.openPayload(id, payload)
}

// StoredBytes returns a record's bytes exactly as they sit on the network,
// still encrypted, for confirming that what left the device is unreadable.
func (v *Vault) StoredBytes(ctx context.Context, id record.ID) ([]byte, error) {
	if v.client == nil {
		return nil, errOffline
	}
	entry, err := v.manifest.Lookup(id)
	if err != nil {
		return nil, err
	}
	ref, err := sia.ParseObjectRef(entry.ObjectRef)
	if err != nil {
		return nil, err
	}
	return v.client.Download(ctx, ref)
}

// BodyFromNetwork returns a record's decrypted bytes as recovered from the
// network, so a round trip can be compared byte for byte rather than field by
// field.
func (v *Vault) BodyFromNetwork(ctx context.Context, id record.ID) ([]byte, error) {
	payload, err := v.StoredBytes(ctx, id)
	if err != nil {
		return nil, err
	}
	return v.openBody(id, payload)
}

// LocalBody returns a record's bytes as this device holds them.
func (v *Vault) LocalBody(id record.ID) ([]byte, error) { return v.local.GetBody(id) }

// ForgetLocally drops this device's copy of a record without touching the
// network, so a read can be forced down to the tiers below.
func (v *Vault) ForgetLocally(id record.ID) error { return v.local.ForgetBody(id) }

// CountBodies reports how many records this device is holding a copy of.
//
// It is the difference between a vault that has been catalogued and one that has
// been downloaded, which is a distinction a hydration has to be able to state
// rather than assert.
func (v *Vault) CountBodies() (int, error) { return v.local.CountBodies() }

// ForgetSessionHead drops this device's copy of a session head, leaving the
// transcript where it is.
//
// It is the head's counterpart to ForgetLocally, and it has no tier below it to
// fall to: a head is never packed into a slab, so the only way back from here is
// to rebuild one from the chunks. That asymmetry is the whole of what
// RebuildSessions exists to measure.
func (v *Vault) ForgetSessionHead(id record.ID) error { return v.local.ForgetSessionHead(id) }
