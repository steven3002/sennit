package local

import (
	"fmt"
	"strings"

	"github.com/steven3002/sennit/record"
)

// Described is the ranking metadata for a set of candidates, plus which of them
// a later record replaces.
type Described struct {
	Meta       map[record.ID]RankingMeta
	Superseded map[record.ID]bool
}

// Describe reads ranking metadata for a candidate pool in one pass.
//
// Ranking calls this once per recall with the whole pool, rather than caching an
// answer when the process starts. Any process sharing this vault may supersede a
// record at any moment, and a cached answer would go on returning the replaced
// version until something reopened the database.
func (s *Store) Describe(ids []record.ID) (Described, error) {
	out := Described{
		Meta:       make(map[record.ID]RankingMeta, len(ids)),
		Superseded: make(map[record.ID]bool),
	}
	if len(ids) == 0 {
		return out, nil
	}

	keys := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		keys[i] = id.String()
		args[i] = keys[i]
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")

	rows, err := s.db.Query(
		`SELECT record_id, kind, type FROM record_meta WHERE record_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return Described{}, fmt.Errorf("describe candidates: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var idHex, kind, recordType string
		if err := rows.Scan(&idHex, &kind, &recordType); err != nil {
			return Described{}, fmt.Errorf("scan candidate: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return Described{}, err
		}
		out.Meta[id] = RankingMeta{ID: id, Kind: record.Kind(kind), Type: record.Type(recordType)}
	}
	if err := rows.Err(); err != nil {
		return Described{}, err
	}

	tagRows, err := s.db.Query(
		`SELECT record_id, tag FROM record_tags WHERE record_id IN (`+placeholders+`) ORDER BY tag`, args...)
	if err != nil {
		return Described{}, fmt.Errorf("describe candidate tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var idHex, tag string
		if err := tagRows.Scan(&idHex, &tag); err != nil {
			return Described{}, fmt.Errorf("scan candidate tag: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return Described{}, err
		}
		meta, ok := out.Meta[id]
		if !ok {
			continue
		}
		meta.Tags = append(meta.Tags, tag)
		out.Meta[id] = meta
	}
	if err := tagRows.Err(); err != nil {
		return Described{}, err
	}

	// A record is superseded when some other record names it. The edge is
	// stored on the replacement, so this asks which candidates are pointed at
	// rather than reading a flag on the candidate itself, which is what keeps
	// the vault append-only: marking the old record would mean rewriting a body
	// that is already immutable and already on the network.
	supRows, err := s.db.Query(
		`SELECT DISTINCT supersedes FROM record_meta WHERE supersedes IN (`+placeholders+`)`, args...)
	if err != nil {
		return Described{}, fmt.Errorf("describe supersession: %w", err)
	}
	defer supRows.Close()
	for supRows.Next() {
		var idHex string
		if err := supRows.Scan(&idHex); err != nil {
			return Described{}, fmt.Errorf("scan supersession: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return Described{}, err
		}
		out.Superseded[id] = true
	}
	return out, supRows.Err()
}
