// Package width measures text in terminal cells.
//
// A terminal draws most characters in one cell, East Asian wide and fullwidth
// characters in two, and combining marks, format characters and variation
// selectors in none. Counting bytes or runes instead is what misaligns a table
// and wraps a status line that was meant to fit, and a line that wraps cannot be
// redrawn in place: a carriage return only returns to the start of the last
// physical row.
//
// The tables come from Unicode's own EastAsianWidth.txt, at the release the Go
// toolchain's unicode package implements. Where terminals disagree with each
// other the measure errs towards counting more cells than are drawn, never
// fewer, because over-counting only leaves a gap while under-counting wraps.
package width

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// emojiPresentation is VARIATION SELECTOR-16. A character followed by it asks
// for its emoji form, which terminals draw two cells wide even when the
// character alone is narrow.
const emojiPresentation = '️'

// Rune reports how many cells r occupies on its own. Ambiguous runes count as
// the given width: one where a terminal treats them as narrow, two where it
// treats them as wide.
func Rune(r rune, ambiguous int) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case isWide(r):
		return 2
	case isAmbiguous(r):
		return ambiguous
	}
	return 1
}

// String reports how many cells s occupies with every ambiguous rune counted as
// one cell.
func String(s string) int { return StringWith(s, 1) }

// StringWith reports how many cells s occupies with ambiguous runes counted as
// the given width.
func StringWith(s string, ambiguous int) int {
	n := 0
	for rest := s; rest != ""; {
		size, cells := cluster(rest, ambiguous)
		n += cells
		rest = rest[size:]
	}
	return n
}

// cluster measures the character at the start of s together with the zero-width
// runes that follow it, and reports how many bytes that is and how many cells it
// occupies.
//
// Zero-width joiner sequences are deliberately not joined: a terminal that draws
// one as a single glyph uses fewer cells than the sum counted here, which leaves
// a gap, and one that does not draws exactly the sum.
func cluster(s string, ambiguous int) (size, cells int) {
	base, n := utf8.DecodeRuneInString(s)
	size, cells = n, Rune(base, ambiguous)
	for size < len(s) {
		next, n := utf8.DecodeRuneInString(s[size:])
		if Rune(next, ambiguous) != 0 || next < 0x20 {
			break
		}
		if next == emojiPresentation && cells > 0 {
			cells = 2
		}
		size += n
	}
	return size, cells
}

// Truncate shortens s to at most limit cells, ending in tail when anything was
// cut. A character that would straddle the limit is dropped rather than split,
// together with the marks that belong to it, so the result can be one cell
// shorter than limit.
func Truncate(s string, limit int, tail string, ambiguous int) string {
	if StringWith(s, ambiguous) <= limit {
		return s
	}
	room := limit - StringWith(tail, ambiguous)
	if room < 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for rest := s; rest != ""; {
		size, cells := cluster(rest, ambiguous)
		if used+cells > room {
			break
		}
		b.WriteString(rest[:size])
		used += cells
		rest = rest[size:]
	}
	b.WriteString(tail)
	return b.String()
}

// Pad appends spaces until s occupies at least n cells.
func Pad(s string, n int) string {
	if w := String(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func isWide(r rune) bool      { return in(wide, r) }
func isAmbiguous(r rune) bool { return in(ambiguous, r) }

func in(table [][2]rune, r rune) bool {
	i := sort.Search(len(table), func(i int) bool { return table[i][1] >= r })
	return i < len(table) && table[i][0] <= r
}
