package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// ErrNoRecord reports that the vault holds nothing at an address.
var ErrNoRecord = errors.New("no record at that address")

// A Resolution is one address's content, rendered once.
//
// It is the whole of I2. The `open` tool and every `resources/read` handler are
// two doors into one namespace, and the invariant is that they cannot disagree
// about what is behind an address, not by convention, but because there is one
// function that turns an address into bytes and both of them call it. Anything
// that renders a record renders this value; nothing downstream is allowed a
// second opinion.
type Resolution struct {
	Address Address
	// Name and Title are what to call the thing.
	Name  string
	Title string
	// MIMEType describes Body.
	MIMEType string
	// Body is the content. Both entry points return exactly these bytes.
	Body string
	// Detail is the same content as a typed value, for structuredContent. It
	// carries no field Body does not, so a caller reading either one is reading
	// the same answer.
	Detail any
	// Links are the addresses this content points at, so a caller can navigate
	// without constructing a URI itself.
	Links []Link
}

// A Link is another address worth following from this one.
type Link struct {
	URI         string `json:"uri" jsonschema:"the address to open"`
	Name        string `json:"name" jsonschema:"a short name for the target"`
	Description string `json:"description,omitempty" jsonschema:"why this link is here"`
	MIMEType    string `json:"mimeType,omitempty" jsonschema:"the type of what the address returns"`
}

// ResolveOptions narrow what a resolution returns.
//
// They only ever select a subrange of the same content, a page of a transcript
// rather than all of it. Nothing here changes how anything is rendered, which is
// what keeps the two entry points identical when neither asks for anything.
type ResolveOptions struct {
	// From starts a transcript at a message id, and Limit caps how many
	// messages come back.
	From  string
	Limit int
	// IncludeSubagents inlines a session's delegated runs rather than naming
	// them.
	IncludeSubagents bool
}

// Resolve turns an address into its content.
//
// Every read in this package ends here: the `open` tool, `resources/read`, and
// the resume prompt's embedded resources alike.
func (s *Server) Resolve(ctx context.Context, uri string, opts ResolveOptions) (Resolution, error) {
	address, err := Parse(uri)
	if err != nil {
		return Resolution{}, err
	}
	return s.resolve(ctx, address, opts)
}

func (s *Server) resolve(ctx context.Context, address Address, opts ResolveOptions) (Resolution, error) {
	switch address.Form {
	case FormGuide:
		// The guide resolves whether or not a vault opened. It is the one thing
		// a caller that cannot reach its vault still needs.
		return Resolution{
			Address:  address,
			Name:     "guide",
			Title:    "How to use this vault",
			MIMEType: "text/markdown",
			Body:     Guide,
			Detail:   GuideDetail{Guide: Guide},
			Links:    []Link{{URI: VaultURI, Name: "vault", Description: "what this vault holds right now"}},
		}, nil
	case FormVault:
		return s.resolveVault(ctx, address)
	}

	if err := s.ready(); err != nil {
		return Resolution{}, err
	}
	switch address.Form {
	case FormMemory:
		return s.resolveMemory(ctx, address)
	case FormSession:
		return s.resolveSession(ctx, address, opts)
	case FormTranscript:
		return s.resolveTranscript(ctx, address, opts)
	default:
		return Resolution{}, fmt.Errorf("%w: %s", ErrNoRecord, address.URI)
	}
}

// GuideDetail is the guide as structured content.
type GuideDetail struct {
	Guide string `json:"guide" jsonschema:"how to use this vault, in markdown"`
}

