//go:build windows

package rename

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// patience is how long a rename refused because the file is in use is tried
// again. It is long beside the moment a scanner or an indexer holds a file for,
// and short beside what a person would notice.
const patience = 2 * time.Second

// over renames from to to, and tries again while the refusal is the one Windows
// gives for a file that is open. Any other failure is the answer at once: a
// name that does not exist will not exist a moment later either.
func over(from, to string) error {
	deadline := time.Now().Add(patience)
	for wait := 5 * time.Millisecond; ; wait = min(2*wait, 100*time.Millisecond) {
		err := os.Rename(from, to)
		if err == nil || !inUse(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
	}
}

// inUse reports whether a rename failed the way it does when another handle has
// the file open. Windows says access is denied when the destination is the one
// that is open, and reports a sharing violation when it is the source.
func inUse(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
