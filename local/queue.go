package local

import (
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/steven3002/sennit/record"
)

// A QueuedBlob is one sealed record waiting to be written to the network.
type QueuedBlob struct {
	ID      record.ID
	Kind    record.Kind
	CID     string
	Payload []byte
	// QueuedAt is when the record was sealed, which is when its durability
	// window opened.
	QueuedAt time.Time
	// Seq orders the queue by arrival, so a flush writes records in the order
	// they were remembered.
	Seq int64
}

// Enqueue adds a sealed record to the flush queue.
//
// It is written before the caller is told the record was stored, so the promise
// the user is given survives the process that made it. An in-memory queue keeps
// that promise only until the process exits, and the records it drops are
// exactly the most recent ones.
func (s *Store) Enqueue(blob QueuedBlob) error {
	if blob.QueuedAt.IsZero() {
		blob.QueuedAt = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO queue (record_id, kind, cid, payload, queued_at, seq)
		 VALUES (?, ?, ?, ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM queue))
		 ON CONFLICT(record_id) DO UPDATE SET
		     payload = excluded.payload, cid = excluded.cid, queued_at = excluded.queued_at`,
		blob.ID.String(), string(blob.Kind), blob.CID, blob.Payload, stamp(blob.QueuedAt))
	if err != nil {
		return fmt.Errorf("queue record %s: %w", blob.ID, err)
	}
	return nil
}

// A QueueState summarises what is waiting, without reading the payloads.
type QueueState struct {
	Records int
	// Bytes counts sealed, framed payloads as they will be written, which is
	// what a slab is actually filled with.
	Bytes int64
	// Oldest and Newest bound the queue's age, which is what the flush
	// deadlines are measured against.
	Oldest, Newest time.Time
}

// QueueState reports the queue's size and age.
func (s *Store) QueueState() (QueueState, error) {
	var (
		state          QueueState
		records, bytes sql.NullInt64
		oldest, newest sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT COUNT(*), SUM(LENGTH(payload)), MIN(queued_at), MAX(queued_at) FROM queue`).
		Scan(&records, &bytes, &oldest, &newest)
	if err != nil {
		return QueueState{}, fmt.Errorf("read queue state: %w", err)
	}
	state.Records, state.Bytes = int(records.Int64), bytes.Int64
	if state.Oldest, err = parseStamp(oldest); err != nil {
		return QueueState{}, err
	}
	if state.Newest, err = parseStamp(newest); err != nil {
		return QueueState{}, err
	}
	return state, nil
}

// ClaimQueued takes ownership of the queue for one flush and returns it in
// arrival order.
//
// The claim is a single statement so that two processes over the same vault
// cannot both take the same records and pay for two slabs holding the same
// thing. A claim older than staleAfter is taken over, so a process that dies
// mid-flush releases its work by expiry rather than stranding it. A staleAfter
// of zero grants no grace at all and takes over any claim, which is what a
// caller means when it says a previous holder is definitely gone.
//
// Claim times are stored at the same millisecond resolution as every other
// timestamp here, so a grace period finer than that cannot be expressed. The
// real one is minutes.
func (s *Store) ClaimQueued(owner string, staleAfter time.Duration, limit int) ([]QueuedBlob, error) {
	if limit <= 0 {
		limit = -1
	}
	now := time.Now()
	stale := `claimed_at IS NULL OR claimed_at < ?`
	args := []any{stamp(now), owner, stamp(now.Add(-staleAfter)), limit}
	if staleAfter <= 0 {
		stale = `1 = 1`
		args = []any{stamp(now), owner, limit}
	}

	rows, err := s.db.Query(
		`UPDATE queue SET claimed_at = ?, claimed_by = ?
		 WHERE record_id IN (
		     SELECT record_id FROM queue
		     WHERE `+stale+`
		     ORDER BY seq LIMIT ?)
		 RETURNING record_id, kind, cid, payload, queued_at, seq`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("claim queued records: %w", err)
	}
	defer rows.Close()

	var out []QueuedBlob
	for rows.Next() {
		var (
			blob           QueuedBlob
			id, kind, when string
		)
		if err := rows.Scan(&id, &kind, &blob.CID, &blob.Payload, &when, &blob.Seq); err != nil {
			return nil, fmt.Errorf("scan queued record: %w", err)
		}
		if blob.ID, err = record.ParseID(id); err != nil {
			return nil, err
		}
		if blob.QueuedAt, err = time.Parse(time.RFC3339, when); err != nil {
			return nil, fmt.Errorf("queued record %s has an unreadable timestamp %q: %w", id, when, err)
		}
		blob.Kind = record.Kind(kind)
		out = append(out, blob)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read claimed records: %w", err)
	}
	// RETURNING does not promise an order, and the order is the point: records
	// must reach a slab in the order they were remembered.
	slices.SortFunc(out, func(a, b QueuedBlob) int { return int(a.Seq - b.Seq) })
	return out, nil
}

// ReleaseQueued returns claimed records to the queue after a flush failed.
func (s *Store) ReleaseQueued(ids []record.ID) error {
	return s.eachQueued(ids, `UPDATE queue SET claimed_at = NULL, claimed_by = NULL WHERE record_id = ?`, "release")
}

// DropQueued removes records from the queue once they are on the network and
// catalogued.
//
// It runs last on purpose. A record dropped before its location is recorded is
// gone; a record dropped after is at worst written twice, which costs a slab
// and loses nothing.
func (s *Store) DropQueued(ids []record.ID) error {
	return s.eachQueued(ids, `DELETE FROM queue WHERE record_id = ?`, "drop")
}

func (s *Store) eachQueued(ids []record.ID, stmt, verb string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("%s %d queued record(s): %w", verb, len(ids), err)
	}
	defer tx.Rollback()

	prepared, err := tx.Prepare(stmt)
	if err != nil {
		return fmt.Errorf("%s %d queued record(s): %w", verb, len(ids), err)
	}
	defer prepared.Close()

	for _, id := range ids {
		if _, err := prepared.Exec(id.String()); err != nil {
			return fmt.Errorf("%s queued record %s: %w", verb, id, err)
		}
	}
	return tx.Commit()
}

// stamp renders a time in the one layout this package stores, which is
// fixed-width UTC so that SQL's lexicographic comparison is chronological.
func stamp(t time.Time) string { return record.At(t).String() }

func parseStamp(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("unreadable queue timestamp %q: %w", value.String, err)
	}
	return parsed, nil
}