// VaultDetail is what the vault holds and what it owes the network.
type VaultDetail struct {
	Records   int    `json:"records" jsonschema:"how many records this device holds metadata for"`
	Memories  int    `json:"memories" jsonschema:"how many of them are memories"`
	Sessions  int    `json:"sessions" jsonschema:"how many are stored conversations"`
	Pending   int    `json:"pending" jsonschema:"records durable on this device but not yet on the network"`
	Indexed   int    `json:"indexed" jsonschema:"how many records are searchable by meaning"`
	Model     string `json:"model" jsonschema:"the embedding model the index was built with"`
	Unindexed int    `json:"unindexed,omitempty" jsonschema:"vectors from another model, which cannot be searched"`
	Online    bool   `json:"online" jsonschema:"whether the vault can reach its indexer"`
	Indexer   string `json:"indexer,omitempty" jsonschema:"which indexer it is connected to"`
	// Tags is the vault's own vocabulary, most used first. It is here because a
	// filter matches tags exactly, so an agent coining a synonym for a tag the
	// vault already uses splits the records that should have been preferred
	// together.
	Tags []TagUse `json:"tags,omitempty" jsonschema:"the tags this vault already uses, most used first"`
	// Ready is empty when the vault is usable and says what to do when it is not.
	Ready string `json:"ready,omitempty" jsonschema:"what stands between this server and a usable vault"`
}

// A TagUse is one tag and how much of the vault carries it.
type TagUse struct {
	Tag     string `json:"tag" jsonschema:"the tag"`
	Records int    `json:"records" jsonschema:"how many records carry it"`
	// TooCommon marks a tag on so much of the vault that preferring it narrows
	// nothing.
	TooCommon bool `json:"tooCommon,omitempty" jsonschema:"true when the tag is too common in this vault to narrow a search"`
}

// VocabularyLimit is how many of the vault's tags the vault resource reports.
//
// It is a context budget rather than a data limit: the point of the list is that
// an agent reuses an existing tag instead of coining a synonym, and the tags that
// do that work are the ones already in use.
const VocabularyLimit = 40

func (s *Server) resolveVault(ctx context.Context, address Address) (Resolution, error) {
	detail := VaultDetail{}
	if err := s.ready(); err != nil {
		detail.Ready = err.Error()
	} else {
		if err := s.fillVault(ctx, &detail); err != nil {
			return Resolution{}, err
		}
	}
	return Resolution{
		Address:  address,
		Name:     "vault",
		Title:    "This vault",
		MIMEType: "application/json",
		Body:     mustJSON(detail),
		Detail:   detail,
		Links:    []Link{{URI: GuideURI, Name: "guide", Description: "how to use this vault"}},
	}, nil
}

func (s *Server) fillVault(ctx context.Context, detail *VaultDetail) error {
	health := s.vault.IndexHealth()
	detail.Indexed, detail.Model, detail.Unindexed = health.Indexed, health.Model, health.Stale()
	detail.Pending = s.vault.Pending()
	detail.Online, detail.Indexer = s.vault.Online(), s.vault.Indexer()

	// Counted from the ranking metadata rather than from the catalog, because
	// the catalog names what has reached the network and a record queued for the
	// next flush is held on this device just as much as one that has.
	counts, err := s.vault.CountRecords()
	if err != nil {
		return err
	}
	detail.Records = counts.Total
	detail.Memories = counts.ByKind[record.KindMemory]
	detail.Sessions = counts.ByKind[record.KindSession]

	tags, err := s.vault.TagVocabulary(VocabularyLimit)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		detail.Tags = append(detail.Tags, TagUse{Tag: tag.Tag, Records: tag.Records, TooCommon: tag.TooCommon})
	}
	_ = ctx
	return nil
}

