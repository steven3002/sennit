package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
)

// A vault in a folder that was already there and that other accounts can read
// is warned of at open, with what to do about it in the reader's own system.
func TestAFolderOthersCanReadIsWarnedOf(t *testing.T) {
	for name, c := range map[string]struct{ home, who, goos string }{
		"warning-folder-open.txt": {
			"/srv/shared/sennit", "every account on this machine, since its mode is 0755", "linux"},
		"warning-folder-open-windows.txt": {`C:\data\sennit`, `BUILTIN\Users`, "windows"},
	} {
		s := ended(t, 80, 400*time.Millisecond)
		s.out.warn(folderOpenTo(c.home, c.who, c.goos))
		s.out.done(ui.MarkSuccess, "Checked this vault")
		s.assert(t, name, fixture(t, name))
	}

	// A path a shell would split is quoted in the line to run.
	hint := folderOpenTo("/home/ada/my vault", "the accounts in its group, since its mode is 0750", "linux").Hints[0]
	if !strings.Contains(hint, "`chmod 700 '/home/ada/my vault'`") {
		t.Errorf("the hint for a path with a space is %q, want the path quoted", hint)
	}
	// Windows is not asked to run a Unix command.
	if hint := folderOpenTo(`C:\data\sennit`, `BUILTIN\Users`, "windows").Hints[0]; strings.Contains(hint, "chmod") {
		t.Errorf("Windows is told to run chmod: %q", hint)
	}
}
