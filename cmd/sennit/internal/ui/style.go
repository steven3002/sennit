package ui

import (
	"strings"
)

// A Style is what a span of text means. Only this file knows which escape
// sequence draws a meaning, so no command ever writes one directly.
type Style uint8

const (
	Plain Style = iota
	// Success, Warning and Failure are the marks a finished command ends on.
	Success
	Warning
	Failure
	// HintText is cyan text, the colour of advice. Hints are drawn as badges
	// today; this is the rest of the decided palette, kept whole so the table
	// below is the colour system rather than a subset of it.
	HintText
	// Spinner is the live line's glyph.
	Spinner
	// Faint is secondary text: ids, timings, detail.
	Faint
	// Bold is emphasis and headings.
	Bold
	// The badges are a label on a coloured background.
	ErrorBadge
	WarningBadge
	HintBadge
)

// sgr maps each meaning to a Select Graphic Rendition sequence from the 16
// colours every terminal theme defines, so the shades are the theme's own and
// nothing depends on the background being light or dark. Blue, white, black and
// bright black never colour text on the terminal's own background: each of them
// disappears, or nearly, under some common theme. A badge sets its background as
// well as its text, so its contrast does not depend on the theme.
var sgr = map[Style]string{
	Success:      "32",
	Warning:      "33",
	Failure:      "31",
	HintText:     "36",
	Spinner:      "36",
	Faint:        "2",
	Bold:         "1",
	ErrorBadge:   "97;41",
	WarningBadge: "30;43",
	HintBadge:    "30;46",
}

// reset ends every styled span, so a background colour can never bleed past its
// own text to the end of the line.
const reset = "\x1b[0m"

// A Span is a run of text with one style.
type Span struct {
	Style Style
	Text  string
	// Data marks text that came from the vault or from the person running the
	// command, a statement, a tag, a path. It is printed exactly as given; only
	// text this program wrote has its glyphs swapped for a tier that lacks them.
	Data bool
}

// A Line is the spans of one line of output.
type Line []Span

// S is a span of this program's own text in a style.
func S(style Style, text string) Span { return Span{Style: style, Text: text} }

// T is a span of this program's own unstyled text.
func T(text string) Span { return Span{Text: text} }

// D is a span of data, printed as given.
func D(text string) Span { return Span{Text: text, Data: true} }

// A Painter renders lines for one stream.
type Painter struct {
	Color bool
	Tier  Tier
}

// Render draws a line.
func (p Painter) Render(line Line) string {
	var b strings.Builder
	for _, span := range line {
		text := span.Text
		if !span.Data {
			text = p.Glyphs(text)
		}
		code, styled := sgr[span.Style]
		if !p.Color || !styled || text == "" {
			b.WriteString(text)
			continue
		}
		b.WriteString("\x1b[" + code + "m" + text + reset)
	}
	return b.String()
}

// Glyphs swaps the characters a tier cannot draw for ones it can.
func (p Painter) Glyphs(text string) string {
	switch p.Tier {
	case TierASCII:
		return asciiGlyphs.Replace(text)
	case TierBasic:
		return basicGlyphs.Replace(text)
	}
	return text
}

// Ellipsis is how this tier marks text that was cut short.
func (p Painter) Ellipsis() string { return p.Glyphs("…") }

var (
	// basicGlyphs covers the two marks: Cascadia Mono, the Windows Terminal
	// font, has no ✗, and √ and × are both in WGL4.
	basicGlyphs = strings.NewReplacer("✓", "√", "✗", "×")
	asciiGlyphs = strings.NewReplacer(
		"✓", "ok",
		"✗", "x",
		"√", "ok",
		"×", "x",
		" · ", ", ",
		"·", "-",
		"…", "...",
	)
)

// frames are each tier's pulse cycle, one frame every FrameInterval.
//
// The full cycle grows a dot into a ring, counts one, two, three at its widest,
// fills, and shrinks back to its centre, which hands back to the seed so the loop
// has no visible jump. The basic cycle keeps those twelve beats with characters
// the common console fonts carry.
var frames = map[Tier][]string{
	TierFull:  {".", "◡", "◞", "◝", "◌", "၀", "ဝ", "㊀", "㊁", "㊂", "◍", "⊙"},
	TierBasic: {".", "∙", "•", "◦", "○", "○", "●", "●", "●", "○", "◦", "∙"},
	TierASCII: {"|", "/", "-", `\`},
}

// Frames is a tier's pulse cycle.
func (t Tier) Frames() []string { return frames[t] }

// A Mark is the glyph a finished command's line begins with.
type Mark int

const (
	MarkSuccess Mark = iota
	MarkWarning
	MarkFailure
)

func (m Mark) span() Span {
	switch m {
	case MarkWarning:
		return S(Warning, "!")
	case MarkFailure:
		return S(Failure, "✗")
	}
	return S(Success, "✓")
}