// MemoryDetail is one memory as an address returns it.
type MemoryDetail struct {
	URI       string   `json:"uri" jsonschema:"this record's address"`
	ID        string   `json:"id" jsonschema:"this record's id"`
	Type      string   `json:"type" jsonschema:"one of fact, preference, insight, doc, profile, correction"`
	Statement string   `json:"statement" jsonschema:"the proposition"`
	Context   string   `json:"context" jsonschema:"what makes the statement resolvable on its own"`
	Tags      []string `json:"tags,omitempty" jsonschema:"the record's tags"`
	Created   string   `json:"created" jsonschema:"when the vault learned this"`
	Updated   string   `json:"updated,omitempty" jsonschema:"when this record last changed"`
	// Importance and Confidence are the writing agent's own judgement. The vault
	// runs no model and does not infer them.
	Importance float64 `json:"importance,omitempty" jsonschema:"the writing agent's judgement, 0 to 1"`
	Confidence float64 `json:"confidence,omitempty" jsonschema:"the writing agent's judgement, 0 to 1"`
	ValidFrom  string  `json:"validFrom,omitempty" jsonschema:"when the statement became true of the world"`
	ValidUntil string  `json:"validUntil,omitempty" jsonschema:"when the statement stopped being true of the world"`
	Supersedes string  `json:"supersedes,omitempty" jsonschema:"the address of the record this one replaced"`
	// Session and Span are where this memory came from, so a claim can be traced
	// back to the conversation that produced it.
	Session string   `json:"session,omitempty" jsonschema:"the address of the conversation this came from"`
	Span    string   `json:"span,omitempty" jsonschema:"the turns of that conversation it was drawn from"`
	Links   []string `json:"links,omitempty" jsonschema:"related records"`
	// Tier reports where the body was read from: this device, a cached location,
	// or the network.
	Tier string `json:"tier" jsonschema:"where the body was read from: local, cached or network"`
}

func (s *Server) resolveMemory(ctx context.Context, address Address) (Resolution, error) {
	memory, tier, err := s.vault.FetchMemory(ctx, address.ID)
	if err != nil {
		return Resolution{}, notFound(address, err)
	}
	detail := MemoryDetail{
		URI:        address.URI,
		ID:         memory.ID.String(),
		Type:       string(memory.Type),
		Statement:  memory.Statement,
		Context:    memory.Context,
		Tags:       memory.Tags,
		Created:    memory.CreatedAt.String(),
		Importance: memory.Importance,
		Confidence: memory.Confidence,
		Links:      memory.Links,
		Tier:       string(tier),
	}
	if !memory.UpdatedAt.Equal(memory.CreatedAt.Time) {
		detail.Updated = memory.UpdatedAt.String()
	}
	if memory.ValidFrom != nil {
		detail.ValidFrom = memory.ValidFrom.String()
	}
	if memory.ValidUntil != nil {
		detail.ValidUntil = memory.ValidUntil.String()
	}
	if memory.Supersedes != nil {
		detail.Supersedes = URI(record.KindMemory, *memory.Supersedes)
	}
	if memory.Source.SessionID != "" {
		if id, err := record.ParseID(memory.Source.SessionID); err == nil {
			detail.Session, detail.Span = URI(record.KindSession, id), memory.Source.Span
		}
	}

	resolution := Resolution{
		Address:  address,
		Name:     "memory " + shortID(memory.ID),
		Title:    firstLine(memory.Statement),
		MIMEType: "text/markdown",
		Body:     renderMemory(detail),
		Detail:   detail,
	}
	if detail.Session != "" {
		resolution.Links = append(resolution.Links, Link{
			URI:         detail.Session,
			Name:        "source conversation",
			Description: "the conversation this was drawn from",
		})
	}
	if detail.Supersedes != "" {
		resolution.Links = append(resolution.Links, Link{
			URI:         detail.Supersedes,
			Name:        "replaced record",
			Description: "what this record replaced, kept as history",
		})
	}
	return resolution, nil
}

