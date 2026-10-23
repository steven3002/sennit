package mcp_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// An agent reads sennit://vault before it searches, so a search index the
// server rebuilt as it opened is said there: what was found, what the rebuild
// did, and what an agent can do about the one thing it cannot.
func TestTheVaultResourceSaysWhatTheServerRebuiltInTheSearchIndex(t *testing.T) {
	home := t.TempDir()
	writer, err := vaultForServing(home)
	if err != nil {
		t.Fatalf("open the vault: %v", err)
	}
	for _, statement := range []string{
		"The harbour gauge at Tidepool reads in centimetres.",
		"The river gauge at Tidepool reads in millimetres.",
		"The tide tables at Tidepool are printed every spring.",
	} {
		if _, err := writer.Remember(context.Background(), vault.RememberRequest{
			Statement: statement,
			Context:   "From the fictional Tidepool field guide.",
			Type:      record.TypeFact,
			Tags:      []string{"tidepool"},
		}); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}
	writer.Close()

	path := filepath.Join(home, index.DeltaName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the index's delta: %v", err)
	}
	first := 4 + int(binary.LittleEndian.Uint32(raw))
	second := 4 + int(binary.LittleEndian.Uint32(raw[first:]))
	damaged := bytes.Clone(raw)
	damaged[first+second/2] ^= 0xff
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatalf("damage the index's delta: %v", err)
	}

	detail := vaultCounts(t, connect(t, mcp.New(openVault(t, home))))
	for _, says := range []string{
		"found the search index on this device damaged as it opened",
		"1 record in it could not be read",
		"gave every memory and conversation this device holds that had no vector one, 1 in all",
		"saving it again puts that right",
	} {
		if !strings.Contains(detail.Index, says) {
			t.Errorf("sennit://vault does not say %q: %q", says, detail.Index)
		}
	}
	if detail.Indexed != 3 {
		t.Errorf("sennit://vault counts %d searchable record(s) once the index was rebuilt, want 3", detail.Indexed)
	}

	if again := vaultCounts(t, connect(t, mcp.New(openVault(t, home)))); again.Index != "" {
		t.Errorf("a server over an index with nothing left to rebuild says %q", again.Index)
	}
}
