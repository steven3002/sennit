package manifest

import "github.com/steven3002/sennit/record"

// Removed reports whether the catalog marks a record as no longer held, and
// returns the entry that says so.
//
// A restore asks it of every record it finds on the network. A removed record's
// object stays there until something deletes it, and a restore that catalogued
// every record it found appended an entry over the removal, which put back the
// record that had been removed. The removal is kept through every compaction,
// so this device always knows what it has let go of.
func (m *Manifest) Removed(id record.ID) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.entries[id]
	if !ok || !entry.Deleted {
		return Entry{}, false
	}
	return entry, true
}

// RemovedOnNetwork lists the removed records whose object this device has not
// deleted from the network, in the order they were first written.
//
// A removal keeps the record's object ref, and ObjectDeleted clears it once the
// object is gone, so an entry that still names an object names one a restore on
// any device can still find. A catalog written before objects were deleted this
// way lists every record it ever removed. Deleting an object that is already
// gone costs one call to the network and changes nothing there.
func (m *Manifest) RemovedOnNetwork() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Entry
	for _, id := range m.order {
		if entry := m.entries[id]; entry.Deleted && entry.ObjectRef != "" {
			out = append(out, entry)
		}
	}
	return out
}

// ObjectDeleted records that a removed record's object has been deleted from
// the network, so the entry stops naming it.
//
// It changes nothing unless the entry is still a removal and still names ref.
// The deletion runs over the network without the catalog's lock, so the entry
// is read again here rather than trusted from before: one this process cleared
// in the meantime, by deleting the same object a second time, is left alone.
func (m *Manifest) ObjectDeleted(id record.ID, ref string) error {
	if err := m.objectDeleted(id, ref); err != nil {
		return err
	}
	if !m.DueForCompaction() {
		return nil
	}
	return m.Compact()
}

func (m *Manifest) objectDeleted(id record.ID, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.entries[id]
	if !ok || !entry.Deleted || ref == "" || entry.ObjectRef != ref {
		return nil
	}
	entry.ObjectRef, entry.SlabID = "", ""
	entry.WrittenAt = record.Now()
	if err := m.log.Append(entry); err != nil {
		return err
	}
	m.entries[id] = entry
	return nil
}