// SessionDetail is one conversation's head.
type SessionDetail struct {
	URI     string   `json:"uri" jsonschema:"this conversation's address"`
	ID      string   `json:"id" jsonschema:"this conversation's id"`
	Title   string   `json:"title" jsonschema:"what the conversation is called"`
	Summary string   `json:"summary,omitempty" jsonschema:"what happened in it"`
	Tags    []string `json:"tags,omitempty" jsonschema:"the conversation's tags"`
	Kind    string   `json:"kind" jsonschema:"main, subagent or background"`
	Agent   string   `json:"agent,omitempty" jsonschema:"the client that wrote it"`
	Models  []string `json:"models,omitempty" jsonschema:"the models that spoke in it"`
	Project string   `json:"project,omitempty" jsonschema:"the repository or directory it happened in"`
	Created string   `json:"created" jsonschema:"when it started"`
	Updated string   `json:"updated" jsonschema:"when it was last appended to"`
	// Messages, Chunks and Bytes describe the transcript without reading it.
	Messages int   `json:"messages" jsonschema:"how many turns the transcript holds"`
	Chunks   int   `json:"chunks" jsonschema:"how many immutable pieces it is stored in"`
	Bytes    int64 `json:"bytes" jsonschema:"the transcript's size"`
	Version  int64 `json:"version" jsonschema:"how many times the head has been revised"`
	// Transcript is where the messages are. The head deliberately does not carry
	// them: a head measured at about a kilobyte can name hundreds of kilobytes
	// of conversation, and everything that lists or previews wants the first.
	Transcript string `json:"transcript" jsonschema:"the address of this conversation's messages"`
	// HeadMessage is the leaf of the message graph: where a resume continues
	// from.
	HeadMessage string `json:"headMessage,omitempty" jsonschema:"the id of the last turn"`
	// Memories are the records drawn out of this conversation.
	Memories []string `json:"memories,omitempty" jsonschema:"addresses of memories drawn from this conversation"`
	// Subagents are the runs it delegated, named rather than inlined unless the
	// caller asked otherwise.
	Subagents []SubagentDetail `json:"subagents,omitempty" jsonschema:"delegated runs, as references"`
	// Archived marks a conversation the user has put away.
	Archived bool `json:"archived,omitempty" jsonschema:"true when the conversation has been archived"`
}

// A SubagentDetail names a delegated run without fetching it.
type SubagentDetail struct {
	URI      string `json:"uri" jsonschema:"the delegated run's address"`
	Title    string `json:"title" jsonschema:"what it was called"`
	Kind     string `json:"kind" jsonschema:"subagent or background"`
	Messages int    `json:"messages" jsonschema:"how many turns it holds"`
}

func (s *Server) resolveSession(ctx context.Context, address Address, opts ResolveOptions) (Resolution, error) {
	loaded, err := s.vault.LoadSession(ctx, vault.LoadSessionRequest{
		ID:               address.ID,
		IncludeSubagents: opts.IncludeSubagents,
	})
	if err != nil {
		return Resolution{}, notFound(address, err)
	}
	detail := sessionDetail(loaded)

	resolution := Resolution{
		Address:  address,
		Name:     "session " + shortID(loaded.Session.ID),
		Title:    loaded.Session.Title,
		MIMEType: "application/json",
		Body:     mustJSON(detail),
		Detail:   detail,
		Links: []Link{{
			URI:         detail.Transcript,
			Name:        "transcript",
			Description: fmt.Sprintf("the %d turns of this conversation", detail.Messages),
		}},
	}
	for _, memory := range detail.Memories {
		resolution.Links = append(resolution.Links, Link{
			URI: memory, Name: "memory", Description: "drawn from this conversation",
		})
	}
	for _, subagent := range detail.Subagents {
		resolution.Links = append(resolution.Links, Link{
			URI: subagent.URI, Name: subagent.Title, Description: "a run this conversation delegated",
		})
	}
	return resolution, nil
}

func sessionDetail(loaded vault.LoadedSession) SessionDetail {
	session := loaded.Session
	detail := SessionDetail{
		URI:         URI(record.KindSession, session.ID),
		ID:          session.ID.String(),
		Title:       session.Title,
		Summary:     session.Summary,
		Tags:        session.Tags,
		Kind:        string(session.Kind),
		Agent:       session.Agent.Name,
		Models:      session.Models,
		Project:     local.ProjectKey(session.Project),
		Created:     session.Created.String(),
		Updated:     session.Updated.String(),
		Messages:    session.Counts.Messages,
		Chunks:      len(session.Chunks),
		Bytes:       session.Counts.Bytes,
		Version:     session.Version,
		Transcript:  TranscriptURI(session.ID),
		HeadMessage: session.HeadMessage,
		Archived:    session.Archived,
	}
	for _, memory := range session.Links.Memories {
		detail.Memories = append(detail.Memories, URI(record.KindMemory, memory))
	}
	for _, subagent := range loaded.Subagents {
		detail.Subagents = append(detail.Subagents, SubagentDetail{
			URI:      URI(record.KindSession, subagent.ID),
			Title:    subagent.Title,
			Kind:     string(subagent.Kind),
			Messages: subagent.Messages,
		})
	}
	return detail
}

