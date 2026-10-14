package embedtest_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
)

// Left to itself, a test run looks for the model under the home directory, on
// every platform. Windows names that directory in USERPROFILE and an ordinary
// session there has no HOME at all.
func TestTheModelIsLookedForUnderTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv(embedtest.ModelDirEnv, "")
	if runtime.GOOS == "windows" {
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}

	want := filepath.Join(home, ".cache", "sennit", "models")
	if got := embedtest.ModelDir(); got != want {
		t.Errorf("the model is looked for in %q, want %q", got, want)
	}
}
