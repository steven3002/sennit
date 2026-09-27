package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every result is returned twice: once as structuredContent against the tool's
// output schema, and once as text.
//
// Both are required by the specification and they are not redundant in practice.
// A host doing code-mode reads the structured half; a model reading a tool
// result in its context reads the text. Rendering the text from the same value
// the structured half carries is what keeps them from drifting into two
// different answers to one call.

// withLinks builds a tool result's content: the mirrored text, then a resource
// link per record.
//
// Links rather than bare ids, everywhere. An id is a dead end, the model has to
// know which subsystem owns it and how to assemble an address, where a link is
// something it can hand straight back to `open`.
func withLinks(text string, hits []HitOut) []sdk.Content {
	content := make([]sdk.Content, 0, len(hits)+1)
	content = append(content, &sdk.TextContent{Text: text})
	for _, hit := range hits {
		if hit.URI == "" {
			continue
		}
		content = append(content, &sdk.ResourceLink{
			URI:         hit.URI,
			Name:        hit.Title,
			Description: linkDescription(hit),
			MIMEType:    mimeFor(hit.Kind),
		})
	}
	return content
}

// linkDescription carries the score on the link itself, so a host that renders
// only the links still shows how well each one matched.
func linkDescription(hit HitOut) string {
	if hit.Similarity == 0 && hit.Score == 0 {
		return hit.Snippet
	}
	return fmt.Sprintf("similarity %.3f · %s", hit.Similarity, hit.Snippet)
}

func mimeFor(kind string) string {
	if kind == "memory" {
		return "text/markdown"
	}
	return "application/json"
}

func renderMemory(detail MemoryDetail) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", detail.Statement)
	fmt.Fprintf(&out, "%s\n\n", detail.Context)
	fmt.Fprintf(&out, "- type: %s\n", detail.Type)
	if len(detail.Tags) > 0 {
		fmt.Fprintf(&out, "- tags: %s\n", strings.Join(detail.Tags, ", "))
	}
	fmt.Fprintf(&out, "- stored: %s\n", detail.Created)
	if detail.ValidFrom != "" || detail.ValidUntil != "" {
		fmt.Fprintf(&out, "- true of the world: %s to %s\n",
			orElse(detail.ValidFrom, "unstated"), orElse(detail.ValidUntil, "unstated"))
	}
	if detail.Importance > 0 || detail.Confidence > 0 {
		fmt.Fprintf(&out, "- importance %.2f · confidence %.2f\n", detail.Importance, detail.Confidence)
	}
	if detail.Supersedes != "" {
		fmt.Fprintf(&out, "- replaced: %s\n", detail.Supersedes)
	}
	if detail.Session != "" {
		fmt.Fprintf(&out, "- from conversation: %s", detail.Session)
		if detail.Span != "" {
			fmt.Fprintf(&out, " (turns %s)", detail.Span)
		}
		out.WriteByte('\n')
	}
	fmt.Fprintf(&out, "- address: %s\n- read from: %s\n", detail.URI, detail.Tier)
	return out.String()
}

func renderRecall(in RecallIn, out RecallOut) string {
	var text strings.Builder
	if len(out.Results) == 0 {
		fmt.Fprintf(&text, "No records match %q.\n\n%s\n", in.Query, out.Hint)
		return text.String()
	}

	fmt.Fprintf(&text, "%d record(s) for %q, searched over %d:\n\n", len(out.Results), in.Query, out.Searched)
	for i, hit := range out.Results {
		fmt.Fprintf(&text, "%d. [%s] %s\n", i+1, hit.Kind, hit.Title)
		if hit.Snippet != "" && hit.Snippet != hit.Title {
			fmt.Fprintf(&text, "   %s\n", hit.Snippet)
		}
		fmt.Fprintf(&text, "   similarity %.3f", hit.Similarity)
		if hit.Boost > 0 {
			fmt.Fprintf(&text, " · your filter moved it by %.4f", hit.Boost)
		}
		if len(hit.Tags) > 0 {
			fmt.Fprintf(&text, " · %s", strings.Join(hit.Tags, ", "))
		}
		fmt.Fprintf(&text, "\n   %s\n", hit.URI)
	}

	var notes []string
	if out.LexicalHits == 0 {
		notes = append(notes, "no record shares a word with the query; this ranking is by meaning alone")
	}
	if out.SupersededHidden > 0 {
		notes = append(notes, fmt.Sprintf("%d replaced version(s) held back", out.SupersededHidden))
	}
	if out.ScopeExcluded > 0 {
		notes = append(notes, fmt.Sprintf("%d candidate(s) removed by the scope", out.ScopeExcluded))
	}
	if out.NextCursor != "" {
		notes = append(notes, "more results are available; pass nextCursor back")
	}
	if len(notes) > 0 {
		fmt.Fprintf(&text, "\n(%s)\n", strings.Join(notes, "; "))
	}
	if out.Hint != "" {
		fmt.Fprintf(&text, "\n%s\n", out.Hint)
	}
	fmt.Fprint(&text, "\nThese are snippets. Call `open` on an address for the whole record.\n")
	return text.String()
}

