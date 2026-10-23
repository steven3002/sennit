package local

import (
	"fmt"

	"github.com/steven3002/sennit/record"
)

// Held is which records this device holds, by what it keeps of each: the ranking
// rows that list and rank a record, the body of a memory, and the head of a
// conversation.
//
// It is read when the search index has to be rebuilt without trusting the index
// to say what belongs in it. A memory's body and a conversation's head are
// written before the record's vector on every path that writes one, and a forget
// takes the vector before it lets go of the last of what it removes, so between
// them these say which vectors still have a record behind them, and which
// records are here whole and still lack a vector.
type Held struct {
	// MemoryRows and SessionRows are the records with ranking rows, by class.
	MemoryRows, SessionRows map[record.ID]bool
	// MemoryBodies are the memories whose body is on this device.
	MemoryBodies map[record.ID]bool
	// Heads are the conversations whose head is on this device.
	Heads map[record.ID]bool
}

// HeldRecords reads which records this device holds.
func (s *Store) HeldRecords() (Held, error) {
	held := Held{
		MemoryRows:   make(map[record.ID]bool),
		SessionRows:  make(map[record.ID]bool),
		MemoryBodies: make(map[record.ID]bool),
		Heads:        make(map[record.ID]bool),
	}
	rows, err := s.db.Query(`SELECT record_id, kind FROM record_meta`)
	if err != nil {
		return Held{}, fmt.Errorf("list ranked records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var idHex, kind string
		if err := rows.Scan(&idHex, &kind); err != nil {
			return Held{}, fmt.Errorf("scan ranked record: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return Held{}, err
		}
		switch record.Kind(kind) {
		case record.KindMemory:
			held.MemoryRows[id] = true
		case record.KindSession:
			held.SessionRows[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return Held{}, err
	}

	bodies, err := s.BodyIDsOfKind(record.KindMemory)
	if err != nil {
		return Held{}, err
	}
	for _, id := range bodies {
		held.MemoryBodies[id] = true
	}

	heads, err := s.db.Query(`SELECT session_id FROM session_heads`)
	if err != nil {
		return Held{}, fmt.Errorf("list session heads: %w", err)
	}
	defer heads.Close()
	for heads.Next() {
		var idHex string
		if err := heads.Scan(&idHex); err != nil {
			return Held{}, fmt.Errorf("scan session head: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return Held{}, err
		}
		held.Heads[id] = true
	}
	return held, heads.Err()
}
