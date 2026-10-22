package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/steven3002/sennit/record"
)

// A ListingRewrite is what dating every listing row by the record it lists did
// on this device. It is kept as the value of the setting that marks the rewrite
// done, so what it left alone is still on record after the process that made it
// has gone.
type ListingRewrite struct {
	// Ran reports that this call made the rewrite. It is false when the rewrite
	// had already been made on this device, by this process or by another.
	Ran bool `json:"-"`
	// At is when the rewrite was made.
	At record.Time `json:"at"`
	// Rows is how many listing rows it read, and Rewritten how many of those it
	// gave a time other than the one they had.
	Rows      int `json:"rows"`
	Rewritten int `json:"rewritten"`
	// NotHeld counts the rows it left as they were because this device holds no
	// copy of the record to read a time from: no body for a memory, no head for
	// a conversation.
	NotHeld int `json:"notHeld"`
	// Unreadable counts the rows it left as they were because the copy this
	// device holds could not be read.
	Unreadable int `json:"unreadable"`
	// Undated counts the conversation heads it stored without their dates.
	Undated int `json:"undated"`
}

// A ListingDater answers what the rewrite asks of the records it reads.
type ListingDater interface {
	// TimeOf is the time a record carries, read from this device's copy of
	// it: a memory's body or a conversation's head.
	TimeOf(kind record.Kind, held []byte) (record.Time, error)
	// Undated says whether a conversation's head is to lose its dates, and
	// when it is, gives the head to store in its place, one revision on from
	// it as every change to a head is. A nil session leaves the head as it is.
	Undated(head []byte) (*record.Session, []byte, error)
}

