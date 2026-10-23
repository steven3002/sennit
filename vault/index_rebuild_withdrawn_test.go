package vault

import (
	"context"
	"strings"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// A forget withdraws a record's vector from the index before it lets go of the
// last of the record, so one that stops part way leaves the removal in the index
// and the record's ranking rows on the device, with its body or its head. That
// is how a conversation's forget leaves it when it stops after the vector, and
// it is not a record that lost its vector. A rebuild run while the removal is in
// the index does not give the record its vector back.
//
// The rebuild compacts the index once it has read past the damage, and a
// compaction carries no removal into the new base. The removal it read before
// then has to hold for the rest of the rebuild: a later round that read the
// index afresh found a record held whole with no vector, and embedded it.
func TestARebuildGivesNoVectorToARecordAForgetHasBegunOn(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(t *testing.T, v *Vault) (record.ID, string)
	}{
		{"a conversation", func(t *testing.T, v *Vault) (record.ID, string) {
			saved, err := v.SaveSession(context.Background(), SaveSessionRequest{
				Title:    "Breakwater demolition",
				Summary:  "Planned the demolition of the old breakwater.",
				Messages: exchange("breakwater", false),
			})
			if err != nil {
				t.Fatalf("save the conversation: %v", err)
			}
			return saved.ID, "breakwater"
		}},
		{"a memory", func(t *testing.T, v *Vault) (record.ID, string) {
			return rememberFact(t, v, "The old breakwater at Tidepool was demolished.", "breakwater"), "breakwater"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			v := ledgerVault(t, home)
			withdrawn, words := c.write(t, v)
			// The forget's first step, and the only one this forget took.
			if err := v.removeVector(withdrawn); err != nil {
				t.Fatalf("withdraw the vector: %v", err)
			}
			ids := threeFacts(t, v)
			v.Close()
			damageIndexRecord(t, home, index.DeltaName, 4, garbleRecord)

			embedder := counting()
			reopened := openWith(t, home, embedder)
			if reopened.index.Has(withdrawn) {
				t.Error("the rebuild gave its vector back to a record a forget had begun on")
			}
			for _, text := range embedder.embedded() {
				if strings.Contains(strings.ToLower(text), words) {
					t.Errorf("the rebuild embedded the record a forget had begun on: %q", text)
				}
			}
			searchable(t, reopened, "memories held whole", ids...)
			reopened.Close()

			if again := openWith(t, home, counting()); again.index.Has(withdrawn) {
				t.Error("the record a forget had begun on has a vector in the open after the rebuild")
			}
		})
	}
}
