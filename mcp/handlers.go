package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// SnippetBytes is how much of a record a concise result carries.
//
// Enough to decide whether to open it and not enough to make a page of results
// cost what the records themselves would. Truncation is reported rather than
// silent: a model that cannot tell a snippet from a whole record will answer
// from half a statement.
const SnippetBytes = 320

// no is a false the SDK's pointer-shaped hints can take the address of.
var no = false

func readOnly() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &no}
}

// writes describes a tool that adds to the vault without destroying anything.
//
// IdempotentHint is earned rather than asserted: the substrate is
// content-addressed, so writing identical content twice yields one stored blob,
// and a retry after a dropped response cannot double a record's storage. It does
// mint a second record id, which is why the tool reports the neighbours a write
// landed next to instead of claiming deduplication it does not perform.
func writes() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
}

func (s *Server) registerTools() {
	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        RecallTool.Name,
		Title:       RecallTool.Title,
		Description: RecallTool.Description,
		Annotations: readOnly(),
	}, s.recall)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        RememberTool.Name,
		Title:       RememberTool.Title,
		Description: RememberTool.Description,
		Annotations: writes(),
	}, s.remember)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        BrowseTool.Name,
		Title:       BrowseTool.Title,
		Description: BrowseTool.Description,
		Annotations: readOnly(),
	}, s.browse)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        OpenTool.Name,
		Title:       OpenTool.Title,
		Description: OpenTool.Description,
		Annotations: readOnly(),
	}, s.open)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        SaveSessionTool.Name,
		Title:       SaveSessionTool.Title,
		Description: SaveSessionTool.Description,
		Annotations: writes(),
		// The one schema on this surface that is not inferred. A message's
		// content is a list of parts and a tool result carries parts of its own,
		// and inference refuses a recursive type rather than emitting the `$ref`
		// that expresses it. See SaveSessionSchema.
		InputSchema: SaveSessionSchema,
	}, s.saveSession)

	sdk.AddTool(s.sdk, &sdk.Tool{
		Name:        ForgetTool.Name,
		Title:       ForgetTool.Title,
		Description: ForgetTool.Description,
		Annotations: &sdk.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &no},
	}, s.forget)
}

// ── recall ──────────────────────────────────────────────────────────────────

// RecallIn is one search.
//
// Tags and Types prefer; Scope excludes. They are separate fields because they
// are separate mechanisms, and a single field with a strictness flag would put
// the whole difference behind a boolean an agent has to get right.
type RecallIn struct {
	Query             string   `json:"query" jsonschema:"the user's question, in their own words; not keywords"`
	Tags              []string `json:"tags,omitempty" jsonschema:"two or three tags you expect the answer to carry; these PREFER and never exclude, so a wrong guess costs ranking quality and never an answer"`
	Types             []string `json:"types,omitempty" jsonschema:"memory types that could answer: fact, preference, insight, doc, profile, correction; these PREFER and never exclude"`
	Scope             []string `json:"scope,omitempty" jsonschema:"which classes of record may answer at all: memory, session. This EXCLUDES. Set it only when the user named a container, never because a question's wording suggests a class; empty means both"`
	Limit             int      `json:"limit,omitempty" jsonschema:"how many results to return, default 5, maximum 100. Raise it when the user asks for a thorough search and accepts the context cost: a deeper page finds records the default ranks below the cut"`
	Cursor            string   `json:"cursor,omitempty" jsonschema:"a nextCursor from a previous call, to continue the same ranking"`
	IncludeSuperseded bool     `json:"includeSuperseded,omitempty" jsonschema:"include records a later one replaced; use when the user asks what something used to be"`
	Detail            string   `json:"detail,omitempty" jsonschema:"concise (default) returns snippets and addresses; full returns whole records"`
}

// RecallOut is a ranked answer and the evidence for it.
type RecallOut struct {
	Results    []HitOut `json:"results" jsonschema:"the ranked records, best first"`
	NextCursor string   `json:"nextCursor,omitempty" jsonschema:"pass back as cursor for more; absent means there are no more"`
	Hint       string   `json:"hint,omitempty" jsonschema:"what to try next, when the answer is empty or thin"`
	// Searched and Considered say how much of the vault the answer came out of.
	Searched   int `json:"searched" jsonschema:"how many records the query was scored against"`
	Considered int `json:"considered" jsonschema:"how many candidates were ranked"`
	// LexicalHits is how many records matched the query's actual words. Zero
	// means the ranking was by meaning alone.
	LexicalHits int `json:"lexicalHits" jsonschema:"how many records matched the query's words; zero means the ranking was by meaning alone"`
	// SupersededHidden and ScopeExcluded are the two things that can shorten an
	// answer, reported so a caller never has to infer them from a short list.
	SupersededHidden int   `json:"supersededHidden,omitempty" jsonschema:"replaced versions held back; ask for history to see them"`
	ScopeExcluded    int   `json:"scopeExcluded,omitempty" jsonschema:"candidates the scope removed; a filter can never do this"`
	TookMS           int64 `json:"tookMs" jsonschema:"how long the search took"`
}

// A HitOut is one ranked record.
type HitOut struct {
	URI       string   `json:"uri" jsonschema:"the record's address; pass it to open for the full record"`
	Kind      string   `json:"kind" jsonschema:"memory or session"`
	Type      string   `json:"type,omitempty" jsonschema:"the memory type, for memories"`
	Title     string   `json:"title" jsonschema:"a memory's statement, or a conversation's title"`
	Snippet   string   `json:"snippet,omitempty" jsonschema:"the beginning of the record's content"`
	Truncated bool     `json:"truncated,omitempty" jsonschema:"true when the snippet is shorter than the record"`
	Tags      []string `json:"tags,omitempty" jsonschema:"the record's tags"`
	Created   string   `json:"created,omitempty" jsonschema:"when the vault learned this. For a conversation this device rebuilt from the network, the time of the first dated turn it was rebuilt from instead, and absent when none of those turns is dated"`
	// Score is the rank the record earned, Similarity how close it sits to the
	// query in meaning, and Boost how far the filter moved it. All three are
	// reported so a caller can see how much of a position was earned by meaning
	// and how much by matching a filter it supplied itself.
	Score      float32 `json:"score" jsonschema:"the rank score; comparable within one result set, not across runs"`
	Similarity float32 `json:"similarity" jsonschema:"cosine similarity to the query, -1 to 1; how close in meaning"`
	Boost      float32 `json:"boost,omitempty" jsonschema:"how far your filter moved this record"`
	// Detail carries the whole record when the caller asked for it.
	Detail any `json:"detail,omitempty" jsonschema:"the whole record, when detail was full"`
}

