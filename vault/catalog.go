package vault

import (
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
)

// Describe answers the ranking pipeline's questions about a candidate pool.
//
// The vault is the adapter here rather than the store implementing the recall
// interface directly, so that `local` stays a storage package that knows nothing
// about ranking, and `recall` keeps depending on nothing but the index, the
// embedder and the record schema.
func (v *Vault) Describe(ids []record.ID) (recall.Described, error) {
	described, err := v.local.Describe(ids)
	if err != nil {
		return recall.Described{}, err
	}
	out := recall.Described{
		Meta:       make(map[record.ID]recall.Meta, len(described.Meta)),
		Superseded: described.Superseded,
	}
	for id, meta := range described.Meta {
		out.Meta[id] = recall.Meta{Kind: meta.Kind, Type: meta.Type, Tags: meta.Tags}
	}
	return out, nil
}

// rankingMeta is what a memory contributes to the ranking metadata tables.
func rankingMeta(memory *record.Memory) local.RankingMeta {
	return local.RankingMeta{
		ID:         memory.ID,
		Kind:       record.KindMemory,
		Type:       memory.Type,
		Tags:       memory.Tags,
		Supersedes: memory.Supersedes,
		Text:       local.LexicalText(memory),
	}
}

// sessionRankingMeta is what a session contributes to the ranking metadata
// tables.
//
// A session carries no memory type: that vocabulary classifies propositions,
// and a conversation is not a proposition. It ranks on its tags, its class and
// the words of its summary, which is everything a session offers a search.
func sessionRankingMeta(session *record.Session) local.RankingMeta {
	return local.RankingMeta{
		ID:   session.ID,
		Kind: record.KindSession,
		Tags: session.Tags,
		Text: local.SessionLexicalText(session),
	}
}
