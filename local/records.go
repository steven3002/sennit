package local

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/steven3002/sennit/record"
)

// A RecordQuery lists records by their metadata rather than by meaning.
//
// Every filter here is an ordinary one, it excludes. That is the opposite of
// what ranking's filters do, and the difference is not an inconsistency: a
// ranked answer must never come back empty because an agent guessed a tag
// wrongly from the wording of a question, whereas a caller asking to see the
// records carrying a tag is naming what it wants rather than guessing at it.
type RecordQuery struct {
	// Kinds selects record classes. Empty means every class.
	Kinds []record.Kind
	// Types selects memory types. Empty means every type.
	Types []record.Type
	// Tags requires every listed tag.
	Tags []string
	// IncludeSuperseded lists replaced versions alongside current ones.
	IncludeSuperseded bool
	// Limit caps the page, and Cursor continues a previous one.
	Limit  int
	Cursor Cursor
}

// A RecordRow is one record as a listing sees it.
type RecordRow struct {
	ID      record.ID
	Kind    record.Kind
	Type    record.Type
	Tags    []string
	Created record.Time
	// Superseded reports that a later record replaced this one.
	Superseded bool
}

// DefaultRecordLimit is how many records a listing returns when the caller does
// not say.
const DefaultRecordLimit = 20

// MaxRecordLimit bounds a page, so a caller asking for everything gets a page
// and a cursor rather than a vault.
const MaxRecordLimit = 200

// A Cursor is an opaque position in a listing.
//
// It is opaque on purpose. The alternative, an offset, is not stable across
// writes: a record added between two pages shifts every row after it, so a
// caller paging through a vault that is being written to sees an entry twice or
// not at all. A keyset cursor names the last row seen, so the next page starts
// after it whatever else has changed.
type Cursor string

// cursorSeparator cannot appear in either half: a timestamp is fixed-format and
// a record id is hex.
const cursorSeparator = "\x00"

func encodeCursor(created record.Time, id record.ID) Cursor {
	return Cursor(base64.RawURLEncoding.EncodeToString(
		[]byte(created.String() + cursorSeparator + id.String())))
}

// decode splits a cursor back into the row it names.
func (c Cursor) decode() (string, string, error) {
	if c == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil {
		return "", "", fmt.Errorf("cursor is not one this vault issued: %w", err)
	}
	created, id, ok := strings.Cut(string(raw), cursorSeparator)
	if !ok {
		return "", "", fmt.Errorf("cursor is not one this vault issued")
	}
	if _, err := record.ParseID(id); err != nil {
		return "", "", fmt.Errorf("cursor is not one this vault issued: %w", err)
	}
	return created, id, nil
}

// ListRecords returns records newest first, with a cursor when more remain.
//
// It reads the ranking metadata and not the bodies. A listing that opened every
// record would cost the size of the vault to render a page of it, and the
// metadata is already held apart precisely because ranking has to read it before
// it fetches anything.
func (s *Store) ListRecords(query RecordQuery) ([]RecordRow, Cursor, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultRecordLimit
	}
	if limit > MaxRecordLimit {
		limit = MaxRecordLimit
	}

	where := []string{"1 = 1"}
	var args []any

	if len(query.Kinds) > 0 {
		where = append(where, "m.kind IN ("+placeholders(len(query.Kinds))+")")
		for _, kind := range query.Kinds {
			args = append(args, string(kind))
		}
	}
	if len(query.Types) > 0 {
		where = append(where, "m.type IN ("+placeholders(len(query.Types))+")")
		for _, recordType := range query.Types {
			args = append(args, string(recordType))
		}
	}
	for _, tag := range NormalizeTags(query.Tags) {
		where = append(where,
			"EXISTS (SELECT 1 FROM record_tags t WHERE t.record_id = m.record_id AND t.tag = ?)")
		args = append(args, tag)
	}
	// A superseded record is held back the way recall holds it back, and by the
	// same rule: the vault's current state is the default answer and its history
	// is reachable on request.
	if !query.IncludeSuperseded {
		where = append(where,
			"NOT EXISTS (SELECT 1 FROM record_meta r WHERE r.supersedes = m.record_id)")
	}

	created, id, err := query.Cursor.decode()
	if err != nil {
		return nil, "", err
	}
	if created != "" {
		// Strictly after the row the cursor names, in the listing's own order,
		// so a page boundary never repeats a row or skips one.
		where = append(where, "(m.created_at < ? OR (m.created_at = ? AND m.record_id > ?))")
		args = append(args, created, created, id)
	}

	// One more than the page, so "are there more" is answered by the query
	// rather than by a second count that could disagree with it.
	args = append(args, limit+1)
	rows, err := s.db.Query(
		`SELECT m.record_id, m.kind, m.type, m.created_at,
		        EXISTS (SELECT 1 FROM record_meta r WHERE r.supersedes = m.record_id)
		 FROM record_meta m
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY m.created_at DESC, m.record_id ASC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list records: %w", err)
	}
	defer rows.Close()

	out := make([]RecordRow, 0, limit)
	var more bool
	for rows.Next() {
		if len(out) == limit {
			more = true
			break
		}
		var row RecordRow
		var idHex, kind, recordType, createdAt string
		if err := rows.Scan(&idHex, &kind, &recordType, &createdAt, &row.Superseded); err != nil {
			return nil, "", fmt.Errorf("scan record: %w", err)
		}
		parsed, err := record.ParseID(idHex)
		if err != nil {
			return nil, "", err
		}
		if err := row.Created.UnmarshalJSON([]byte(`"` + createdAt + `"`)); err != nil {
			return nil, "", fmt.Errorf("record %s created: %w", idHex, err)
		}
		row.ID, row.Kind, row.Type = parsed, record.Kind(kind), record.Type(recordType)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if err := s.attachTags(out); err != nil {
		return nil, "", err
	}
	if !more || len(out) == 0 {
		return out, "", nil
	}
	last := out[len(out)-1]
	return out, encodeCursor(last.Created, last.ID), nil
}

// attachTags fills the tags of a page in one query rather than one per row.
func (s *Store) attachTags(page []RecordRow) error {
	if len(page) == 0 {
		return nil
	}
	args := make([]any, len(page))
	at := make(map[record.ID]int, len(page))
	for i, row := range page {
		args[i] = row.ID.String()
		at[row.ID] = i
	}
	rows, err := s.db.Query(
		`SELECT record_id, tag FROM record_tags
		 WHERE record_id IN (`+placeholders(len(page))+`) ORDER BY tag`, args...)
	if err != nil {
		return fmt.Errorf("list record tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var idHex, tag string
		if err := rows.Scan(&idHex, &tag); err != nil {
			return fmt.Errorf("scan record tag: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return err
		}
		if i, ok := at[id]; ok {
			page[i].Tags = append(page[i].Tags, tag)
		}
	}
	return rows.Err()
}

// CountRecordsByKind reports how many records of each class the device holds
// metadata for.
//
// It counts the metadata and not the catalog, because the catalog names what has
// reached the network: a record queued for the next flush is held on this device
// exactly as much as one already on Sia, and a count that said otherwise would
// report a vault as smaller than it is for up to an hour after every write.
func (s *Store) CountRecordsByKind() (map[record.Kind]int, error) {
	rows, err := s.db.Query(`SELECT kind, COUNT(*) FROM record_meta GROUP BY kind`)
	if err != nil {
		return nil, fmt.Errorf("count records by kind: %w", err)
	}
	defer rows.Close()

	out := map[record.Kind]int{}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			return nil, fmt.Errorf("scan record count: %w", err)
		}
		out[record.Kind(kind)] = count
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
