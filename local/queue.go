package local

import (
	"context"
	"database/sql"
	"errors"
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

// ErrClaimed reports that a queued record is held by a flush in progress, which
// has its payload in hand and is writing it to the network.
var ErrClaimed = errors.New("a flush in progress has claimed it and is writing it to the network")

// WithdrawQueued takes a record back out of the queue before any flush writes
// it, and reports whether it was queued at all.
//
// A record a flush has claimed is left where it is, and ErrClaimed says why.
// That flush read the payload when it made the claim and catalogues the record
// once it lands, so deleting the row now would stop neither. A claim older than
// staleAfter is one ClaimQueued would take over, left by a flush that is no
// longer running, and it holds nothing back; a staleAfter of zero treats every
// claim that way, as it does there.
//
// The claim is checked by the same statement that deletes the row, so no flush
// can claim the record in between.
func (s *Store) WithdrawQueued(id record.ID, staleAfter time.Duration) (bool, error) {
	unclaimed := `claimed_at IS NULL OR claimed_at < ?`
	args := []any{id.String(), stamp(time.Now().Add(-staleAfter))}
	if staleAfter <= 0 {
		unclaimed = `1 = 1`
		args = args[:1]
	}
	result, err := s.db.Exec(`DELETE FROM queue WHERE record_id = ? AND (`+unclaimed+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("withdraw queued record %s: %w", id, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("withdraw queued record %s: %w", id, err)
	}
	if deleted > 0 {
		return true, nil
	}

	// Nothing was deleted, so the record is either not queued or claimed.
	var claimed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM queue WHERE record_id = ?`, id.String()).Scan(&claimed); err != nil {
		return false, fmt.Errorf("withdraw queued record %s: %w", id, err)
	}
	if claimed > 0 {
		return false, fmt.Errorf("withdraw queued record %s: %w", id, ErrClaimed)
	}
	return false, nil
}

// WithdrawQueuedAll takes several records back out of the queue together, or
// none of them, and reports which of them were queued.
//
// It is WithdrawQueued for a record that is written as several, such as a
// conversation's transcript chunks, which a flush can claim some of and not
// others. Taking the free ones out and then meeting a claim on the next would
// leave that one to the flush and the ones taken out never to reach the
// network: the record neither withdrawn nor whole. So a claim on any of them
// refuses all of them with ErrClaimed, and nothing leaves the queue. The claims
// are read and the rows deleted in one write transaction, so no flush can claim
// one in between, or land one part way through.
//
// A claim older than staleAfter holds nothing back, and a staleAfter of zero
// treats every claim that way, as in WithdrawQueued. An id that is not queued
// at all is passed over.
func (s *Store) WithdrawQueuedAll(ids []record.ID, staleAfter time.Duration) ([]record.ID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var withdrawn []record.ID
	err := s.writing(func(ctx context.Context, conn *sql.Conn) error {
		if staleAfter > 0 {
			held, err := conn.PrepareContext(ctx,
				`SELECT COUNT(*) FROM queue WHERE record_id = ? AND claimed_at IS NOT NULL AND claimed_at >= ?`)
			if err != nil {
				return err
			}
			defer held.Close()
			liveSince := stamp(time.Now().Add(-staleAfter))
			for _, id := range ids {
				var claimed int
				if err := held.QueryRowContext(ctx, id.String(), liveSince).Scan(&claimed); err != nil {
					return fmt.Errorf("record %s: %w", id, err)
				}
				if claimed > 0 {
					return fmt.Errorf("record %s: %w", id, ErrClaimed)
				}
			}
		}

		remove, err := conn.PrepareContext(ctx, `DELETE FROM queue WHERE record_id = ?`)
		if err != nil {
			return err
		}
		defer remove.Close()
		for _, id := range ids {
			result, err := remove.ExecContext(ctx, id.String())
			if err != nil {
				return fmt.Errorf("record %s: %w", id, err)
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("record %s: %w", id, err)
			}
			if deleted > 0 {
				withdrawn = append(withdrawn, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("withdraw %d queued record(s): %w", len(ids), err)
	}
	return withdrawn, nil
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
