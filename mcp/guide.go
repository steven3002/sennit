package mcp

import (
	_ "embed"
	"strings"
)

//go:embed instructions.md
var instructionsMD string

// Instructions is what the model is told about this server at connect.
//
// It is the highest-leverage string in the package, and the reason is measured
// rather than assumed. A production host fetches `resources/list` at connect and
// keeps it to itself, the model never sees it, so registering a resource does
// not make it reachable. Asked for an address it had not been told about, a
// model correctly refused to guess rather than fabricate a plausible URI. What
// closed that gap was naming every address here.
//
// So: anything a caller must be able to reach appears in this string. A test
// enforces it against the server's own registrations, because the failure is
// silent, everything looks registered, and the namespace is simply invisible.
var Instructions = strings.TrimSpace(instructionsMD)

//go:embed guide.md
var guideMD string

// Guide is the sennit://guide resource: the long form of the above, for a
// model that wants more than the connect-time summary.
//
// It exists because instructions are read once and are competing for context
// against everything else in a system prompt, whereas a guide is fetched when it
// is actually wanted. What it adds is the part that decides recall quality: how
// to write a record that can be found again.
var Guide = strings.TrimSpace(guideMD)
