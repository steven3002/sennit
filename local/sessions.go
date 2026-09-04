package local

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steven3002/sennit/record"
)

// A SessionRow is a session as a listing sees it: the head's columns without
// the head.
//
// It exists so that listing a thousand sessions reads a thousand short rows and
// no transcripts. That is the whole reason a session is a head plus chunks, and
// it stops being true the moment a lister has to open something to render a
// line.
type SessionRow struct {
	ID       record.ID
	Version  int64
	Title    string
	Summary  string
	Kind     record.SessionKind
	Project  string
	Agent    string
	Parent   *record.ID
	Created  record.Time
	Updated  record.Time
	Archived bool
	Counts   record.Counts
}

// A SessionQuery narrows a listing.
//
// Every field here is a property of the head. Nothing in a listing may depend
// on a chunk.
type SessionQuery struct {
	// Kinds selects session classes. Empty means every class.
	Kinds []record.SessionKind
	// Parent selects the sub-agent runs of one session.
	Parent *record.ID
	// Project matches the session's project key exactly.
	Project string
	// Tags requires every listed tag. A listing is a browse and not a ranked
	// search, so this one is an ordinary filter, the reason ranking's filters
	// only ever prefer is that a *ranked* answer must never come back empty
	// because a guess was wrong, and a listing's caller is not guessing.
	Tags []string
	// Since and Until bound the last update.
	Since, Until *record.Time
	// IncludeArchived returns archived sessions as well.
	IncludeArchived bool
	// Limit and Offset page the result, newest first.
	Limit, Offset int
}

// ErrStaleHead reports that a session head changed under a writer.
//
// It is the one place the vault has a lost-update problem to solve. Appending
// to a session is read the head, write chunks, write the head back, and the
// middle step reaches the network, so it cannot be held inside a transaction.
// Several processes share one vault by design, so the guard is a version check
// rather than a lock: a writer that lost the race is told, instead of silently
// erasing the turns the other one appended.
var ErrStaleHead = errors.New("the session was changed by another writer")

// PutSessionHead writes or replaces a session head, provided it is still at the
// version the writer read.
//
// A zero expected version means the session is being created and must not
// already exist. The head is the one mutable record the vault holds, and it is
// written whole on every append, affordable precisely because it is small: the
// transcript it describes is somewhere else and stays there.
func (s *Store) PutSessionHead(session *record.Session, head []byte, expect int64) error {
	var parent any
	if session.Lineage.ParentSession != nil {
		parent = session.Lineage.ParentSession.String()
	}
	// The WHERE on the update arm tests the row that is already there, so a
	// creation whose id is taken and an append whose head has moved on both
	// come back as nothing written rather than as an overwrite.
	result, err := s.db.Exec(
		`INSERT INTO session_heads
		   (session_id, version, title, summary, kind, project, agent, parent,
		    created_at, updated_at, archived, messages, chunks, bytes, head)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   version = excluded.version, title = excluded.title, summary = excluded.summary,
		   kind = excluded.kind, project = excluded.project, agent = excluded.agent,
		   parent = excluded.parent, updated_at = excluded.updated_at,
		   archived = excluded.archived, messages = excluded.messages,
		   chunks = excluded.chunks, bytes = excluded.bytes, head = excluded.head
		 WHERE session_heads.version = ?`,
		session.ID.String(), session.Version, session.Title, session.Summary,
		string(session.Kind), ProjectKey(session.Project), session.Agent.Name, parent,
		session.Created.String(), session.Updated.String(), session.Archived,
		session.Counts.Messages, len(session.Chunks), session.Counts.Bytes, head,
		expect)
	if err != nil {
		return fmt.Errorf("store session head %s: %w", session.ID, err)
	}
	written, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store session head %s: %w", session.ID, err)
	}
	if written == 0 {
		return fmt.Errorf("session %s: %w (expected version %d)", session.ID, ErrStaleHead, expect)
	}
	return nil
}

// GetSessionHead reads one session head.
func (s *Store) GetSessionHead(id record.ID) ([]byte, error) {
	var head []byte
	err := s.db.QueryRow(`SELECT head FROM session_heads WHERE session_id = ?`, id.String()).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read session head %s: %w", id, err)
	}
	return head, nil
}

// SessionTitle reads just the title of one session.
//
// It selects the denormalised column and not the head, so naming a session in a
// listing costs a short row rather than the head's whole chunk list.
func (s *Store) SessionTitle(id record.ID) (string, error) {
	var title string
	err := s.db.QueryRow(`SELECT title FROM session_heads WHERE session_id = ?`, id.String()).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read session title %s: %w", id, err)
	}
	return title, nil
}

