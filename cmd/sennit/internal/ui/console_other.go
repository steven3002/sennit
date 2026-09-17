//go:build !windows

package ui

import "os"

// EnableEscapes reports that f interprets escape sequences. A Unix terminal does
// so without being asked, so there is nothing to enable and nothing to restore.
func EnableEscapes(*os.File) (bool, func()) { return true, func() {} }
