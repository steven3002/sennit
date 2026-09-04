package vault

import (
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A BrowseRequest lists what the vault holds, by metadata rather than by
// meaning.
//
// It is the complement of recall and not a variant of it. Recall answers "what
// do I know about this", ranks by similarity, and must never come back empty
// because a guess was wrong. Browse answers "what is in here", orders by time,
// and its filters exclude, because a caller listing the records carrying a tag
// is naming what it wants rather than inferring it from the wording of a
// question. Conflating the two would either make browsing unable to select or
// make recall able to lose an answer.
type BrowseRequest struct {
	// Kinds selects record classes. Empty means every class.
	Kinds []record.Kind
	// Types selects memory types, and Tags requires every listed tag.
	Types []record.Type
	Tags  []string
	// IncludeSuperseded lists replaced versions alongside current ones.
	IncludeSuperseded bool
	// Limit caps the page and Cursor continues a previous one.
	Limit  int
	Cursor local.Cursor
}

// A BrowseRow is one record as a listing renders it.
type BrowseRow struct {
	ID      record.ID
	Kind    record.Kind
	Type    record.Type
	Tags    []string
	Created record.Time
	// Label is what to call the record: a memory's statement, a session's title.
	// It is empty when this device no longer holds the record's body, which is an
	// ordinary state rather than an error, the record is still on the network
	// and still addressable.
	Label string
	// Superseded reports that a later record replaced this one.
	Superseded bool
}

// A BrowseResult is one page.
type BrowseResult struct {
	Rows []BrowseRow
	// NextCursor continues the listing. It is empty when the page is the last
	// one, so a caller stops on its absence rather than on a short page.
	NextCursor local.Cursor
}

// RecordCounts is what the vault holds, by class.
type RecordCounts struct {
	Total  int
	ByKind map[record.Kind]int
}

// CountRecords reports what this vault holds.
//
// Chunks are absent by construction rather than by exclusion: a transcript chunk
// carries no ranking metadata, because nothing searches one. What is counted is
// what is addressable.
func (v *Vault) CountRecords() (RecordCounts, error) {
	byKind, err := v.local.CountRecordsByKind()
	if err != nil {
		return RecordCounts{}, err
	}
	counts := RecordCounts{ByKind: byKind}
	for _, n := range byKind {
		counts.Total += n
	}
	return counts, nil
}

// Browse lists records newest first.
func (v *Vault) Browse(req BrowseRequest) (BrowseResult, error) {
	rows, next, err := v.local.ListRecords(local.RecordQuery{
		Kinds:             req.Kinds,
		Types:             req.Types,
		Tags:              req.Tags,
		IncludeSuperseded: req.IncludeSuperseded,
		Limit:             req.Limit,
		Cursor:            req.Cursor,
	})
	if err != nil {
		return BrowseResult{}, err
	}

	out := BrowseResult{Rows: make([]BrowseRow, 0, len(rows)), NextCursor: next}
	for _, row := range rows {
		out.Rows = append(out.Rows, BrowseRow{
			ID:         row.ID,
			Kind:       row.Kind,
			Type:       row.Type,
			Tags:       row.Tags,
			Created:    row.Created,
			Superseded: row.Superseded,
			Label:      v.label(row),
		})
	}
	return out, nil
}

// label reads what to call a record from whatever holds it.
//
// A session's title is a denormalised column, so it costs a row and never a
// transcript. A memory's statement is in its body on this device; a device that
// has dropped the body still lists the record, because the listing is metadata
// and the body is not what makes an entry addressable.
func (v *Vault) label(row local.RecordRow) string {
	switch row.Kind {
	case record.KindSession:
		title, err := v.local.SessionTitle(row.ID)
		if err != nil {
			return ""
		}
		return title
	default:
		body, err := v.local.GetBody(row.ID)
		if err != nil {
			return ""
		}
		memory, err := record.Unmarshal(body)
		if err != nil {
			return ""
		}
		return memory.Statement
	}
}
