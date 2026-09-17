package local

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steven3002/sennit/record"
)

// RankingMeta is everything ranking needs to know about a record before it has
// fetched it: what it is, what it is about, and what it replaced.
type RankingMeta struct {
	ID record.ID
	// Kind is which class of record this is. It is what a caller selects a
	// scope with, as opposed to Type and Tags, which are what a caller
	// expresses a preference with.
	Kind record.Kind
	Type record.Type
	Tags []string
	// Supersedes is the record this one replaces, if any. The reverse edge is
	// derived rather than stored: a record is superseded when some other record
	// names it here. That keeps the vault append-only, since marking the older
	// record would mean rewriting an immutable body.
	Supersedes *record.ID
	// Text is what the lexical pass indexes. An empty value leaves the record
	// reachable by meaning but not by its words, which is a degraded ranking and
	// never a missing record.
	Text string
}

// execer is the part of a transaction the metadata writers use, so the helpers
// that write to more than one table can be called from a single transaction
// instead of opening their own.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// NormalizeTag puts a tag in the one form the vault compares.
//
// Tags are matched exactly, so "Sia", "sia " and "sia" have to become one thing
// somewhere. Doing it at the boundary means a record and a filter that mean the
// same tag always agree, whichever of them was written first.
func NormalizeTag(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }

// NormalizeTags cleans a tag list and drops blanks and duplicates.
func NormalizeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		clean := NormalizeTag(tag)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

