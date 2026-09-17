package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

// LiveElapsed is the clock on the live line: whole seconds, then minutes and
// seconds, then hours and minutes. It ticks, so a tenth of a second would only
// flicker.
func LiveElapsed(d time.Duration) string {
	d = d.Truncate(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// FinalElapsed is the time on a finished command's line: tenths of a second
// under a minute, where the difference between a fast and a slow run lives, and
// the live clock's form above it.
func FinalElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return LiveElapsed(d)
}

// LowerFirst lowers the first letter, so a phase named as a sentence can follow
// "Still" or "Cancelled while".
func LowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}

// Commas writes a count with thousands separators.
func Commas(n int) string {
	digits := fmt.Sprint(n)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return sign + b.String()
}

// Wrap breaks text into lines that fit within limit cells when each line begins
// at column indent. A `code span` is never broken, so a command a person is
// meant to copy stays on one line: one that does not fit on the current line
// starts the next, and one longer than a whole line overflows it.
func Wrap(text string, indent, limit int) []string {
	room := limit - indent
	if room < 10 {
		room = 10
	}
	var lines []string
	var line strings.Builder
	used := 0
	for _, unit := range units(text) {
		cells := width.String(unit)
		switch {
		case used == 0:
			line.WriteString(unit)
			used = cells
		case used+1+cells <= room:
			line.WriteString(" " + unit)
			used += 1 + cells
		default:
			lines = append(lines, line.String())
			line.Reset()
			line.WriteString(unit)
			used = cells
		}
	}
	if used > 0 || len(lines) == 0 {
		lines = append(lines, line.String())
	}
	return lines
}

// units splits text on spaces, keeping backticked spans whole.
func units(text string) []string {
	var out []string
	var current strings.Builder
	inCode := false
	for _, r := range text {
		switch {
		case r == '`':
			inCode = !inCode
			current.WriteRune(r)
		case r == ' ' && !inCode:
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// KeyValues lays out a report: each label padded to the widest label and two
// spaces. A row with neither label nor value is a blank line.
func KeyValues(rows [][2]string) []string {
	labelWidth := 0
	for _, row := range rows {
		labelWidth = max(labelWidth, width.String(row[0]))
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row[0] == "" && row[1] == "" {
			out = append(out, "")
			continue
		}
		out = append(out, width.Pad(row[0], labelWidth+2)+row[1])
	}
	return out
}