// TranscriptDetail is a conversation's messages, or a page of them.
type TranscriptDetail struct {
	URI     string `json:"uri" jsonschema:"this transcript's address"`
	Session string `json:"session" jsonschema:"the address of the conversation it belongs to"`
	Title   string `json:"title" jsonschema:"what the conversation is called"`
	// Messages are the turns, in order, in the vault's portable schema.
	Messages []record.Message `json:"messages" jsonschema:"the turns, in order"`
	// More reports that a limit cut the transcript short, and NextFrom is the
	// message to continue from.
	More     bool   `json:"more" jsonschema:"true when a limit cut the transcript short"`
	NextFrom string `json:"nextFrom,omitempty" jsonschema:"pass this back as from to continue"`
	// Total is how many turns the whole conversation holds, so a caller reading
	// a page knows what it is a page of.
	Total int `json:"total" jsonschema:"how many turns the whole conversation holds"`
	// ChunksRead is how many immutable pieces this read fetched.
	ChunksRead int `json:"chunksRead" jsonschema:"how many stored pieces this read fetched"`
}

func (s *Server) resolveTranscript(ctx context.Context, address Address, opts ResolveOptions) (Resolution, error) {
	loaded, err := s.vault.LoadSession(ctx, vault.LoadSessionRequest{
		ID:               address.ID,
		Transcript:       true,
		From:             opts.From,
		Limit:            opts.Limit,
		IncludeSubagents: opts.IncludeSubagents,
	})
	if err != nil {
		return Resolution{}, notFound(address, err)
	}
	detail := TranscriptDetail{
		URI:        address.URI,
		Session:    URI(record.KindSession, loaded.Session.ID),
		Title:      loaded.Session.Title,
		Messages:   loaded.Messages,
		More:       loaded.More,
		NextFrom:   loaded.NextFrom,
		Total:      loaded.Session.Counts.Messages,
		ChunksRead: loaded.ChunksRead,
	}
	return Resolution{
		Address:  address,
		Name:     "transcript " + shortID(loaded.Session.ID),
		Title:    loaded.Session.Title + ", transcript",
		MIMEType: "application/json",
		Body:     mustJSON(detail),
		Detail:   detail,
		Links: []Link{{
			URI:         detail.Session,
			Name:        "conversation",
			Description: "the head this transcript belongs to",
		}},
	}, nil
}

// notFound turns a vault miss into the one error the protocol has a code for,
// and leaves anything else alone.
//
// The three misses are matched by their own sentinels rather than by their
// wording, because they come from three layers that report an absence
// differently: the session store has no such head, the device has no such body,
// and the catalog has no such entry. All three mean the same thing to a caller,
// the address returns nothing, and a caller cannot act on which layer noticed.
// Anything else is a real failure and is passed through, so a broken indexer
// never reads as an empty vault.
func notFound(address Address, err error) error {
	if errors.Is(err, vault.ErrNoSession) || errors.Is(err, local.ErrNotFound) ||
		errors.Is(err, manifest.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNoRecord, address.URI)
	}
	return err
}

func mustJSON(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(encoded)
}

// shortID is an id abbreviated for a human-readable name. Addresses always carry
// the whole thing; this is only ever a label.
func shortID(id record.ID) string {
	text := id.String()
	if len(text) > 12 {
		return text[:12]
	}
	return text
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}
