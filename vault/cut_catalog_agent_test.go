package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// cutTheCatalogLog takes the last n bytes off the catalog's log under home, as a
// crash part way through an append leaves it.
func cutTheCatalogLog(t *testing.T, home string, n int) {
	t.Helper()
	path := filepath.Join(home, manifest.LogName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the catalog's log: %v", err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-n], 0o600); err != nil {
		t.Fatalf("cut the catalog's log: %v", err)
	}
}

// An agent hears of a dropped catalog change through the vault resource it is
// told to read before its first write. Nothing else it reads would say so, and
// a record that was being forgotten when the crash came is still there, which
// without the note looks like a forget that did not work.
func TestAnAgentIsToldOfACatalogChangeACrashCutOff(t *testing.T) {
	home := t.TempDir()
	writer := vault.OpenWithLedgerForTest(t, home)
	remember(t, writer, harbourGauge, record.TypeFact, "harbour", "gauge")
	remember(t, writer, riverGauge, record.TypeFact, "river", "gauge")
	vault.FlushToNetworkForTest(t, writer)
	writer.Close()

	cutTheCatalogLog(t, home, 40)

	device := vault.OpenWithLedgerForTest(t, home)
	note, _ := vaultResource(t, serveDevice(t, device))["catalog"].(string)
	for _, says := range []string{"cut off", "one record's latest change", "still queued", "forget it again", "`sennit recover`"} {
		if !strings.Contains(note, says) {
			t.Errorf("the vault resource's catalog note does not say %q: %q", says, note)
		}
	}

	// The next process to open the vault finds a log with nothing cut off, and
	// says nothing about it.
	next := vault.OpenWithLedgerForTest(t, home)
	if note, present := vaultResource(t, serveDevice(t, next))["catalog"]; present {
		t.Errorf("a vault whose catalog had nothing cut off carries a catalog note: %v", note)
	}
}