// RewriteListingDates dates every listing row by the time the record it lists
// carries, once on this device, and records under the setting marker that it
// did. Before it dates them, it stores without their dates the conversation
// heads dater picks out, so each of those conversations is dated by the head it
// is left with.
//
// A row keeps the time first written for it, and a build before records were
// listed by their own times wrote this device's clock there: on a device that
// restored the vault, the moment the restore reached the record. Nothing a later
// write does moves that time, so such a device would go on listing what it
// restored then in the order the restore walked the account, among records dated
// by their own times since. This is the one correction, made from this device's
// own copy of each record, a memory's body or a conversation's head.
//
// Once the marker is set, a call costs one read. The heads, the rows and the
// marker are written in one transaction, which takes the write lock as it
// begins and reads the marker again under that lock. A second process making
// the same call at the same moment, as two processes opening one vault do,
// therefore waits for the first and then finds the rewrite made. A rewrite that
// stops part way leaves every head, every row and the marker as they were, so
// the next call makes the whole of it again. No other connection can write
// while the lock is held, so whatever dater reads through the store's other
// connections is what the transaction sees.
//
// A row whose record this device does not hold, or whose copy cannot be read, is
// left as it is and counted, and is not looked at again.
func (s *Store) RewriteListingDates(marker string, dater ListingDater) (ListingRewrite, error) {
	if _, err := s.GetMeta(marker); err == nil {
		return ListingRewrite{}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ListingRewrite{}, err
	}

	var made ListingRewrite
	err := s.writing(func(ctx context.Context, conn *sql.Conn) error {
		var value string
		err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, marker).Scan(&value)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read meta %q: %w", marker, err)
		}

		undated, err := undateHeads(ctx, conn, dater)
		if err != nil {
			return err
		}
		rewrite, err := rewriteListingDates(ctx, conn, dater.TimeOf)
		if err != nil {
			return err
		}
		rewrite.Undated = undated
		rewrite.At = record.Now()
		encoded, err := json.Marshal(rewrite)
		if err != nil {
			return fmt.Errorf("record the listing rewrite: %w", err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES (?, ?)`, marker, string(encoded)); err != nil {
			return fmt.Errorf("store meta %q: %w", marker, err)
		}
		rewrite.Ran = true
		made = rewrite
		return nil
	})
	if err != nil {
		return ListingRewrite{}, err
	}
	return made, nil
}

// undateHeads stores without their dates the conversation heads dater picks
// out, inside the transaction conn holds, and counts them.
func undateHeads(ctx context.Context, conn *sql.Conn, dater ListingDater) (int, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version, head FROM session_heads`)
	if err != nil {
		return 0, fmt.Errorf("read the session heads: %w", err)
	}
	defer rows.Close()

	type replacement struct {
		session *record.Session
		head    []byte
		expect  int64
	}
	var chosen []replacement
	for rows.Next() {
		var version int64
		var head []byte
		if err := rows.Scan(&version, &head); err != nil {
			return 0, fmt.Errorf("read the session heads: %w", err)
		}
		session, replaced, err := dater.Undated(head)
		if err != nil {
			return 0, err
		}
		if session != nil {
			chosen = append(chosen, replacement{session: session, head: replaced, expect: version})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read the session heads: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("read the session heads: %w", err)
	}
	for _, r := range chosen {
		if err := replaceHead(ctx, conn, r.session, r.head, r.expect); err != nil {
			return 0, err
		}
	}
	return len(chosen), nil
}

// replaceHead stores a head in place of the one this device holds at version
// expect, inside the transaction conn holds.
//
// It writes the columns PutSessionHead writes when it replaces a head, and
// created_at with them, because every column is a copy of the head and this
// head's dates are the change. PutSessionHead itself writes through the pool,
// where it would wait for the lock this transaction holds.
func replaceHead(ctx context.Context, conn *sql.Conn, session *record.Session, head []byte, expect int64) error {
	var parent any
	if session.Lineage.ParentSession != nil {
		parent = session.Lineage.ParentSession.String()
	}
	result, err := conn.ExecContext(ctx,
		`UPDATE session_heads SET
		   version = ?, title = ?, summary = ?, kind = ?, project = ?, agent = ?, parent = ?,
		   created_at = ?, updated_at = ?, archived = ?, messages = ?, chunks = ?, bytes = ?, head = ?
		 WHERE session_id = ? AND version = ?`,
		session.Version, session.Title, session.Summary,
		string(session.Kind), ProjectKey(session.Project), session.Agent.Name, parent,
		session.Created.String(), session.Updated.String(), session.Archived,
		session.Counts.Messages, len(session.Chunks), session.Counts.Bytes, head,
		session.ID.String(), expect)
	if err != nil {
		return fmt.Errorf("store session head %s: %w", session.ID, err)
	}
	written, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store session head %s: %w", session.ID, err)
	}
	// The head was read under this transaction's lock, so nothing can have
	// moved it on. Writing nothing would still record it as stored, so it is
	// refused instead.
	if written != 1 {
		return fmt.Errorf("session %s: %w (expected version %d)", session.ID, ErrStaleHead, expect)
	}
	return nil
}

// rewriteListingDates dates each listing row by its record, inside the
// transaction conn holds, and counts what it did.
func rewriteListingDates(ctx context.Context, conn *sql.Conn, dateOf func(record.Kind, []byte) (record.Time, error)) (ListingRewrite, error) {
	memory, session := string(record.KindMemory), string(record.KindSession)
	// A listing row is written for a memory or a conversation and for nothing
	// else. A memory's copy is its body, joined with the kind it was stored
	// under, because every record is JSON and a tolerant decoder reads one kind
	// as another without complaint. A conversation's copy is its head.
	rows, err := conn.QueryContext(ctx,
		`SELECT m.record_id, m.kind, m.created_at,
		        b.record_id IS NOT NULL OR h.session_id IS NOT NULL,
		        COALESCE(b.body, h.head)
		 FROM record_meta m
		 LEFT JOIN bodies b ON m.kind = ? AND b.record_id = m.record_id AND b.kind = ?
		 LEFT JOIN session_heads h ON m.kind = ? AND h.session_id = m.record_id
		 WHERE m.kind IN (?, ?)`,
		memory, memory, session, memory, session)
	if err != nil {
		return ListingRewrite{}, fmt.Errorf("read the listing: %w", err)
	}
	defer rows.Close()

	// The new times are collected and written once the read is done, rather
	// than while it is still stepping through the table it would be writing.
	type dated struct{ id, created string }
	var rewrite ListingRewrite
	var changed []dated
	for rows.Next() {
		var (
			id, kind, created string
			holds             bool
			held              []byte
		)
		if err := rows.Scan(&id, &kind, &created, &holds, &held); err != nil {
			return ListingRewrite{}, fmt.Errorf("read the listing: %w", err)
		}
		rewrite.Rows++
		if !holds {
			rewrite.NotHeld++
			continue
		}
		at, err := dateOf(record.Kind(kind), held)
		if err != nil {
			// One record that will not read costs its own date and not the
			// listing's: refusing here would leave every row as it was, and
			// every open trying again.
			rewrite.Unreadable++
			continue
		}
		if at.String() != created {
			changed = append(changed, dated{id: id, created: at.String()})
		}
	}
	if err := rows.Err(); err != nil {
		return ListingRewrite{}, fmt.Errorf("read the listing: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ListingRewrite{}, fmt.Errorf("read the listing: %w", err)
	}

	update, err := conn.PrepareContext(ctx, `UPDATE record_meta SET created_at = ? WHERE record_id = ?`)
	if err != nil {
		return ListingRewrite{}, fmt.Errorf("date the listing: %w", err)
	}
	defer update.Close()
	for _, row := range changed {
		if _, err := update.ExecContext(ctx, row.created, row.id); err != nil {
			return ListingRewrite{}, fmt.Errorf("date listing row %s: %w", row.id, err)
		}
	}
	rewrite.Rewritten = len(changed)
	return rewrite, nil
}
