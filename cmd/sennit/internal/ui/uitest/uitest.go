// Package uitest turns the expected output of a test into bytes.
//
// An expected line is written once, as text with its styled spans marked
// [[style:text]], and yields both renderings a stream can receive: the plain
// text, and the text with each span wrapped in its escape sequence. The escape
// sequences are spelled out here rather than read from package ui, so a test
// fails when the palette changes instead of agreeing with whatever it became.
package uitest

import (
	"regexp"
	"strings"
)

// sgr is the approved palette, meaning by meaning.
var sgr = map[string]string{
	"ok":            "32",
	"warn":          "33",
	"err":           "31",
	"hint":          "36",
	"glyph":         "36",
	"faint":         "2",
	"bold":          "1",
	"badge-ERROR":   "97;41",
	"badge-WARNING": "30;43",
	"badge-HINT":    "30;46",
}

var span = regexp.MustCompile(`\[\[([a-zA-Z-]+):(.*?)\]\]`)

// Plain is the text of a marked-up expectation with the marks removed.
func Plain(markup string) string {
	return span.ReplaceAllString(markup, "$2")
}

// Colored is a marked-up expectation with every span in its escape sequence.
// It panics on a style it does not know, which is a mistake in the test.
func Colored(markup string) string {
	return span.ReplaceAllStringFunc(markup, func(match string) string {
		parts := span.FindStringSubmatch(match)
		code, ok := sgr[parts[1]]
		if !ok {
			panic("uitest: unknown style " + parts[1])
		}
		return "\x1b[" + code + "m" + parts[2] + "\x1b[0m"
	})
}

var escape = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

// Strip removes every CSI escape sequence, leaving what a person reads.
func Strip(text string) string { return escape.ReplaceAllString(text, "") }

// HasEscape reports whether text carries an escape byte at all.
func HasEscape(text string) bool { return strings.ContainsRune(text, 0x1b) }

// Lines joins lines with a newline after each, the way they are printed.
func Lines(lines ...string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
