package vault

import (
	"context"

	"github.com/steven3002/sennit/record"
)

const (
	// NeighbourLimit is how many existing records a write is compared against.
	//
	// Small on purpose. This runs inside the user's wait for a write, and the
	// question it answers, "is this already here", is settled by the closest
	// few or not at all.
	NeighbourLimit = 5

	// ConflictSimilarity is where a neighbour stops being related and starts
	// being the same statement again.
	//
	// ⚠ This threshold is a starting point, not a measurement. The recall work
	// measured how well the right record is *found*; nothing has yet measured
	// where restatement begins, which needs a labelled set of near-duplicates
	// this project does not have. Every neighbour is returned with its score
	// whatever side of the line it falls, so the caller can disagree, the
	// threshold decides a label, never what is reported.
	ConflictSimilarity = 0.90

	// TagCommonShare is the share of a vault a tag can sit on before it stops
	// telling anything apart.
	//
	// Chosen from the measured failure it exists to catch: in a single-domain
	// vault of ten thousand records the tags that could not separate the answer
	// from its neighbours were the ones carried by roughly a third of
	// everything, while tags below about a tenth discriminated well. A quarter
	// sits between the two.
	TagCommonShare = 0.25

	// TagAdviceMinRecords is the vault size below which tag frequency says
	// nothing. A tag on two of three records is not evidence of anything.
	TagAdviceMinRecords = 30
)

// A Neighbour is an existing record close to the one being written.
type Neighbour struct {
	ID   record.ID
	Type record.Type
	Tags []string
	// Statement is the neighbour's own statement, when this device holds it.
	Statement string
	// Similarity is cosine similarity to the record being written.
	Similarity float32
	// Conflict marks a neighbour close enough to be the same statement again
	// rather than a related one.
	Conflict bool
}

// TagSpecificity reports how much one tag can narrow a search of this vault.
type TagSpecificity struct {
	Tag string
	// Records is how many records already carry the tag, and Share is that as a
	// fraction of the vault.
	Records int
	Share   float64
	// TooCommon marks a tag that sits on so much of the vault that filtering on
	// it removes little.
	TooCommon bool
	// New marks a tag no record carries yet. Not a problem, it is how a
	// vocabulary grows, but worth surfacing, because it is also what a typo
	// looks like.
	New bool
}

// TagAdvice reports how well a record's tags will separate it from the rest of
// the vault.
//
// It exists because of a measured failure rather than as a nicety. Soft metadata
// filtering is what carries recall, and it works by preferring records that
// share the query's tags, so in a vault where nearly everything carries the
// same few tags, it has nothing to prefer. Measured on a single-domain vault of
// ten thousand records, filtering left 29% of a query's near neighbours still
// competing, against 11.7% on a mixed vault, and hit@5 fell to 0.797 against
// 0.898. The write is the only moment at which that is cheap to fix, because
// records are immutable once stored.
type TagAdvice struct {
	// Records is how many records the vault holds metadata for.
	Records int
	// Tags describes each tag supplied with this write.
	Tags []TagSpecificity
	// Discriminating is how many of them are specific enough to narrow a search.
	Discriminating int
}

// NeedsNarrowerTags reports whether none of a record's tags will separate it
// from the vault it is joining.
func (a TagAdvice) NeedsNarrowerTags() bool {
	return a.Records >= TagAdviceMinRecords && len(a.Tags) > 0 && a.Discriminating == 0
}

// neighbours finds the records closest to a vector, labelling the ones close
// enough to be restatements.
//
// It reuses the vector the write already computed, so it costs a search over
// vectors held in memory and a handful of local reads, not another embedding.
func (v *Vault) neighbours(ctx context.Context, self record.ID, vector []float32) ([]Neighbour, error) {
	// One more than asked for, because the record being written is in the index
	// by this point and will match itself perfectly.
	matches, err := v.index.Search(vector, NeighbourLimit+1)
	if err != nil {
		return nil, err
	}

	ids := make([]record.ID, 0, len(matches))
	for _, match := range matches {
		if match.ID != self {
			ids = append(ids, match.ID)
		}
	}
	described, err := v.local.Describe(ids)
	if err != nil {
		return nil, err
	}

	out := make([]Neighbour, 0, NeighbourLimit)
	for _, match := range matches {
		if match.ID == self || len(out) == NeighbourLimit {
			continue
		}
		neighbour := Neighbour{
			ID:         match.ID,
			Similarity: match.Score,
			Conflict:   match.Score >= ConflictSimilarity,
		}
		if meta, ok := described.Meta[match.ID]; ok {
			neighbour.Type = meta.Type
			neighbour.Tags = meta.Tags
		}
		// The statement is a convenience for the caller, not a requirement. A
		// device that no longer holds the body still reports the neighbour.
		if body, err := v.local.GetBody(match.ID); err == nil {
			if memory, err := record.Unmarshal(body); err == nil {
				neighbour.Statement = memory.Statement
			}
		}
		out = append(out, neighbour)
	}
	_ = ctx
	return out, nil
}

// tagAdvice reports how specific a record's tags are within this vault.
func (v *Vault) tagAdvice(tags []string) (TagAdvice, error) {
	counts, total, err := v.local.TagFrequencies(tags)
	if err != nil {
		return TagAdvice{}, err
	}
	advice := TagAdvice{Records: total}
	for _, count := range counts {
		specificity := TagSpecificity{Tag: count.Tag, Records: count.Records}
		if total > 0 {
			specificity.Share = float64(count.Records) / float64(total)
		}
		switch {
		case count.Records == 0:
			specificity.New = true
		case total >= TagAdviceMinRecords && specificity.Share > TagCommonShare:
			specificity.TooCommon = true
		}
		if !specificity.TooCommon {
			advice.Discriminating++
		}
		advice.Tags = append(advice.Tags, specificity)
	}
	return advice, nil
}

// TagVocabulary lists the tags this vault already uses, most common first.
//
// It is what lets a caller reuse an existing tag instead of coining a synonym
// for it. Two tags meaning one thing split the records that should have been
// preferred together, and the filter matches exactly.
func (v *Vault) TagVocabulary(limit int) ([]TagSpecificity, error) {
	counts, err := v.local.VocabularyTags(limit)
	if err != nil {
		return nil, err
	}
	total, err := v.local.CountRecordMeta()
	if err != nil {
		return nil, err
	}
	out := make([]TagSpecificity, 0, len(counts))
	for _, count := range counts {
		specificity := TagSpecificity{Tag: count.Tag, Records: count.Records}
		if total > 0 {
			specificity.Share = float64(count.Records) / float64(total)
			specificity.TooCommon = total >= TagAdviceMinRecords && specificity.Share > TagCommonShare
		}
		out = append(out, specificity)
	}
	return out, nil
}