func renderRemember(out RememberOut) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Stored as %s\n%s\n", out.URI, out.Durability)

	if len(out.Conflicts) > 0 {
		fmt.Fprintf(&text, "\n⚠ %d existing record(s) may be this statement again:\n", len(out.Conflicts))
		for _, conflict := range out.Conflicts {
			fmt.Fprintf(&text, "  %.3f  %s\n        %s\n",
				conflict.Similarity, conflict.Statement, conflict.URI)
		}
		fmt.Fprint(&text, "  Decide: leave both if they are genuinely different, or write again with "+
			"`supersedes` set if this replaces one.\n")
	} else if len(out.Neighbours) > 0 {
		fmt.Fprint(&text, "\nNearest existing records:\n")
		for _, neighbour := range out.Neighbours {
			fmt.Fprintf(&text, "  %.3f  %s\n", neighbour.Similarity, neighbour.Statement)
		}
	}

	if len(out.Tags) > 0 {
		fmt.Fprint(&text, "\nTags in this vault:\n")
		for _, tag := range out.Tags {
			switch {
			case tag.New:
				fmt.Fprintf(&text, "  %-24s new to this vault\n", tag.Tag)
			case tag.TooCommon:
				fmt.Fprintf(&text, "  %-24s %d record(s), %.0f%% of the vault, too common to narrow anything\n",
					tag.Tag, tag.Records, 100*tag.Share)
			default:
				fmt.Fprintf(&text, "  %-24s %d record(s), %.0f%%\n", tag.Tag, tag.Records, 100*tag.Share)
			}
		}
	}
	if out.Advice != "" {
		fmt.Fprintf(&text, "\n%s\n", out.Advice)
	}
	return text.String()
}

func renderBrowse(out BrowseOut) string {
	var text strings.Builder
	if len(out.Rows) == 0 {
		// The hint is the whole answer, because it names what emptied the page.
		// A heading saying nothing matches would name a filter even when none
		// was set.
		fmt.Fprintf(&text, "%s\n", out.Hint)
		return text.String()
	}
	fmt.Fprintf(&text, "%d record(s), newest first:\n\n", len(out.Rows))
	for _, row := range out.Rows {
		fmt.Fprintf(&text, "  %s  [%s", row.Created, row.Kind)
		if row.Type != "" {
			fmt.Fprintf(&text, "/%s", row.Type)
		}
		fmt.Fprintf(&text, "] %s\n", orElse(row.Label, "(body not held on this device)"))
		if len(row.Tags) > 0 {
			fmt.Fprintf(&text, "                          %s\n", strings.Join(row.Tags, ", "))
		}
		fmt.Fprintf(&text, "                          %s\n", row.URI)
	}
	if out.NextCursor != "" {
		fmt.Fprint(&text, "\nMore remain; pass nextCursor back verbatim.\n")
	}
	return text.String()
}

func renderSaveSession(out SaveSessionOut) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Conversation stored as %s (version %d)\n", out.URI, out.Version)
	fmt.Fprintf(&text, "%d turn(s), %d byte(s), in %d immutable piece(s).\n",
		out.Messages, out.Bytes, out.Chunks)
	fmt.Fprintf(&text, "%s\n", out.Durability)
	fmt.Fprintf(&text, "Transcript: %s\n%s\n", out.Transcript, out.Resume)
	return text.String()
}

func orElse(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// A ranked cursor is an offset into one query's ranking, stamped with the query
// it belongs to.
//
// The stamp is what makes it safe. A ranking has no key order to page along, so
// the position has to be an offset, and an offset carried across a different
// query would silently skip that query's best results while looking like it
// worked. Refusing it is a wrong argument the caller can see; honouring it is a
// wrong answer the caller cannot.
const cursorPrefix = "r1:"

func encodeRankCursor(offset int, query string) string {
	return cursorPrefix + strconv.Itoa(offset) + ":" + queryStamp(query)
}

func decodeRankCursor(cursor, query string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	rest, ok := strings.CutPrefix(cursor, cursorPrefix)
	if !ok {
		return 0, fmt.Errorf("that cursor is not one this server issued")
	}
	offsetPart, stamp, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, fmt.Errorf("that cursor is not one this server issued")
	}
	offset, err := strconv.Atoi(offsetPart)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("that cursor is not one this server issued")
	}
	if stamp != queryStamp(query) {
		return 0, fmt.Errorf("that cursor belongs to a different query; a cursor continues the " +
			"ranking it came from, so re-send the original query or start again without a cursor")
	}
	return offset, nil
}

func queryStamp(query string) string {
	sum := sha256.Sum256([]byte(query))
	return base64.RawURLEncoding.EncodeToString(sum[:6])
}
