package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/internal/private/privatetest"
)

// A host shows a server's log to the person running it, and a vault in a folder
// other accounts can read is something that person should hear about. It is
// said before the line that says what is being served.
func TestTheServerTellsItsHostOfAFolderOthersCanRead(t *testing.T) {
	home := filepath.Join(t.TempDir(), "shared")
	privatetest.OpenFolderToEveryone(t, home)

	lines := announced(t, home)
	if len(lines) != 2 {
		t.Fatalf("the server said %d line(s), want the open folder and then what it serves: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "sennit-mcp: the vault's folder can be read by other accounts on this machine: it lets in ") {
		t.Errorf("the server's first line is %q, want the open folder", lines[0])
	}
	if !strings.HasPrefix(lines[1], "sennit-mcp: serving ") {
		t.Errorf("the server's last line is %q, want what it serves", lines[1])
	}

	// A vault in a folder it made says nothing of the kind.
	if lines := announced(t, filepath.Join(t.TempDir(), "made-here")); len(lines) != 1 {
		t.Errorf("a vault in a folder it made is announced with %d lines: %q", len(lines), lines)
	}
}
