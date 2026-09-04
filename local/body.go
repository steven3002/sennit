package local

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/steven3002/sennit/record"
)

// ErrNotFound reports that the device has no copy of a record.
var ErrNotFound = errors.New("not held locally")

// PutBody stores a record body on the device.
//
// This is the step that makes a write durable for the user. Reaching Sia is a
// separate, later event, and the difference between the two is a window the
// interface must show rather than hide.
func (s *Store) PutBody(id record.ID, kind record.Kind, body []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO bodies (record_id, kind, body, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(record_id) DO UPDATE SET body = excluded.body`,
		id.String(), string(kind), body, record.Now().String())
	if err != nil {
		return fmt.Errorf("store body %s: %w", id, err)
	}
	return nil
}

// GetBody reads a record body from the device.
func (s *Store) GetBody(id record.ID) ([]byte, error) {
	var body []byte
	err := s.db.QueryRow(`SELECT body FROM bodies WHERE record_id = ?`, id.String()).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("body %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read body %s: %w", id, err)
	}
	return body, nil
}

// GetRecord reads a record body together with the kind that says how to parse
// it.
//
// The kind is stored beside the body rather than inferred from it, because the
// three record classes are all JSON and a tolerant decoder will happily read one
// as another and produce a well-formed record of the wrong shape.
func (s *Store) GetRecord(id record.ID) (record.Kind, []byte, error) {
	var kind string
	var body []byte
	err := s.db.QueryRow(`SELECT kind, body FROM bodies WHERE record_id = ?`, id.String()).Scan(&kind, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("body %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", nil, fmt.Errorf("read body %s: %w", id, err)
	}
	return record.Kind(kind), body, nil
}

// ForgetBody drops this device's copy of a record.
//
// The record itself is untouched: it is on the network, the catalog still
// points at it, and the next read fetches it back. It exists so the tiers below
// the device's own copy can be reached at all, which is otherwise impossible on
// the machine that wrote the record.
func (s *Store) ForgetBody(id record.ID) error {
	if _, err := s.db.Exec(`DELETE FROM bodies WHERE record_id = ?`, id.String()); err != nil {
		return fmt.Errorf("forget body %s: %w", id, err)
	}
	return nil
}

// BodyIDsOfKind lists the records of one class that this device holds.
//
// It reads the kind column beside the body rather than any of the metadata
// tables, which is what makes it usable for the one record class that has no
// metadata: a transcript chunk is never ranked and never listed, so nothing else
// in the store knows the device has one.
func (s *Store) BodyIDsOfKind(kind record.Kind) ([]record.ID, error) {
	rows, err := s.db.Query(
		`SELECT record_id FROM bodies WHERE kind = ? ORDER BY created_at ASC, record_id ASC`,
		string(kind))
	if err != nil {
		return nil, fmt.Errorf("list %s bodies: %w", kind, err)
	}
	defer rows.Close()

	var out []record.ID
	for rows.Next() {
		var idHex string
		if err := rows.Scan(&idHex); err != nil {
			return nil, fmt.Errorf("scan %s body: %w", kind, err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountBodies reports how many records the device holds.
func (s *Store) CountBodies() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM bodies`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count bodies: %w", err)
	}
	return n, nil
}