// MaxLimit bounds a page of results, so a caller asking for the vault gets a
// page and a cursor.
//
// At the ceiling the page equals recall.DefaultCandidates, so the tag and type
// boost still reorders what came back but can no longer promote a record that
// similarity did not already retrieve. That is benign, because everything
// fetched is returned, but the filter's measured gain does not apply there.
const MaxLimit = 100

func (s *Server) recall(ctx context.Context, _ *sdk.CallToolRequest, in RecallIn) (*sdk.CallToolResult, RecallOut, error) {
	if err := s.ready(); err != nil {
		return nil, RecallOut{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, RecallOut{}, errors.New("recall needs a query: pass the user's question in their own words")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = recall.DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	scope, err := parseScope(in.Scope)
	if err != nil {
		return nil, RecallOut{}, err
	}
	types, err := parseTypes(in.Types)
	if err != nil {
		return nil, RecallOut{}, err
	}
	offset, err := decodeRankCursor(in.Cursor, in.Query)
	if err != nil {
		return nil, RecallOut{}, err
	}

	result, err := s.vault.Recall(ctx, recall.Request{
		Query: in.Query,
		// The pipeline ranks a pool and returns its head, so a page is taken by
		// asking for everything up to the end of it and slicing. The ranking is
		// deterministic, a stable sort with ties broken on the record id, so
		// two pages of one unchanged vault agree. It is an offset into a ranking
		// and not a keyset, which is the honest shape for a ranked answer: a
		// record written between two pages does move the boundary, and no
		// cursor over a ranking can prevent that.
		Limit:             offset + limit,
		Candidates:        candidatePool(offset + limit),
		Scope:             scope,
		Filter:            recall.Filter{Tags: in.Tags, Types: types},
		IncludeSuperseded: in.IncludeSuperseded,
	})
	if err != nil {
		return nil, RecallOut{}, err
	}

	out := RecallOut{
		Results:          []HitOut{},
		Searched:         result.Searched,
		Considered:       result.Considered,
		LexicalHits:      result.LexicalHits,
		SupersededHidden: result.SupersededHidden,
		ScopeExcluded:    result.ScopeExcluded,
		TookMS:           (result.EmbedFor + result.SearchFor + result.FetchFor).Milliseconds(),
	}
	hits := result.Hits
	if offset < len(hits) {
		hits = hits[offset:]
	} else {
		hits = nil
	}
	full := in.Detail == "full"
	for i := range hits {
		out.Results = append(out.Results, s.hit(hits[i], full))
	}
	if len(out.Results) == limit && len(result.Hits) == offset+limit {
		out.NextCursor = encodeRankCursor(offset+limit, in.Query)
	}
	if out.Hint, err = s.recallHint(in, out); err != nil {
		return nil, RecallOut{}, err
	}

	return &sdk.CallToolResult{Content: withLinks(renderRecall(in, out), out.Results)}, out, nil
}

// candidatePool keeps the pool deep enough that the filter has something to
// reorder.
//
// A boost can only move a record similarity already retrieved, so a pool the
// size of the answer would make filtering decorative, the records it exists to
// promote would never have been fetched. This is the depth the measured gain was
// established at, and a deeper page raises it rather than lowering it.
func candidatePool(want int) int {
	if want > recall.DefaultCandidates {
		return want
	}
	return recall.DefaultCandidates
}

// recallHint says what to try next when an answer is empty or shortened.
//
// Returning nothing is a legitimate answer and is never an error, but an answer
// of nothing with no explanation is one a model will paper over. Naming the
// mechanism that shortened it is what lets the model correct itself rather than
// guess.
func (s *Server) recallHint(in RecallIn, out RecallOut) (string, error) {
	switch {
	case len(out.Results) > 0 && out.ScopeExcluded > 0:
		return fmt.Sprintf("The scope %s removed %d candidate(s). Scope excludes; drop it to search "+
			"every class of record.", strings.Join(in.Scope, " and "), out.ScopeExcluded), nil
	case len(out.Results) > 0:
		return "", nil
	case out.ScopeExcluded > 0:
		return fmt.Sprintf("Nothing in scope %s matched, though %d candidate(s) outside it did. "+
			"Scope excludes, drop it and search every class of record.",
			strings.Join(in.Scope, " and "), out.ScopeExcluded), nil
	case out.Searched == 0:
		return s.nothingSearchable()
	case out.SupersededHidden > 0:
		return fmt.Sprintf("Nothing current matched, though %d replaced version(s) did. "+
			"Set includeSuperseded to see what this used to be.", out.SupersededHidden), nil
	default:
		// Not a suggestion to add tags: the filter cannot have emptied this,
		// and telling a model otherwise would teach it the wrong lesson about a
		// mechanism the whole ranking rests on.
		return "The vault does not hold this. Your tags did not cause it, filters only ever prefer, " +
			"and cannot remove a record. Try broader words, or `browse` to see what is stored. " +
			"Telling the user it is not there is a better answer than the nearest record.", nil
	}
}

// nothingSearchable says why a recall had nothing on this device to search.
//
// That is an empty vault only on a device that has restored what the network
// holds of it. A device that has not restored the vault holds nothing to search
// whatever the vault holds, so it says so and names the depth of hydrate that
// makes a record searchable, which is deeper than the default.
func (s *Server) nothingSearchable() (string, error) {
	restoration, err := s.vault.Restoration()
	if err != nil {
		return "", err
	}
	switch {
	case restoration.Untouched:
		return "Nothing on this device is searchable by meaning yet, and it has not restored any record " +
			"of this vault from the network, so this does not show that the vault is empty. If this vault " +
			"was just created here, there is nothing to restore; otherwise `sennit hydrate --depth index` " +
			"restores its records from the network and makes them searchable. New ones are stored with " +
			"`remember`.", nil
	case !restoration.Complete():
		return fmt.Sprintf("Nothing on this device is searchable by meaning yet, though it knows of %s "+
			"on the network that it has not restored, and a search reads only what this device holds. "+
			"`sennit hydrate --depth index` restores this vault's records from the network and makes "+
			"them searchable.", unrestoredRecords(restoration)), nil
	}
	return "This vault holds nothing searchable yet. Use `remember` to store the first record.", nil
}

func (s *Server) hit(hit recall.Hit, full bool) HitOut {
	out := HitOut{
		Kind:       string(hit.Kind()),
		URI:        URI(hit.Kind(), hit.ID()),
		Score:      hit.Score,
		Similarity: hit.Similarity,
		Boost:      hit.Boost,
	}
	switch {
	case hit.Memory != nil:
		out.Type, out.Tags = string(hit.Memory.Type), hit.Memory.Tags
		out.Title = firstLine(hit.Memory.Statement)
		out.Created = hit.Memory.CreatedAt.String()
		out.Snippet, out.Truncated = snippet(hit.Memory.Statement + ", " + hit.Memory.Context)
		if full {
			out.Detail = memoryDetail(hit.Memory)
		}
	case hit.Session != nil:
		out.Tags, out.Title = hit.Session.Tags, hit.Session.Title
		out.Created = knownTime(hit.Session.Created)
		out.Snippet, out.Truncated = snippet(hit.Session.Summary)
		if full {
			out.Detail = sessionDetail(vault.LoadedSession{Session: hit.Session})
		}
	}
	return out
}

// ── remember ────────────────────────────────────────────────────────────────

// RememberIn is one memory to store.
type RememberIn struct {
	Statement  string   `json:"statement" jsonschema:"one self-contained proposition, in a single sentence, that will still make sense to someone who never saw this conversation"`
	Context    string   `json:"context" jsonschema:"what makes the statement resolvable on its own: where it came from, what was being decided, what it contrasts with. Required, and the single largest thing deciding whether this record is ever found again"`
	Type       string   `json:"type" jsonschema:"exactly one of fact, preference, insight, doc, profile, correction"`
	Tags       []string `json:"tags" jsonschema:"two to four lowercase tags, no spaces. Prefer the specific over the general and reuse the vault's existing tags rather than coining synonyms; read sennit://vault for the vocabulary"`
	Supersedes string   `json:"supersedes,omitempty" jsonschema:"the address of a record this one replaces because the world changed. The old record is kept as history. Use a correction type instead when something was never true"`
	Links      []string `json:"links,omitempty" jsonschema:"addresses of related records, for provenance and navigation"`
	Importance float64  `json:"importance,omitempty" jsonschema:"your own judgement, 0 to 1; the vault runs no model and will not infer it"`
	Confidence float64  `json:"confidence,omitempty" jsonschema:"your own judgement, 0 to 1"`
	ValidFrom  string   `json:"validFrom,omitempty" jsonschema:"when the statement became true OF THE WORLD, as distinct from when you learned it; RFC 3339"`
	ValidUntil string   `json:"validUntil,omitempty" jsonschema:"when the statement stopped being true of the world; RFC 3339"`
	Session    string   `json:"session,omitempty" jsonschema:"EXPECTED. The address of the conversation this was drawn from. The statement above is a rewrite, not what the user said, and a rewrite silently loses detail: a dropped date makes a record that still looks right and can no longer answer. This address is the way back to the words themselves, so supply it whenever the memory came from a conversation you have saved"`
	Span       string   `json:"span,omitempty" jsonschema:"EXPECTED with session. Which turns of that conversation, as first..last message ids, so the trail leads to the exact exchange rather than the whole transcript"`
	Keywords   []string `json:"keywords,omitempty" jsonschema:"extra words this record should be findable by, beyond the ones in the statement"`
}

// RememberOut is what was stored, and the evidence for what to do next.
type RememberOut struct {
	URI    string `json:"uri" jsonschema:"the new record's address"`
	ID     string `json:"id" jsonschema:"the new record's id"`
	Stored string `json:"stored" jsonschema:"when it was stored"`
	// OnNetwork is false while a record is durable on this device alone.
	OnNetwork  bool   `json:"onNetwork" jsonschema:"whether the record has reached the network yet"`
	Durability string `json:"durability" jsonschema:"where the record is right now, in words you may repeat to the user"`
	// Neighbours are the closest existing records, and Conflicts the subset near
	// enough to be the same statement again. The vault runs no model and does
	// not decide which; it reports what it found.
	Neighbours    []NeighbourOut `json:"neighbours,omitempty" jsonschema:"the closest existing records; read them"`
	Conflicts     []NeighbourOut `json:"conflicts,omitempty" jsonschema:"neighbours close enough to be this statement again. Decide: leave both, or supersede the old one"`
	Tags          []TagOut       `json:"tags,omitempty" jsonschema:"how well each tag you used separates this record from the rest of the vault"`
	Advice        string         `json:"advice,omitempty" jsonschema:"what is worth acting on before the next write"`
	LinkedSession string         `json:"linkedSession,omitempty" jsonschema:"the conversation this memory was recorded against, now linked in both directions"`
}

// A NeighbourOut is an existing record close to the one just written.
type NeighbourOut struct {
	URI        string   `json:"uri" jsonschema:"the neighbour's address"`
	Statement  string   `json:"statement,omitempty" jsonschema:"what it says"`
	Type       string   `json:"type,omitempty" jsonschema:"its type"`
	Tags       []string `json:"tags,omitempty" jsonschema:"its tags"`
	Similarity float32  `json:"similarity" jsonschema:"how close it is to what you just wrote, 0 to 1"`
	Conflict   bool     `json:"conflict,omitempty" jsonschema:"true when it is close enough to be the same statement again"`
}

// A TagOut reports how much one tag narrows a search of this vault.
type TagOut struct {
	Tag       string  `json:"tag" jsonschema:"the tag"`
	Records   int     `json:"records" jsonschema:"how many records already carry it"`
	Share     float64 `json:"share" jsonschema:"that as a fraction of the vault"`
	TooCommon bool    `json:"tooCommon,omitempty" jsonschema:"true when the tag sits on too much of this vault to narrow anything"`
	New       bool    `json:"new,omitempty" jsonschema:"true when no record carries it yet; that is how a vocabulary grows, and also what a typo looks like"`
}

func (s *Server) remember(ctx context.Context, _ *sdk.CallToolRequest, in RememberIn) (*sdk.CallToolResult, RememberOut, error) {
	if err := s.ready(); err != nil {
		return nil, RememberOut{}, err
	}
	memoryType := record.Type(strings.TrimSpace(in.Type))
	if !memoryType.Valid() {
		return nil, RememberOut{}, fmt.Errorf("type %q is not one this vault stores; use one of %s",
			in.Type, strings.Join(record.TypeNames(), ", "))
	}

	req := vault.RememberRequest{
		Statement:  in.Statement,
		Context:    in.Context,
		Type:       memoryType,
		Tags:       in.Tags,
		Keywords:   in.Keywords,
		Links:      in.Links,
		Importance: in.Importance,
		Confidence: in.Confidence,
	}
	if in.Supersedes != "" {
		id, err := addressOf(in.Supersedes, FormMemory)
		if err != nil {
			return nil, RememberOut{}, fmt.Errorf("supersedes: %w", err)
		}
		req.Supersedes = &id
	}
	// A span is a range inside a session, so it cannot stand alone. It used to
	// be read only inside this branch, which meant a caller that sent a span and
	// no session lost it with no error and no warning.
	if in.Session == "" && strings.TrimSpace(in.Span) != "" {
		return nil, RememberOut{}, errors.New(
			"span names turns inside a conversation, so it needs the session it belongs to: pass session too, or drop span")
	}
	if in.Session != "" {
		id, err := addressOf(in.Session, FormSession)
		if err != nil {
			return nil, RememberOut{}, fmt.Errorf("session: %w", err)
		}
		req.Source = record.Source{SessionID: id.String(), Span: in.Span, Client: Name}
	}
	var err error
	if req.ValidFrom, err = optionalTime(in.ValidFrom, "validFrom"); err != nil {
		return nil, RememberOut{}, err
	}
	if req.ValidUntil, err = optionalTime(in.ValidUntil, "validUntil"); err != nil {
		return nil, RememberOut{}, err
	}

	stored, err := s.vault.Remember(ctx, req)
	if err != nil {
		return nil, RememberOut{}, err
	}

	out := RememberOut{
		URI:        URI(record.KindMemory, stored.ID),
		ID:         stored.ID.String(),
		Stored:     stored.Stored.String(),
		OnNetwork:  stored.OnNetwork,
		Durability: durability(stored.OnNetwork),
	}
	for _, neighbour := range stored.Neighbours {
		out.Neighbours = append(out.Neighbours, neighbourOut(neighbour))
	}
	for _, conflict := range stored.Conflicts {
		out.Conflicts = append(out.Conflicts, neighbourOut(conflict))
	}
	for _, tag := range stored.Tags.Tags {
		out.Tags = append(out.Tags, TagOut{
			Tag: tag.Tag, Records: tag.Records, Share: tag.Share,
			TooCommon: tag.TooCommon, New: tag.New,
		})
	}
	out.Advice = writeAdvice(stored)
	if stored.LinkedSession != nil {
		out.LinkedSession = URI(record.KindSession, *stored.LinkedSession)
	}

	return &sdk.CallToolResult{Content: withLinks(renderRemember(out), []HitOut{{URI: out.URI,
		Kind: string(record.KindMemory), Title: firstLine(in.Statement)}})}, out, nil
}

func neighbourOut(neighbour vault.Neighbour) NeighbourOut {
	return NeighbourOut{
		URI:        URI(record.KindMemory, neighbour.ID),
		Statement:  neighbour.Statement,
		Type:       string(neighbour.Type),
		Tags:       neighbour.Tags,
		Similarity: neighbour.Similarity,
		Conflict:   neighbour.Conflict,
	}
}

// durability says where a record is in words the model may repeat.
//
// It is a sentence rather than a flag because the flag is the thing that gets
// dropped: an interface that reports `onNetwork: false` and nothing else invites
// a summary of "saved to Sia", which is the one claim this project must not make
// before it is true.
func durability(onNetwork bool) string {
	if onNetwork {
		return "Stored on this device and written to the network."
	}
	return "Stored on this device. It reaches the network on the vault's ordinary schedule, " +
		"within the hour. Do not tell the user it is on the network yet."
}

func writeAdvice(stored vault.RememberResult) string {
	var advice []string
	if len(stored.Conflicts) > 0 {
		advice = append(advice, fmt.Sprintf("%d existing record(s) are close enough to be this "+
			"statement again. Read them: leave both if they are genuinely different, or write this "+
			"again with `supersedes` set if it replaces one.", len(stored.Conflicts)))
	}
	if stored.Tags.NeedsNarrowerTags() {
		advice = append(advice, "None of these tags narrows a search of this vault, each sits on "+
			"too much of it. Use more specific ones on the next write; this record cannot be retagged.")
	}
	return strings.Join(advice, " ")
}

// ── browse ──────────────────────────────────────────────────────────────────

// BrowseIn lists by metadata. Every field here excludes.
type BrowseIn struct {
	Kinds             []string `json:"kinds,omitempty" jsonschema:"which classes to list: memory, session. Omit for both. This EXCLUDES"`
	Types             []string `json:"types,omitempty" jsonschema:"memory types to list. This EXCLUDES: a record of another type will not appear"`
	Tags              []string `json:"tags,omitempty" jsonschema:"every listed tag is REQUIRED. A record carrying two of your three tags will not appear. Unlike recall, this excludes"`
	IncludeSuperseded bool     `json:"includeSuperseded,omitempty" jsonschema:"include records a later one replaced"`
	Limit             int      `json:"limit,omitempty" jsonschema:"how many rows, default 20, maximum 200"`
	Cursor            string   `json:"cursor,omitempty" jsonschema:"a nextCursor from a previous call; opaque, pass it back verbatim"`
}

// BrowseOut is one page of a listing.
type BrowseOut struct {
	Rows       []RowOut `json:"rows" jsonschema:"the records, newest first"`
	NextCursor string   `json:"nextCursor,omitempty" jsonschema:"pass back as cursor for the next page; absent means you have seen everything"`
	Hint       string   `json:"hint,omitempty" jsonschema:"what to try next, when the page is empty"`
}

// A RowOut is one record in a listing.
type RowOut struct {
	URI        string   `json:"uri" jsonschema:"the record's address; pass it to open"`
	Kind       string   `json:"kind" jsonschema:"memory or session"`
	Type       string   `json:"type,omitempty" jsonschema:"the memory type, for memories"`
	Label      string   `json:"label" jsonschema:"a memory's statement or a conversation's title; empty when this device no longer holds the body"`
	Tags       []string `json:"tags,omitempty" jsonschema:"the record's tags"`
	Created    string   `json:"created" jsonschema:"when the vault learned this"`
	Superseded bool     `json:"superseded,omitempty" jsonschema:"true when a later record replaced this one"`
}

func (s *Server) browse(_ context.Context, _ *sdk.CallToolRequest, in BrowseIn) (*sdk.CallToolResult, BrowseOut, error) {
	if err := s.ready(); err != nil {
		return nil, BrowseOut{}, err
	}
	kinds, err := parseScope(in.Kinds)
	if err != nil {
		return nil, BrowseOut{}, err
	}
	types, err := parseTypes(in.Types)
	if err != nil {
		return nil, BrowseOut{}, err
	}

	req := vault.BrowseRequest{
		Kinds:             kinds,
		Types:             types,
		Tags:              in.Tags,
		IncludeSuperseded: in.IncludeSuperseded,
		Limit:             in.Limit,
		Cursor:            local.Cursor(in.Cursor),
	}
	page, err := s.vault.Browse(req)
	if err != nil {
		return nil, BrowseOut{}, err
	}

	out := BrowseOut{Rows: []RowOut{}, NextCursor: string(page.NextCursor)}
	links := make([]HitOut, 0, len(page.Rows))
	for _, row := range page.Rows {
		out.Rows = append(out.Rows, RowOut{
			URI:        URI(row.Kind, row.ID),
			Kind:       string(row.Kind),
			Type:       string(row.Type),
			Label:      row.Label,
			Tags:       row.Tags,
			Created:    row.Created.String(),
			Superseded: row.Superseded,
		})
		links = append(links, HitOut{URI: URI(row.Kind, row.ID), Kind: string(row.Kind), Title: row.Label})
	}
	if len(out.Rows) == 0 {
		if out.Hint, err = s.browseHint(req); err != nil {
			return nil, BrowseOut{}, err
		}
	}
	return &sdk.CallToolResult{Content: withLinks(renderBrowse(out), links)}, out, nil
}

// browseHint says why a page came back empty.
//
// It names what emptied this page and nothing else. A filter is the usual
// cause, and the one the tool's description warns about, but it is a cause only
// when one was set, and only the filters that were set are named. A hint that
// blames a filter nobody set sends the model to `recall` or to an apology about
// tags, when the cause is somewhere else: an empty vault, a cursor at the end of
// the listing, or replaced records held back past it.
func (s *Server) browseHint(req vault.BrowseRequest) (string, error) {
	filters := filtersOf(req)
	switch {
	case req.Cursor == "" && filters.set():
		// A record the filters select may have been replaced, but the record at
		// the end of its chain of replacements is current and would be listed
		// had the filters not excluded it, so on a first page they are the
		// cause whatever else holds a record back.
		return filters.excludedEverything(), nil
	case req.Cursor == "":
		// Supersession cannot empty a first page. Remember refuses to replace a
		// record the vault does not already hold, so every chain of replacements
		// ends in a record nothing has replaced, and that one is listed. With no
		// filter and no cursor nothing else holds a record back, so an empty
		// first page is a device holding nothing, which is an empty vault only
		// once the device has restored what the network holds of it.
		return s.nothingListed()
	}
	// A page after a cursor lists only what comes after it, so an empty one says
	// nothing about the records before it. What can lie past the cursor unlisted
	// is replaced records, held back by default, and asking for the same page
	// with history included tells a listing that has ended from a page of them.
	//
	// With filters set these are still the only two cases: nothing past the
	// cursor matches them, so their listing has ended, or everything that does
	// has been replaced. Neither is a reason to drop a filter, so the filters
	// are not blamed here. If they exclude everything, the first page the hint
	// sends the model back to says so. The probe keeps them, because without
	// them it would count a record they exclude, current or replaced, and point
	// at a page that would still list nothing.
	if !req.IncludeSuperseded {
		probe := req
		probe.IncludeSuperseded, probe.Limit = true, 1
		history, err := s.vault.Browse(probe)
		if err != nil {
			return "", err
		}
		if len(history.Rows) > 0 {
			return filters.replacedPastCursor(), nil
		}
	}
	return filters.listingEnded(), nil
}

// nothingListed says why an unfiltered first page listed nothing: this device
// holds nothing, and whether that shows the vault holds nothing turns on what
// this device has restored.
func (s *Server) nothingListed() (string, error) {
	restoration, err := s.vault.Restoration()
	if err != nil {
		return "", err
	}
	switch {
	case restoration.Untouched:
		return "This device holds nothing to list yet, and it has not restored any record of this vault " +
			"from the network, so this does not show that the vault is empty. If this vault was just " +
			"created here, there is nothing to restore; otherwise `sennit hydrate` restores its records " +
			"from the network. New ones arrive through `remember` and `save_session`.", nil
	case !restoration.Complete():
		return fmt.Sprintf("This device holds nothing to list yet, though it knows of %s on the network "+
			"that it has not restored, and a listing shows only what this device holds. `sennit hydrate` "+
			"restores this vault's records from the network.", unrestoredRecords(restoration)), nil
	}
	return "This vault holds nothing to list yet. Records arrive through `remember` and " +
		"`save_session`.", nil
}

// browseFilters are the parts of a listing's query that could have excluded a
// record: kinds, types and tags. includeSuperseded only widens a listing, and a
// limit or a cursor shapes a page without selecting what the listing holds.
//
// They are read from the query that ran rather than from the arguments as they
// were sent. A blank tag is dropped before the query is built, and a kinds list
// naming both classes excludes nothing because the vault holds no third class,
// so neither is a filter an empty page can be blamed on, and a hint names
// neither.
type browseFilters struct {
	kinds []record.Kind
	types []record.Type
	tags  []string
}

// filtersOf reads the filters from the query that ran.
func filtersOf(req vault.BrowseRequest) browseFilters {
	filters := browseFilters{types: req.Types, tags: local.NormalizeTags(req.Tags)}
	if !(slices.Contains(req.Kinds, record.KindMemory) && slices.Contains(req.Kinds, record.KindSession)) {
		filters.kinds = req.Kinds
	}
	return filters
}

// set reports whether any filter ran.
func (f browseFilters) set() bool {
	return len(f.kinds) > 0 || len(f.types) > 0 || len(f.tags) > 0
}

// String names each filter that ran with its values, in the argument's own
// name, as `kinds ["session"] and tags ["west", "gauge"]`. A tag is given as the
// vault compared it, lowercased and trimmed, which is also how the vault's own
// tag list gives it.
func (f browseFilters) String() string {
	var named []string
	if len(f.kinds) > 0 {
		named = append(named, "kinds "+quotedList(f.kinds))
	}
	if len(f.types) > 0 {
		named = append(named, "types "+quotedList(f.types))
	}
	if len(f.tags) > 0 {
		named = append(named, "tags "+quotedList(f.tags))
	}
	return joinList(named, "and")
}

// excludedEverything is the hint for a first page the filters emptied.
//
// It says to drop only what was set, and in the form that widens the listing.
// Every tag is required, so dropping one widens it. Types are alternatives, so
// dropping one of several would narrow it further, and the advice is to drop
// the argument. The pointer to `recall` is given only for tags and types, the
// filters recall treats as a preference. Its scope excludes a class as kinds
// does, so a page emptied by kinds alone is not sent there.
func (f browseFilters) excludedEverything() string {
	var drop, soft []string
	if len(f.kinds) > 0 {
		drop = append(drop, "kinds")
	}
	switch {
	case len(f.types) == 1:
		drop, soft = append(drop, "the type"), append(soft, "types")
	case len(f.types) > 1:
		drop, soft = append(drop, "types"), append(soft, "types")
	}
	switch {
	case len(f.tags) == 1:
		drop, soft = append(drop, "the tag"), append(soft, "tags")
	case len(f.tags) > 1:
		drop, soft = append(drop, "a tag"), append(soft, "tags")
	}
	advice := "Drop kinds to list every class of record."
	if len(soft) > 0 {
		advice = fmt.Sprintf("Drop %s, or use `recall` with the same %s, which only prefer there and "+
			"cannot empty a result.", joinList(drop, "or"), joinList(soft, "and"))
	}
	return fmt.Sprintf("Nothing is listed with %s set. Every filter here EXCLUDES, so this is not "+
		"evidence the vault holds nothing related. %s", f, advice)
}

// replacedPastCursor is the hint for a page past a cursor where everything the
// filters select has been replaced.
func (f browseFilters) replacedPastCursor() string {
	if !f.set() {
		return "Every record past this cursor has been replaced by a newer one, and replaced " +
			"records are held back by default. Pass the same cursor with includeSuperseded set " +
			"to list them, or call `browse` without a cursor to start again from the newest."
	}
	// The filters go back with the cursor, because the replaced records are
	// the ones they select and without them the page would list others.
	return fmt.Sprintf("Every record past this cursor that matches %s has been replaced by a newer "+
		"one, and replaced records are held back by default. Pass the same cursor and filters with "+
		"includeSuperseded set to list them, or call `browse` without a cursor to start again from "+
		"the newest.", f)
}

// listingEnded is the hint for a page past a cursor with nothing the filters
// select beyond it.
func (f browseFilters) listingEnded() string {
	if !f.set() {
		return "Nothing lies past this cursor, so the listing ends here. An empty page after a cursor " +
			"says nothing about the records before it: call `browse` without a cursor to start again " +
			"from the newest."
	}
	// Records the filters exclude may lie past the cursor, so it is the
	// filtered listing that has ended and the sentence says which one.
	return fmt.Sprintf("Nothing past this cursor matches %s, so the listing ends here. An empty page "+
		"after a cursor says nothing about the records before it: call `browse` without a cursor to "+
		"start again from the newest.", f)
}

// quotedList renders values as a list the model can read back as arguments.
func quotedList[T ~string](values []T) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(string(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// joinList joins items as a sentence does, "a, b and c".
func joinList(items []string, conjunction string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conjunction + " " + items[len(items)-1]
}

// ── open ────────────────────────────────────────────────────────────────────

// OpenIn reads one address.
type OpenIn struct {
	URI              string `json:"uri" jsonschema:"the address to read, from a previous result. Never assemble one yourself"`
	From             string `json:"from,omitempty" jsonschema:"for a transcript, the message id to start at; use a previous nextFrom to continue"`
	Limit            int    `json:"limit,omitempty" jsonschema:"for a transcript, how many turns to return; omit for all of them"`
	IncludeSubagents bool   `json:"includeSubagents,omitempty" jsonschema:"for a conversation, inline the runs it delegated instead of naming them. Off by default: a delegated run can be as large as its parent"`
}

// OpenOut is what an address holds.
type OpenOut struct {
	URI      string `json:"uri" jsonschema:"the address that was read"`
	Kind     string `json:"kind" jsonschema:"what is at that address: vault, guide, memory, session or transcript"`
	Title    string `json:"title" jsonschema:"what to call it"`
	MIMEType string `json:"mimeType" jsonschema:"the type of the content"`
	Content  string `json:"content" jsonschema:"the content, exactly as resources/read returns it for this address"`
	Detail   any    `json:"detail,omitempty" jsonschema:"the same content as structured fields"`
	Links    []Link `json:"links,omitempty" jsonschema:"addresses worth following from here"`
}

func (s *Server) open(ctx context.Context, _ *sdk.CallToolRequest, in OpenIn) (*sdk.CallToolResult, OpenOut, error) {
	resolved, err := s.Resolve(ctx, in.URI, ResolveOptions{
		From:             in.From,
		Limit:            in.Limit,
		IncludeSubagents: in.IncludeSubagents,
	})
	if err != nil {
		if errors.Is(err, ErrNoRecord) {
			// A tool execution error rather than a protocol one: the model is
			// the party that can act on it, by finding a real address instead
			// of the one it tried.
			return nil, OpenOut{}, fmt.Errorf("%w. Use `recall` or `browse` to find an address that "+
				"exists; addresses are never assembled by hand", err)
		}
		return nil, OpenOut{}, err
	}
	out := OpenOut{
		URI:      resolved.Address.URI,
		Kind:     string(resolved.Address.Form),
		Title:    resolved.Title,
		MIMEType: resolved.MIMEType,
		Content:  resolved.Body,
		Detail:   resolved.Detail,
		Links:    resolved.Links,
	}
	content := []sdk.Content{&sdk.TextContent{Text: resolved.Body}}
	for _, link := range resolved.Links {
		content = append(content, &sdk.ResourceLink{
			URI: link.URI, Name: link.Name, Description: link.Description, MIMEType: link.MIMEType,
		})
	}
	return &sdk.CallToolResult{Content: content}, out, nil
}

// ── save_session ────────────────────────────────────────────────────────────

// SaveSessionIn is one conversation, or the next part of one.
type SaveSessionIn struct {
	Session       string           `json:"session,omitempty" jsonschema:"the address of a conversation to append to. Omit to create a new one"`
	Title         string           `json:"title,omitempty" jsonschema:"one specific line naming the conversation. Required when creating"`
	Summary       string           `json:"summary,omitempty" jsonschema:"what was decided, tried and left open. THE TRANSCRIPT IS NOT SEARCHABLE, this and the title and tags are the whole searchable surface"`
	Tags          []string         `json:"tags,omitempty" jsonschema:"two to four specific tags, reused from the vault's vocabulary where they fit"`
	Messages      []record.Message `json:"messages,omitempty" jsonschema:"the turns to store, in order. On an append, only the new ones"`
	Project       ProjectIn        `json:"project,omitzero" jsonschema:"where the conversation happened"`
	Agent         AgentIn          `json:"agent,omitzero" jsonschema:"the client writing this"`
	Models        []string         `json:"models,omitempty" jsonschema:"the models that spoke in it"`
	PreservedTail []string         `json:"preservedTail,omitempty" jsonschema:"message ids worth keeping verbatim beside the summary. Never a replacement for the transcript"`
	Durable       bool             `json:"durable,omitempty" jsonschema:"write to the network before returning instead of on the ordinary schedule. Use at the end of a long conversation, not per append: each forced write costs a whole storage block whatever it holds"`
	Archived      bool             `json:"archived,omitempty" jsonschema:"put the conversation away; it stops appearing in ordinary listings"`
}

// ProjectIn is where a conversation happened.
type ProjectIn struct {
	CWD    string `json:"cwd,omitempty" jsonschema:"the working directory"`
	Repo   string `json:"repo,omitempty" jsonschema:"the repository"`
	Branch string `json:"branch,omitempty" jsonschema:"the branch"`
}

// AgentIn is the client that produced a conversation.
type AgentIn struct {
	Name    string `json:"name,omitempty" jsonschema:"the client's name, for example claude-code"`
	Version string `json:"version,omitempty" jsonschema:"its version"`
}

// SaveSessionOut is what was written.
type SaveSessionOut struct {
	URI        string `json:"uri" jsonschema:"the conversation's address"`
	Transcript string `json:"transcript" jsonschema:"the address of its turns"`
	Version    int64  `json:"version" jsonschema:"how many times the head has been revised"`
	Messages   int    `json:"messages" jsonschema:"how many turns this call stored"`
	Bytes      int64  `json:"bytes" jsonschema:"how much transcript this call stored"`
	Chunks     int    `json:"chunks" jsonschema:"how many immutable pieces it was stored in"`
	OnNetwork  bool   `json:"onNetwork" jsonschema:"whether every piece this call wrote reached the network"`
	Durability string `json:"durability" jsonschema:"where the conversation is right now, in words you may repeat to the user"`
	Resume     string `json:"resume" jsonschema:"how the user brings this conversation back"`
}

func (s *Server) saveSession(ctx context.Context, _ *sdk.CallToolRequest, in SaveSessionIn) (*sdk.CallToolResult, SaveSessionOut, error) {
	if err := s.ready(); err != nil {
		return nil, SaveSessionOut{}, err
	}
	req := vault.SaveSessionRequest{
		Title:         in.Title,
		Summary:       in.Summary,
		Tags:          in.Tags,
		Messages:      in.Messages,
		Models:        in.Models,
		PreservedTail: in.PreservedTail,
		Durable:       in.Durable,
		Archived:      in.Archived,
		Project:       record.Project{CWD: in.Project.CWD, Repo: in.Project.Repo, Branch: in.Project.Branch},
		Agent:         record.Agent{Name: in.Agent.Name, Version: in.Agent.Version},
	}
	if in.Session != "" {
		id, err := addressOf(in.Session, FormSession)
		if err != nil {
			return nil, SaveSessionOut{}, fmt.Errorf("session: %w", err)
		}
		req.ID = id
	}

	saved, err := s.vault.SaveSession(ctx, req)
	if err != nil {
		if errors.Is(err, local.ErrStaleHead) {
			return nil, SaveSessionOut{}, fmt.Errorf("%w. Another client appended to this "+
				"conversation while you were writing. Open it again and re-send only the turns it "+
				"does not already hold", err)
		}
		return nil, SaveSessionOut{}, err
	}

	out := SaveSessionOut{
		URI:        URI(record.KindSession, saved.ID),
		Transcript: TranscriptURI(saved.ID),
		Version:    saved.Version,
		Messages:   saved.Messages,
		Bytes:      saved.Bytes,
		Chunks:     len(saved.Chunks),
		OnNetwork:  saved.OnNetwork,
		Durability: sessionDurability(saved.OnNetwork),
		Resume:     "The user can bring this conversation back in any agent with the /resume prompt.",
	}
	return &sdk.CallToolResult{Content: withLinks(renderSaveSession(out), []HitOut{
		{URI: out.URI, Kind: string(record.KindSession), Title: in.Title},
	})}, out, nil
}

func sessionDurability(onNetwork bool) string {
	if onNetwork {
		return "Stored on this device and written to the network."
	}
	return "Stored on this device. It reaches the network on the vault's ordinary schedule, within " +
		"the hour. Until then these turns exist here and nowhere else, say so rather than saying " +
		"the conversation is backed up."
}

// ── forget ──────────────────────────────────────────────────────────────────

// ForgetIn removes one record.
type ForgetIn struct {
	URI     string `json:"uri" jsonschema:"the address of the record to remove"`
	Confirm bool   `json:"confirm" jsonschema:"must be true. Ask the user first unless they have just asked you to delete this particular thing"`
}

// ForgetOut reports what was removed.
type ForgetOut struct {
	URI     string `json:"uri" jsonschema:"the address that was removed"`
	Removed bool   `json:"removed" jsonschema:"whether anything was there to remove"`
	Note    string `json:"note" jsonschema:"what this did and did not do"`
}

func (s *Server) forget(_ context.Context, _ *sdk.CallToolRequest, in ForgetIn) (*sdk.CallToolResult, ForgetOut, error) {
	if err := s.ready(); err != nil {
		return nil, ForgetOut{}, err
	}
	if !in.Confirm {
		return nil, ForgetOut{}, errors.New("forget needs confirm: true. This removes something from " +
			"a store the user owns and cannot be undone from inside the vault; ask them first")
	}
	address, err := Parse(in.URI)
	if err != nil {
		return nil, ForgetOut{}, err
	}

	out := ForgetOut{URI: address.URI, Removed: true}
	switch address.Form {
	case FormMemory:
		if err := s.vault.Forget(address.ID); err != nil {
			return nil, ForgetOut{}, err
		}
		out.Note = "The memory is gone from this vault. Storage comes back later, when nothing " +
			"written alongside it is still needed, do not report freed space."
	case FormSession, FormTranscript:
		if err := s.vault.ForgetSession(address.ID); err != nil {
			if errors.Is(err, vault.ErrNoSession) {
				// Forgetting what is already gone succeeds. A retry after a
				// dropped response must not look like a failure.
				return &sdk.CallToolResult{}, ForgetOut{URI: address.URI,
					Note: "There was nothing at that address; nothing was removed."}, nil
			}
			return nil, ForgetOut{}, err
		}
		out.Note = "The conversation and its transcript are gone from this vault. Memories drawn " +
			"from it are untouched and are still stored."
	default:
		return nil, ForgetOut{}, fmt.Errorf("%s is part of this server, not a record; there is "+
			"nothing there to forget", address.URI)
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: out.Note}}}, out, nil
}

// ── shared ──────────────────────────────────────────────────────────────────

// addressOf reads a record id out of an address of the expected form.
//
// Everything that references a record takes an address and never a bare id, so
// there is one referencing convention across the whole surface. An id on its own
// is ambiguous between classes and is exactly what a model invents when it has
// not seen one.
func addressOf(uri string, want Form) (record.ID, error) {
	address, err := Parse(uri)
	if err != nil {
		return record.ID{}, err
	}
	if address.Form != want {
		return record.ID{}, fmt.Errorf("%s addresses a %s; this wants a %s address like %s",
			uri, address.Form, want, templateFor(want))
	}
	return address.ID, nil
}

func templateFor(form Form) string {
	switch form {
	case FormMemory:
		return MemoryTemplate
	case FormSession:
		return SessionTemplate
	default:
		return TranscriptTemplate
	}
}

// parseScope reads the hard selector.
//
// An unrecognised class is refused rather than ignored. Everywhere else in this
// surface a value the vault does not know costs ranking quality and nothing
// else, because it is a guess; a scope is not a guess, and silently dropping one
// would answer a different question from the one that was asked.
func parseScope(values []string) ([]record.Kind, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]record.Kind, 0, len(values))
	for _, value := range values {
		kind := record.Kind(strings.TrimSpace(value))
		switch kind {
		case record.KindMemory, record.KindSession:
			out = append(out, kind)
		default:
			return nil, fmt.Errorf("%q is not a class of record in this vault; it holds %s and %s",
				value, record.KindMemory, record.KindSession)
		}
	}
	return out, nil
}

