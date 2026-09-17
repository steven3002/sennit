// Package ui draws what the sennit command shows a person: colour that follows
// the terminal's own theme, one live status line while a command waits, badge
// messages, and the plain forms a pipe, a file or a log receives instead.
//
// It belongs to the command line alone. The MCP server writes protocol traffic
// to stdout and its diagnostics to a host's log, where an escape sequence is
// noise at best, so nothing here is importable from outside cmd/sennit.
package ui

import (
	"strings"
)

// Environment variables the policy reads beyond the conventional ones.
const (
	// NoProgressEnv, set to any value, turns the live status line off and prints
	// one plain line per phase instead.
	NoProgressEnv = "SENNIT_NO_PROGRESS"
	// GlyphsEnv chooses the glyph tier: full, basic or ascii. A program cannot
	// ask a terminal whether its font carries a glyph, so this is how a person
	// whose terminal draws boxes picks a set it can draw.
	GlyphsEnv = "SENNIT_GLYPHS"
)

// A Tier is the set of glyphs a run draws.
type Tier int

const (
	// TierFull is the pulse cycle and the check and cross marks.
	TierFull Tier = iota
	// TierBasic keeps the full cycle's rhythm and slot with characters from
	// Microsoft's WGL4 set, which the common console fonts carry.
	TierBasic
	// TierASCII is for a terminal that cannot show Unicode at all.
	TierASCII
)

// A ColorMode is the --color flag's value.
type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

// ParseColorMode reads a --color value.
func ParseColorMode(value string) (ColorMode, bool) {
	switch mode := ColorMode(value); mode {
	case ColorAuto, ColorAlways, ColorNever:
		return mode, true
	}
	return "", false
}

// A Probe is what the environment says about one run, gathered by the caller so
// that the policy itself is a pure function a test can drive.
type Probe struct {
	StdoutTerminal bool
	StderrTerminal bool
	// StderrEscapes is false when stderr is a Windows console that refused
	// virtual terminal processing, where an escape sequence prints as text.
	StderrEscapes bool
	GOOS          string
	Getenv        func(string) string
}

// A Policy is what a run may draw on each stream.
type Policy struct {
	StdoutTerminal, StderrTerminal bool
	StdoutColor, StderrColor       bool
	// Animate is whether the live status line is drawn in place. When it is
	// not, each phase is a plain line of its own.
	Animate bool
	Tier    Tier
}

// Decide applies the output policy to one run.
//
// Colour is decided per stream, highest first: --color, then NO_COLOR, then
// FORCE_COLOR, then TERM=dumb, then whether the stream is a terminal. A flag
// outranks NO_COLOR because it is a per-invocation choice, which no-color.org
// asks programs to honour over the environment. Colour and animation are
// separate questions: NO_COLOR removes colour and leaves the live line, and
// SENNIT_NO_PROGRESS or TERM=dumb removes the live line.
//
// The line animates only when stdout and stderr are both terminals. With stdout
// piped into another program, redrawing stderr in place would interleave with
// whatever that program prints to the same screen.
func Decide(mode ColorMode, p Probe) Policy {
	if p.Getenv == nil {
		p.Getenv = func(string) string { return "" }
	}
	policy := Policy{
		StdoutTerminal: p.StdoutTerminal,
		StderrTerminal: p.StderrTerminal,
		StdoutColor:    colorOn(mode, p.StdoutTerminal, p.Getenv),
		StderrColor:    colorOn(mode, p.StderrTerminal, p.Getenv),
		Tier:           tierFor(p),
	}
	policy.Animate = p.StdoutTerminal && p.StderrTerminal && p.StderrEscapes &&
		p.Getenv(NoProgressEnv) == "" && p.Getenv("TERM") != "dumb"
	return policy
}

func colorOn(mode ColorMode, terminal bool, getenv func(string) string) bool {
	switch {
	case mode == ColorAlways:
		return true
	case mode == ColorNever:
		return false
	case getenv("NO_COLOR") != "":
		return false
	case getenv("FORCE_COLOR") != "":
		return true
	case getenv("TERM") == "dumb":
		return false
	}
	return terminal
}

// tierFor picks the glyph tier: SENNIT_GLYPHS when it names a tier, ASCII when
// the terminal cannot show Unicode, basic in a Windows console other than
// Windows Terminal, whose fonts lack most of the full cycle, and full elsewhere.
// A value of SENNIT_GLYPHS that names no tier is ignored rather than refused,
// because it can only make the output worse to fail a command over it.
func tierFor(p Probe) Tier {
	switch p.Getenv(GlyphsEnv) {
	case "full":
		return TierFull
	case "basic":
		return TierBasic
	case "ascii":
		return TierASCII
	}
	switch {
	case !unicodeOn(p):
		return TierASCII
	case p.GOOS == "windows" && p.Getenv("WT_SESSION") == "":
		return TierBasic
	}
	return TierFull
}

// unicodeOn reports whether the terminal can show characters beyond ASCII.
//
// Go writes to a Windows console in UTF-16 whatever the code page, so there the
// encoding never fails and only the font can, which the tier covers. Elsewhere
// the locale decides, in the order POSIX gives it: the first of LC_ALL,
// LC_CTYPE and LANG that is set and not empty. With none of them set the POSIX
// locale applies, and it is ASCII. The Linux kernel console draws from a
// 512-glyph font whatever the locale says.
func unicodeOn(p Probe) bool {
	if p.GOOS == "windows" {
		return true
	}
	if p.Getenv("TERM") == "linux" {
		return false
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := p.Getenv(name); value != "" {
			return utf8Locale(value)
		}
	}
	return false
}

// utf8Locale reports whether a locale name of the form
// language[_territory][.codeset][@modifier] names the UTF-8 codeset. A bare
// codeset with no language counts as well: iTerm2 sets LC_CTYPE=UTF-8 when it
// cannot build a full name, and the C library on macOS accepts it.
func utf8Locale(locale string) bool {
	codeset := locale
	if _, after, found := strings.Cut(locale, "."); found {
		codeset = after
	}
	codeset, _, _ = strings.Cut(codeset, "@")
	switch strings.ToLower(codeset) {
	case "utf-8", "utf8":
		return true
	}
	return false
}
