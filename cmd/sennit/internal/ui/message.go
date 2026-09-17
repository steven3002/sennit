package ui

import (
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

// A Kind is what a message is: a problem, a caution, or advice.
type Kind int

const (
	KindError Kind = iota
	KindWarning
	KindHint
)

func (k Kind) label() string {
	switch k {
	case KindWarning:
		return "WARNING"
	case KindHint:
		return "HINT"
	}
	return "ERROR"
}

func (k Kind) badge() Style {
	switch k {
	case KindWarning:
		return WarningBadge
	case KindHint:
		return HintBadge
	}
	return ErrorBadge
}

// A Message is one error, warning or hint with what explains it and what fixes
// it.
//
// The problem and the fix are kept apart on purpose. The text says what is
// wrong, short and lowercase; the hints say what to do about it. A message that
// mixes the two makes a reader parse a paragraph to find the one command they
// need.
type Message struct {
	Kind Kind
	// Text is the problem, with no trailing period.
	Text string
	// Data are lines printed under the text and never wrapped, such as an id or
	// a statement. On a terminal they are cut to the width instead.
	Data []string
	// Explanation are paragraphs of context, each wrapped.
	Explanation []string
	// Hints are the fixes, each its own line.
	Hints []string
}

// MessageMeasure is the widest a wrapped message line may be. A paragraph much
// wider than this is hard to read whatever the terminal allows.
const MessageMeasure = 64

// messageColumn is where message text begins after an ERROR or HINT badge.
const messageColumn = 8

// Lines lays a message out as badges when colour is on, and as plain labels
// otherwise.
//
// Badges line up per block: a block's text, its data and its hints all start
// one column past its widest badge, so a warning's hints align with the warning
// rather than with an error elsewhere. Labels never wrap, because a line in a
// log or a pipe is better whole than broken at a width nothing will display.
func (m Message) Lines(badges bool, termWidth int) []Line {
	if !badges {
		return m.labelled()
	}
	limit := MessageMeasure
	if termWidth > 0 && termWidth-1 < limit {
		limit = termWidth - 1
	}
	column := max(messageColumn, badgeCells(m.Kind)+1)
	gap := func(kind Kind) string { return strings.Repeat(" ", column-badgeCells(kind)) }
	indent := strings.Repeat(" ", column)

	var out []Line
	for i, text := range Wrap(m.Text, column, limit) {
		if i == 0 {
			out = append(out, Line{badge(m.Kind), T(gap(m.Kind)), D(text)})
			continue
		}
		out = append(out, Line{T(indent), D(text)})
	}
	for _, data := range m.Data {
		line := Line{T(indent), D(data)}
		if termWidth > 0 {
			if cut := width.Truncate(data, termWidth-1-column, "…", 1); cut != data {
				line = Line{T(indent), D(strings.TrimSuffix(cut, "…")), T("…")}
			}
		}
		out = append(out, line)
	}
	for _, paragraph := range m.Explanation {
		for _, text := range Wrap(paragraph, 2, limit) {
			out = append(out, Line{T("  "), D(text)})
		}
	}
	if len(m.Hints) > 0 {
		out = append(out, Line{})
	}
	for _, hint := range m.Hints {
		for i, text := range Wrap(hint, column, limit) {
			if i == 0 {
				out = append(out, Line{badge(KindHint), T(gap(KindHint)), D(text)})
				continue
			}
			out = append(out, Line{T(indent), D(text)})
		}
	}
	return out
}

func (m Message) labelled() []Line {
	out := []Line{{T(strings.ToLower(m.Kind.label()) + ": "), D(m.Text)}}
	for _, data := range m.Data {
		out = append(out, Line{T("  "), D(data)})
	}
	if len(m.Explanation) > 0 {
		out = append(out, Line{})
		for _, paragraph := range m.Explanation {
			out = append(out, Line{T("  "), D(paragraph)})
		}
	}
	if len(m.Hints) > 0 {
		out = append(out, Line{})
	}
	for _, hint := range m.Hints {
		out = append(out, Line{T("hint: "), D(hint)})
	}
	return out
}

// badge is a kind's label with one space either side, all of it coloured.
func badge(kind Kind) Span { return S(kind.badge(), " "+kind.label()+" ") }

func badgeCells(kind Kind) int { return len(kind.label()) + 2 }
