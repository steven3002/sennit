// Package build carries the identity of this binary.
//
// It exists so the command line and the MCP server cannot disagree about what
// version they are, which matters as soon as anyone other than the author is
// running this: a bug report that cannot name a build is a bug report that
// cannot be reproduced.
package build

// Version is the release this binary was cut from. Bump it in one place.
const Version = "0.1.0-beta-mvp"

// Commit and Date are stamped at link time by scripts/release.sh:
//
//	-ldflags "-X github.com/steven3002/sennit/build.Commit=$sha"
//
// They are deliberately empty in an ordinary `go build`, so a binary someone
// built themselves says so rather than claiming a provenance it does not have.
var (
	Commit = ""
	Date   = ""
)

// String describes the binary in one line.
func String() string {
	s := Version
	if Commit != "" {
		s += " (" + Commit
		if Date != "" {
			s += ", " + Date
		}
		s += ")"
	} else {
		s += " (built from source)"
	}
	return s
}
