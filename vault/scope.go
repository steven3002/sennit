package vault

import (
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
)

// The vault is a Selector as well as a catalog, and this is what keeps it one.
// The pipeline accepts a catalog that is not, and draws that catalog's scoped
// recalls from every record, so a change to Select's signature would otherwise
// still build, and the vault's scoped recalls would quietly go back to missing
// records.
var _ recall.Selector = (*Vault)(nil)

// Select answers the ranking pipeline's question of which records a scope
// selects: every record of those classes that this device holds metadata for.
// It is the other half of the adapter Describe is, for the same reason.
//
// A scoped recall draws its candidates from these, so it is read from the
// device on every scoped recall, as Describe is, rather than kept from an
// earlier one.
func (v *Vault) Select(scope []record.Kind) (map[record.ID]bool, error) {
	selected := make(map[record.ID]bool)
	for _, kind := range scope {
		ids, err := v.local.RankingMetaIDs(kind)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			selected[id] = true
		}
	}
	return selected, nil
}
