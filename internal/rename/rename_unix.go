//go:build !windows

package rename

import "os"

// A file that is open can be renamed over here: whoever has it open keeps the
// old file, and the name is the new one's from then on.
func over(from, to string) error { return os.Rename(from, to) }