// ForgetSessionHead drops a session head from the device.
func (s *Store) ForgetSessionHead(id record.ID) error {
	if _, err := s.db.Exec(`DELETE FROM session_heads WHERE session_id = ?`, id.String()); err != nil {
		return fmt.Errorf("forget session %s: %w", id, err)
	}
	return nil
}

// CountSessions reports how many session heads the device holds.
func (s *Store) CountSessions() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_heads`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return n, nil
}

// ListSessions returns session heads newest first.
//
// The head column is deliberately not selected. Reading it would make listing
// cost the size of every transcript's chunk list, which is the one thing this
// shape exists to avoid.
func (s *Store) ListSessions(query SessionQuery) ([]SessionRow, error) {
	where := []string{"1 = 1"}
	var args []any

	if !query.IncludeArchived {
		where = append(where, "archived = 0")
	}
	if len(query.Kinds) > 0 {
		placeholders := make([]string, len(query.Kinds))
		for i, kind := range query.Kinds {
			placeholders[i] = "?"
			args = append(args, string(kind))
		}
		where = append(where, "kind IN ("+strings.Join(placeholders, ",")+")")
	}
	if query.Parent != nil {
		where = append(where, "parent = ?")
		args = append(args, query.Parent.String())
	}
	if query.Project != "" {
		where = append(where, "project = ?")
		args = append(args, query.Project)
	}
	if query.Since != nil {
		where = append(where, "updated_at >= ?")
		args = append(args, query.Since.String())
	}
	if query.Until != nil {
		where = append(where, "updated_at <= ?")
		args = append(args, query.Until.String())
	}
	for _, tag := range NormalizeTags(query.Tags) {
		where = append(where,
			"EXISTS (SELECT 1 FROM record_tags WHERE record_tags.record_id = session_heads.session_id AND record_tags.tag = ?)")
		args = append(args, tag)
	}

	limit := query.Limit
	if limit <= 0 {
		limit = DefaultSessionLimit
	}
	args = append(args, limit, max(query.Offset, 0))

	rows, err := s.db.Query(
		`SELECT session_id, version, title, summary, kind, project, agent, parent,
		        created_at, updated_at, archived, messages, chunks, bytes
		 FROM session_heads
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY updated_at DESC, session_id ASC
		 LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var (
			row            SessionRow
			idHex, kind    string
			created, updat string
			parent         sql.NullString
			chunks         int
		)
		if err := rows.Scan(&idHex, &row.Version, &row.Title, &row.Summary, &kind, &row.Project,
			&row.Agent, &parent, &created, &updat, &row.Archived,
			&row.Counts.Messages, &chunks, &row.Counts.Bytes); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return nil, err
		}
		row.ID, row.Kind, row.Counts.Chunks = id, record.SessionKind(kind), chunks
		if err := row.Created.UnmarshalJSON([]byte(`"` + created + `"`)); err != nil {
			return nil, fmt.Errorf("session %s created: %w", idHex, err)
		}
		if err := row.Updated.UnmarshalJSON([]byte(`"` + updat + `"`)); err != nil {
			return nil, fmt.Errorf("session %s updated: %w", idHex, err)
		}
		if parent.Valid && parent.String != "" {
			parentID, err := record.ParseID(parent.String)
			if err != nil {
				return nil, fmt.Errorf("session %s parent: %w", idHex, err)
			}
			row.Parent = &parentID
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DefaultSessionLimit is how many sessions a listing returns when the caller
// does not say. It is generous because the whole cost of a listing is the rows.
const DefaultSessionLimit = 100

// ProjectKey is the single string a session is filed under.
//
// A repository is the stronger identity, the same checkout moves between
// directories and the same directory is reused for different work, so it wins
// where both are present.
func ProjectKey(project record.Project) string {
	switch {
	case project.Repo != "":
		return project.Repo
	default:
		return project.CWD
	}
}

// SessionLexicalText is what the lexical index holds for a session.
//
// It is the same material the vector pass reads. The transcript is not in it,
// and that is the point: a term index over a whole conversation matches on
// filler and tool noise, which is the same reason the transcript is never
// embedded.
func SessionLexicalText(s *record.Session) string {
	parts := make([]string, 0, 3)
	if s.Title != "" {
		parts = append(parts, s.Title)
	}
	if s.Summary != "" {
		parts = append(parts, s.Summary)
	}
	if len(s.Tags) > 0 {
		parts = append(parts, strings.Join(s.Tags, " "))
	}
	return strings.Join(parts, "\n")
}
