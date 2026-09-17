package main

import (
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
)

// A hit is one recalled record as the output shows it, with every number
// already in the form it is printed in.
type hit struct {
	// similarity is how close the record sits to the query, or "-" for a record
	// the vector pass never scored, whose zero would read as unrelated.
	similarity string
	// full is the same number to four places, for the piped form.
	full string
	kind string
	typ  string
	// text is a memory's statement or a conversation's title, and context is a
	// memory's context or a conversation's summary.
	text    string
	context string
	tags    []string
	id      string
	// holds is what a conversation carries: messages and chunks.
	holds string
	// note says what put the record here beyond its meaning.
	note string
	// read is the tier the body came from and what it cost, with --verbose.
	read string
}

const (
	// gutter is the space between columns.
	gutter = 2
	// memoryMinimum is the narrowest the MEMORY column is worth having. Below
	// it the TAGS column is dropped to make room, because a statement cut to a
	// few words says nothing while tags are still in every other form.
	memoryMinimum = 24
	// tagsMaximum stops a record with many tags from taking the line.
	tagsMaximum = 24
	// labelColumn is the width of the labels under an expanded row.
	labelColumn = 9
)

// recallTable lays the results out as a table.
//
// Lines use at most one cell less than the width, and a statement that does not
// fit is cut one cell short of its column so that the ellipsis still aligns
// where a terminal draws that character two cells wide.
func recallTable(hits []hit, termWidth int, memoryText, withRead bool) []ui.Line {
	usable := termWidth - 1
	readWidth := 0
	if withRead {
		readWidth = width.String("READ")
		for _, h := range hits {
			readWidth = max(readWidth, width.String(h.read))
		}
		usable -= readWidth + gutter
	}

	matchWidth, typeWidth, tagsWidth, textWidth := width.String("MATCH"), width.String("TYPE"), width.String("TAGS"), 0
	for _, h := range hits {
		matchWidth = max(matchWidth, width.String(h.similarity))
		typeWidth = max(typeWidth, width.String(h.typ))
		tagsWidth = max(tagsWidth, width.String(strings.Join(h.tags, ", ")))
		textWidth = max(textWidth, width.String(h.text))
	}
	tagsWidth = min(tagsWidth, tagsMaximum)

	showTags := true
	memoryWidth := usable - (matchWidth + gutter + typeWidth + gutter + gutter + tagsWidth)
	if memoryWidth < memoryMinimum {
		showTags = false
		memoryWidth = usable - (matchWidth + gutter + typeWidth + gutter)
	}
	memoryWidth = max(min(memoryWidth, textWidth), min(textWidth, memoryMinimum))
	memoryWidth = max(memoryWidth, width.String("MEMORY"))
	memoryColumn := matchWidth + gutter + typeWidth + gutter

	row := func(match, typ, text, tags, read string) string {
		line := width.Pad(match, matchWidth+gutter) + width.Pad(typ, typeWidth+gutter)
		switch {
		case withRead && showTags:
			return line + width.Pad(text, memoryWidth+gutter) + width.Pad(tags, tagsWidth+gutter) + read
		case withRead:
			return line + width.Pad(text, memoryWidth+gutter) + read
		case !showTags || tags == "":
			return strings.TrimRight(line+text, " ")
		}
		return line + width.Pad(text, memoryWidth+gutter) + tags
	}

	out := []ui.Line{{ui.S(ui.Bold, row("MATCH", "TYPE", "MEMORY", "TAGS", "READ"))}}
	for _, h := range hits {
		tags := width.Truncate(strings.Join(h.tags, ", "), tagsWidth, "…", 1)
		if !memoryText {
			text := h.text
			if width.String(text) > memoryWidth {
				text = width.Truncate(text, memoryWidth-1, "…", 1)
			}
			if !withRead {
				out = append(out, ui.Line{ui.D(row(h.similarity, h.typ, text, tags, ""))})
				continue
			}
			out = append(out,
				ui.Line{ui.D(row(h.similarity, h.typ, text, tags, "")), ui.S(ui.Faint, h.read)})
			continue
		}
		out = append(out, expanded(h, row, memoryColumn, memoryWidth, tagsWidth, usable, tags)...)
	}
	return out
}

// expanded is one row with the whole of its text, and the fields a caller needs
// that the table has no column for: what the record says in full, where it came
// from, and the id a supersession has to name.
func expanded(h hit, row func(match, typ, text, tags, read string) string,
	memoryColumn, memoryWidth, tagsWidth, usable int, tags string,
) []ui.Line {
	var out []ui.Line
	for i, line := range ui.Wrap(h.text, 0, memoryWidth) {
		if i == 0 {
			out = append(out, ui.Line{ui.D(row(h.similarity, h.typ, line, tags, ""))})
			continue
		}
		out = append(out, ui.Line{ui.D(strings.Repeat(" ", memoryColumn) + line)})
	}
	label := func(name, value string) {
		if value == "" {
			return
		}
		indent := memoryColumn + labelColumn
		for i, line := range ui.Wrap(value, indent, usable) {
			if i == 0 {
				out = append(out, ui.Line{ui.D(strings.Repeat(" ", memoryColumn) + width.Pad(name, labelColumn) + line)})
				continue
			}
			out = append(out, ui.Line{ui.D(strings.Repeat(" ", indent) + line)})
		}
	}
	if h.kind == "session" {
		label("summary", h.context)
		label("holds", h.holds)
	} else {
		label("context", h.context)
	}
	label("note", h.note)
	label("read", h.read)
	label("id", h.id)
	return out
}

// recallTSV is the form a pipe receives: one line per record, tab separated, no
// header, nothing cut off.
//
// A tab, newline, carriage return or backslash inside a field is written as a
// two-character escape, so a field can never break a line or invent a column.
func recallTSV(hits []hit) []string {
	escape := strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, strings.Join([]string{
			h.full, h.typ, escape.Replace(strings.Join(h.tags, ",")), h.id,
			escape.Replace(h.text), escape.Replace(h.context),
		}, "\t"))
	}
	return out
}

// found is the past-tense line a search ends on, which counts the two kinds of
// record apart because they answer different questions.
func found(memories, conversations int) string {
	switch {
	case memories == 0 && conversations == 0:
		return "No records matched"
	case conversations == 0:
		return "Found " + plural(memories, "memory")
	case memories == 0:
		return "Found " + plural(conversations, "conversation")
	}
	return "Found " + plural(memories, "memory") + " and " + plural(conversations, "conversation")
}