// parseTypes reads the soft filter's type list.
//
// An unknown type here is refused too, but for the opposite reason from a scope:
// the vocabulary is closed and a type outside it can never match anything, so
// accepting it would be accepting a filter that silently does nothing. That is a
// malformed argument, not a wrong guess, a wrong guess is a type in the
// vocabulary that the answer does not carry, and that one costs nothing at all.
func parseTypes(values []string) ([]record.Type, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]record.Type, 0, len(values))
	for _, value := range values {
		memoryType := record.Type(strings.TrimSpace(value))
		if !memoryType.Valid() {
			return nil, fmt.Errorf("%q is not a memory type; this vault uses %s",
				value, strings.Join(record.TypeNames(), ", "))
		}
		out = append(out, memoryType)
	}
	return out, nil
}

func optionalTime(value, field string) (*record.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var parsed record.Time
	if err := parsed.UnmarshalJSON([]byte(`"` + value + `"`)); err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return &parsed, nil
}

// knownTime is a time as an answer gives it, or nothing when it is not known.
//
// A conversation rebuilt from turns that record no time has no dates, and the
// zero time would be given as 0001-01-01T00:00:00.000Z, which a model reads as
// a real date. Left empty, the field's key is dropped instead.
func knownTime(t record.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.String()
}

func memoryDetail(memory *record.Memory) MemoryDetail {
	detail := MemoryDetail{
		URI:        URI(record.KindMemory, memory.ID),
		ID:         memory.ID.String(),
		Type:       string(memory.Type),
		Statement:  memory.Statement,
		Context:    memory.Context,
		Tags:       memory.Tags,
		Created:    memory.CreatedAt.String(),
		Importance: memory.Importance,
		Confidence: memory.Confidence,
		Links:      memory.Links,
	}
	if memory.Supersedes != nil {
		detail.Supersedes = URI(record.KindMemory, *memory.Supersedes)
	}
	return detail
}

func snippet(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if len(text) <= SnippetBytes {
		return text, false
	}
	cut := SnippetBytes
	// Cut on a rune boundary, and then on a word boundary if one is near, so a
	// snippet never ends inside a character or halfway through a word.
	for cut > 0 && !isBoundary(text[cut]) {
		cut--
	}
	if cut < SnippetBytes/2 {
		cut = SnippetBytes
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
	}
	return strings.TrimSpace(text[:cut]) + "…", true
}

func isBoundary(b byte) bool { return b == ' ' || b == '\n' || b == '\t' }

// unrestoredRecords names what this device knows of on the network and has not
// restored, for an answer that has to say so.
//
// Memories are counted and conversations are not. A conversation is catalogued
// as the chunks of its transcript, and which conversation a chunk belongs to is
// written inside the chunk, which a device that has not restored it cannot read.
func unrestoredRecords(restoration vault.Restoration) string {
	memories := fmt.Sprintf("%d memory record(s)", restoration.Memories)
	switch {
	case restoration.Memories > 0 && restoration.Chunks > 0:
		return memories + " and at least one stored conversation"
	case restoration.Chunks > 0:
		return "at least one stored conversation"
	}
	return memories
}
