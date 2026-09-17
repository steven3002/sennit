package main

import (
	"encoding/json"
	"strings"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
)

// The JSON these types produce is the shape the MCP recall tool answers with
// when a caller asks for full detail.
//
// One contract rather than two. A script that reads `sennit recall --json` and
// an agent that calls the tool see the same field names, so anything written
// against one works against the other. They are mirrored here rather than
// imported because the MCP types come with the protocol SDK, and the command
// line has no business linking a protocol server into itself; a test checks
// the two shapes field for field.
type recallOut struct {
	Results          []hitOut `json:"results"`
	NextCursor       string   `json:"nextCursor,omitempty"`
	Hint             string   `json:"hint,omitempty"`
	Searched         int      `json:"searched"`
	Considered       int      `json:"considered"`
	LexicalHits      int      `json:"lexicalHits"`
	SupersededHidden int      `json:"supersededHidden,omitempty"`
	ScopeExcluded    int      `json:"scopeExcluded,omitempty"`
	TookMS           int64    `json:"tookMs"`
}

type hitOut struct {
	URI        string   `json:"uri"`
	Kind       string   `json:"kind"`
	Type       string   `json:"type,omitempty"`
	Title      string   `json:"title"`
	Snippet    string   `json:"snippet,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Created    string   `json:"created"`
	Score      float32  `json:"score"`
	Similarity float32  `json:"similarity"`
	Boost      float32  `json:"boost,omitempty"`
	Detail     any      `json:"detail,omitempty"`
}

type memoryDetail struct {
	URI        string   `json:"uri"`
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Statement  string   `json:"statement"`
	Context    string   `json:"context"`
	Tags       []string `json:"tags,omitempty"`
	Created    string   `json:"created"`
	Updated    string   `json:"updated,omitempty"`
	Importance float64  `json:"importance,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	ValidFrom  string   `json:"validFrom,omitempty"`
	ValidUntil string   `json:"validUntil,omitempty"`
	Supersedes string   `json:"supersedes,omitempty"`
	Session    string   `json:"session,omitempty"`
	Span       string   `json:"span,omitempty"`
	Links      []string `json:"links,omitempty"`
	Tier       string   `json:"tier"`
}

type sessionDetail struct {
	URI         string   `json:"uri"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Kind        string   `json:"kind"`
	Agent       string   `json:"agent,omitempty"`
	Models      []string `json:"models,omitempty"`
	Project     string   `json:"project,omitempty"`
	Created     string   `json:"created"`
	Updated     string   `json:"updated"`
	Messages    int      `json:"messages"`
	Chunks      int      `json:"chunks"`
	Bytes       int64    `json:"bytes"`
	Version     int64    `json:"version"`
	Transcript  string   `json:"transcript"`
	HeadMessage string   `json:"headMessage,omitempty"`
	Memories    []string `json:"memories,omitempty"`
	Archived    bool     `json:"archived,omitempty"`
}

// scheme addresses a record wherever it is read from, over the protocol or
// here.
const scheme = "sennit://"

func uriOf(kind record.Kind, id record.ID) string { return scheme + string(kind) + "/" + id.String() }

// snippetBytes is how much of a record a result carries before it is cut, the
// same measure the protocol answer uses.
const snippetBytes = 320

// recallJSON is one search as JSON, every result carried in full.
func recallJSON(result recall.Result, hits []recall.Hit) ([]byte, error) {
	out := recallOut{
		Results:          []hitOut{},
		Searched:         result.Searched,
		Considered:       result.Considered,
		LexicalHits:      result.LexicalHits,
		SupersededHidden: result.SupersededHidden,
		ScopeExcluded:    result.ScopeExcluded,
		TookMS:           (result.EmbedFor + result.SearchFor + result.FetchFor).Milliseconds(),
	}
	for _, h := range hits {
		out.Results = append(out.Results, jsonHit(h))
	}
	return json.MarshalIndent(out, "", "  ")
}

func jsonHit(h recall.Hit) hitOut {
	out := hitOut{
		URI:        uriOf(h.Kind(), h.ID()),
		Kind:       string(h.Kind()),
		Score:      h.Score,
		Similarity: h.Similarity,
		Boost:      h.Boost,
	}
	switch {
	case h.Memory != nil:
		memory := h.Memory
		out.Type, out.Tags = string(memory.Type), memory.Tags
		out.Title = firstLine(memory.Statement)
		out.Created = memory.CreatedAt.String()
		out.Snippet, out.Truncated = snippet(memory.Statement + ", " + memory.Context)
		detail := memoryDetail{
			URI: out.URI, ID: memory.ID.String(), Type: string(memory.Type),
			Statement: memory.Statement, Context: memory.Context, Tags: memory.Tags,
			Created: memory.CreatedAt.String(), Importance: memory.Importance,
			Confidence: memory.Confidence, Links: memory.Links, Tier: string(h.Tier),
		}
		if memory.Supersedes != nil {
			detail.Supersedes = uriOf(record.KindMemory, *memory.Supersedes)
		}
		out.Detail = detail
	case h.Session != nil:
		session := h.Session
		out.Tags, out.Title = session.Tags, session.Title
		out.Created = session.Created.String()
		out.Snippet, out.Truncated = snippet(session.Summary)
		detail := sessionDetail{
			URI: out.URI, ID: session.ID.String(), Title: session.Title, Summary: session.Summary,
			Tags: session.Tags, Kind: string(session.Kind), Agent: session.Agent.Name,
			Models: session.Models, Project: local.ProjectKey(session.Project),
			Created: session.Created.String(), Updated: session.Updated.String(),
			Messages: session.Counts.Messages, Chunks: len(session.Chunks), Bytes: session.Counts.Bytes,
			Version: session.Version, Transcript: out.URI + "/transcript",
			HeadMessage: session.HeadMessage, Archived: session.Archived,
		}
		for _, memory := range session.Links.Memories {
			detail.Memories = append(detail.Memories, uriOf(record.KindMemory, memory))
		}
		out.Detail = detail
	}
	return out
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

// snippet cuts on a rune boundary, and then on a word boundary if one is near,
// so a snippet never ends inside a character or halfway through a word.
func snippet(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if len(text) <= snippetBytes {
		return text, false
	}
	cut := snippetBytes
	for cut > 0 && !isBoundary(text[cut]) {
		cut--
	}
	if cut < snippetBytes/2 {
		cut = snippetBytes
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
	}
	return strings.TrimSpace(text[:cut]) + "…", true
}

func isBoundary(b byte) bool { return b == ' ' || b == '\n' || b == '\t' }