// PutRankingMeta records what ranking needs about one memory.
func (s *Store) PutRankingMeta(meta RankingMeta) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("record metadata %s: %w", meta.ID, err)
	}
	defer tx.Rollback()

	var supersedes any
	if meta.Supersedes != nil {
		supersedes = meta.Supersedes.String()
	}
	kind := meta.Kind
	if kind == "" {
		kind = record.KindMemory
	}
	if _, err := tx.Exec(
		`INSERT INTO record_meta (record_id, kind, type, supersedes, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(record_id) DO UPDATE SET kind = excluded.kind, type = excluded.type,
		   supersedes = excluded.supersedes`,
		meta.ID.String(), string(kind), string(meta.Type), supersedes, record.Now().String()); err != nil {
		return fmt.Errorf("record metadata %s: %w", meta.ID, err)
	}
	if _, err := tx.Exec(`DELETE FROM record_tags WHERE record_id = ?`, meta.ID.String()); err != nil {
		return fmt.Errorf("record tags %s: %w", meta.ID, err)
	}
	for _, tag := range NormalizeTags(meta.Tags) {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO record_tags (record_id, tag) VALUES (?, ?)`,
			meta.ID.String(), tag); err != nil {
			return fmt.Errorf("record tag %q on %s: %w", tag, meta.ID, err)
		}
	}
	// In the same transaction as the rest, so no reader ever sees a record that
	// one half of ranking knows about and the other does not.
	if err := putLexical(tx, meta.ID, meta.Text); err != nil {
		return err
	}
	return tx.Commit()
}

// ForgetRankingMeta drops a record from the ranking metadata, so a deleted
// record stops being scored and stops counting toward tag frequencies.
func (s *Store) ForgetRankingMeta(id record.ID) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("forget metadata %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM record_tags WHERE record_id = ?`, id.String()); err != nil {
		return fmt.Errorf("forget tags %s: %w", id, err)
	}
	if _, err := tx.Exec(`DELETE FROM record_meta WHERE record_id = ?`, id.String()); err != nil {
		return fmt.Errorf("forget metadata %s: %w", id, err)
	}
	if err := forgetLexical(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RankingMetaAll reads every record's ranking metadata, for hydrating the
// in-memory view a search consults.
func (s *Store) RankingMetaAll() ([]RankingMeta, error) {
	rows, err := s.db.Query(`SELECT record_id, kind, type, supersedes FROM record_meta`)
	if err != nil {
		return nil, fmt.Errorf("read ranking metadata: %w", err)
	}
	defer rows.Close()

	byID := make(map[record.ID]*RankingMeta)
	var out []RankingMeta
	for rows.Next() {
		var idHex, kind, recordType string
		var supersedes sql.NullString
		if err := rows.Scan(&idHex, &kind, &recordType, &supersedes); err != nil {
			return nil, fmt.Errorf("scan ranking metadata: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return nil, err
		}
		meta := RankingMeta{ID: id, Kind: record.Kind(kind), Type: record.Type(recordType)}
		if supersedes.Valid && supersedes.String != "" {
			replaced, err := record.ParseID(supersedes.String)
			if err != nil {
				return nil, fmt.Errorf("ranking metadata %s: %w", idHex, err)
			}
			meta.Supersedes = &replaced
		}
		out = append(out, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}

	tagRows, err := s.db.Query(`SELECT record_id, tag FROM record_tags ORDER BY record_id, tag`)
	if err != nil {
		return nil, fmt.Errorf("read record tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var idHex, tag string
		if err := tagRows.Scan(&idHex, &tag); err != nil {
			return nil, fmt.Errorf("scan record tag: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return nil, err
		}
		if meta, ok := byID[id]; ok {
			meta.Tags = append(meta.Tags, tag)
		}
	}
	return out, tagRows.Err()
}

// A TagCount is how many records carry one tag.
type TagCount struct {
	Tag     string
	Records int
}

// TagFrequencies reports how many records other than one carry each of the
// given tags, and how many records other than that one the vault holds metadata
// for.
//
// This is what makes a tag's specificity visible at write time. A tag that
// already sits on most of the vault cannot separate anything, and the write path
// is the only place where that is still worth knowing.
//
// The exclusion is what keeps the answer about the vault a record is joining
// rather than about the vault with that record already in it. The write path
// stores a record's metadata before it asks for advice, so without it every tag
// a write supplies is counted at least once, on the record supplying it, and a
// tag used for the first time can never be reported as new. A zero id matches
// no record, and so excludes nothing.
func (s *Store) TagFrequencies(exclude record.ID, tags []string) ([]TagCount, int, error) {
	self := exclude.String()
	var total int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM record_meta WHERE record_id <> ?`, self).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count records: %w", err)
	}
	clean := NormalizeTags(tags)
	if len(clean) == 0 {
		return nil, total, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(clean)), ",")
	args := make([]any, 0, len(clean)+1)
	for _, tag := range clean {
		args = append(args, tag)
	}
	args = append(args, self)
	rows, err := s.db.Query(
		`SELECT tag, COUNT(*) FROM record_tags WHERE tag IN (`+placeholders+`) AND record_id <> ?`+
			` GROUP BY tag`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count tags: %w", err)
	}
	defer rows.Close()

	counted := make(map[string]int, len(clean))
	for rows.Next() {
		var tag string
		var n int
		if err := rows.Scan(&tag, &n); err != nil {
			return nil, 0, fmt.Errorf("scan tag count: %w", err)
		}
		counted[tag] = n
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	out := make([]TagCount, len(clean))
	for i, tag := range clean {
		out[i] = TagCount{Tag: tag, Records: counted[tag]}
	}
	return out, total, nil
}

// VocabularyTags lists the tags already in use, most common first, so a caller
// can reuse the vault's existing vocabulary instead of inventing a synonym for
// a tag that is already there.
func (s *Store) VocabularyTags(limit int) ([]TagCount, error) {
	if limit <= 0 {
		limit = 64
	}
	rows, err := s.db.Query(
		`SELECT tag, COUNT(*) AS n FROM record_tags GROUP BY tag ORDER BY n DESC, tag ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read tag vocabulary: %w", err)
	}
	defer rows.Close()

	var out []TagCount
	for rows.Next() {
		var count TagCount
		if err := rows.Scan(&count.Tag, &count.Records); err != nil {
			return nil, fmt.Errorf("scan tag vocabulary: %w", err)
		}
		out = append(out, count)
	}
	return out, rows.Err()
}

// RankingMetaFor reads one record's ranking metadata.
func (s *Store) RankingMetaFor(id record.ID) (RankingMeta, error) {
	var kind, recordType string
	var supersedes sql.NullString
	err := s.db.QueryRow(
		`SELECT kind, type, supersedes FROM record_meta WHERE record_id = ?`,
		id.String()).Scan(&kind, &recordType, &supersedes)
	if errors.Is(err, sql.ErrNoRows) {
		return RankingMeta{}, fmt.Errorf("ranking metadata %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return RankingMeta{}, fmt.Errorf("read ranking metadata %s: %w", id, err)
	}
	meta := RankingMeta{ID: id, Kind: record.Kind(kind), Type: record.Type(recordType)}
	if supersedes.Valid && supersedes.String != "" {
		replaced, err := record.ParseID(supersedes.String)
		if err != nil {
			return RankingMeta{}, err
		}
		meta.Supersedes = &replaced
	}

	rows, err := s.db.Query(`SELECT tag FROM record_tags WHERE record_id = ? ORDER BY tag`, id.String())
	if err != nil {
		return RankingMeta{}, fmt.Errorf("read tags %s: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return RankingMeta{}, fmt.Errorf("scan tag %s: %w", id, err)
		}
		meta.Tags = append(meta.Tags, tag)
	}
	sort.Strings(meta.Tags)
	return meta, rows.Err()
}

// CountRecordMeta reports how many records the device holds ranking metadata
// for, which is the denominator every tag frequency is judged against.
func (s *Store) CountRecordMeta() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM record_meta`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count record metadata: %w", err)
	}
	return n, nil
}
