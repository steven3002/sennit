package local

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/steven3002/sennit/record"
)

// LexicalText is the text the lexical index holds for one record.
//
// It is the same material the vector pass reads, plus the tags and keywords the
// agent supplied. Those two are lexical by nature, an agent writes them as the
// words it expects to be searched for, so they earn their place in a term index
// even though the embedded text deliberately leaves them out.
func LexicalText(m *record.Memory) string {
	parts := make([]string, 0, 4)
	parts = append(parts, m.Statement)
	if m.Context != "" {
		parts = append(parts, m.Context)
	}
	if len(m.Keywords) > 0 {
		parts = append(parts, strings.Join(m.Keywords, " "))
	}
	if len(m.Tags) > 0 {
		parts = append(parts, strings.Join(m.Tags, " "))
	}
	return strings.Join(parts, "\n")
}

// Terms reduces text to the terms the lexical index compares, which is how a
// caller can tell whether a query and a record share any word at all, the
// question that decides whether a lexical signal has anything to contribute.
func Terms(text string) []string { return analyze(text) }

// analyze reduces text to the terms the index compares.
//
// Lowercase, split on anything that is not a letter or digit, then drop single
// characters and stopwords. There is no stemmer: memory statements are one
// sentence long, and a stemmer is a moving part whose failures are invisible
// until a query stops working.
//
// The analysed terms are what gets stored, rather than the original text. FTS5
// would otherwise apply its own tokenizer to the raw string and index every
// "the" and "what" in the vault, which changes what a term is worth: the words a
// question is phrased with are exactly the ones that carry no information about
// which record answers it. Storing terms rather than prose puts that decision
// here, where it is visible and testable, instead of in a tokenizer setting.
// Normalised terms re-tokenize to themselves, so the index sees precisely this
// list.
func analyze(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) < 2 || stopwords[field] {
			continue
		}
		out = append(out, field)
	}
	return out
}

// stopwords are the words too common to separate one record from another.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true, "not": true,
	"you": true, "all": true, "can": true, "her": true, "was": true, "one": true,
	"our": true, "out": true, "his": true, "has": true, "had": true, "how": true,
	"its": true, "who": true, "did": true, "yes": true, "that": true, "this": true,
	"with": true, "from": true, "they": true, "have": true, "were": true, "been": true,
	"what": true, "when": true, "will": true, "would": true, "there": true, "their": true,
	"which": true, "about": true, "into": true, "than": true, "them": true, "then": true,
	"does": true, "doing": true, "just": true, "some": true, "such": true, "only": true,
	"very": true, "also": true, "any": true, "get": true, "got": true, "let": true,
	"say": true, "she": true, "him": true, "why": true, "use": true, "used": true,
}

// putLexical writes one record's terms inside an open transaction.
//
// It is called from the same transaction that records the rest of what ranking
// needs, so a record is never searchable by one pass and invisible to the other.
func putLexical(tx execer, id record.ID, text string) error {
	if _, err := tx.Exec(`DELETE FROM record_lexical WHERE record_id = ?`, id.String()); err != nil {
		return fmt.Errorf("clear lexical terms %s: %w", id, err)
	}
	terms := analyze(text)
	if len(terms) == 0 {
		return nil
	}
	if _, err := tx.Exec(
		`INSERT INTO record_lexical (record_id, terms) VALUES (?, ?)`,
		id.String(), strings.Join(terms, " ")); err != nil {
		return fmt.Errorf("index lexical terms %s: %w", id, err)
	}
	return nil
}

// forgetLexical drops one record's terms inside an open transaction.
func forgetLexical(tx execer, id record.ID) error {
	if _, err := tx.Exec(`DELETE FROM record_lexical WHERE record_id = ?`, id.String()); err != nil {
		return fmt.Errorf("forget lexical terms %s: %w", id, err)
	}
	return nil
}

// SearchLexical returns the k records whose terms best match the query, best
// first.
//
// The ordering is what the caller uses; the BM25 scores themselves are not
// returned because fusion consumes ranks, and a score that nothing reads is a
// score that can drift without any test noticing.
func (s *Store) SearchLexical(query string, k int) ([]record.ID, error) {
	if k <= 0 {
		return nil, nil
	}
	terms := analyze(query)
	if len(terms) == 0 {
		return nil, nil
	}

	// Every term is quoted, so a query word that happens to be FTS5 syntax,
	// "or", "near", "and", is matched as a word rather than parsed as an
	// operator. Analysis has already reduced each term to letters and digits, so
	// a quoted term cannot carry a quote of its own and close the string early.
	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + term + `"`
	}
	match := strings.Join(quoted, " OR ")

	// bm25() is negative, more so the better the match, so ascending order is
	// best first.
	rows, err := s.db.Query(
		`SELECT record_id FROM record_lexical WHERE record_lexical MATCH ?
		 ORDER BY bm25(record_lexical) LIMIT ?`, match, k)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()

	var out []record.ID
	for rows.Next() {
		var idHex string
		if err := rows.Scan(&idHex); err != nil {
			return nil, fmt.Errorf("scan lexical hit: %w", err)
		}
		id, err := record.ParseID(idHex)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountLexical reports how many records carry terms, which is what tells a
// rebuild whether the lexical index is populated.
func (s *Store) CountLexical() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM record_lexical`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count lexical rows: %w", err)
	}
	return n, nil
}
