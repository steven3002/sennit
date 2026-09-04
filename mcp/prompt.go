package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// ResumePrompt is the name a host renders as a slash command.
const ResumePrompt = "resume"

// ResumeTurns is how many of a conversation's most recent turns a resume
// replays by default.
//
// It is a context budget, not a fidelity choice. The whole transcript is stored
// and addressable, and the prompt names the address, so a model that needs more
// asks for more; loading four hundred turns to answer a follow-up question would
// spend the context the resumed conversation is supposed to use.
const ResumeTurns = 30

// registerPrompts publishes the prompt surface.
//
// A prompt matters here out of proportion to its size, because prompts are
// user-controlled and surface as slash commands. "Open a different agent and
// type /resume" is a different product from "open a different agent and hope it
// decides to call a tool": it is deterministic, it is one keystroke, and it
// leaves the user in control of their own memory, which is the whole political
// point. It is built here rather than as polish for that reason.
//
// ⚠ Whether a host actually renders it as a slash command is confirmed for none
// of them from this side of the wire. It needs a person at an interactive
// session; the surface is built so that it does not depend on one host's
// rendering, the same conversation is reachable through `recall` and `open`
// with no prompt at all.
func (s *Server) registerPrompts() {
	s.sdk.AddPrompt(&sdk.Prompt{
		Name:  ResumePrompt,
		Title: "Resume a stored conversation",
		Description: "Bring back a conversation stored in Sennit, its summary, its most recent " +
			"turns, and the memories drawn from it, so you can carry on where you left off, " +
			"including in a different agent from the one it happened in.",
		Arguments: []*sdk.PromptArgument{
			{
				Name: "session",
				Description: "The address of the conversation to resume, as sennit://session/{id}. " +
					"Leave empty for the most recent one.",
			},
			{
				Name: "topic",
				Description: "Words to find the conversation by, when you do not have its address. " +
					"Searched against titles and summaries.",
			},
			{
				Name:        "turns",
				Description: "How many recent turns to replay. Default " + strconv.Itoa(ResumeTurns) + ".",
			},
		},
	}, s.resume)
}

func (s *Server) resume(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	args := req.Params.Arguments
	turns := ResumeTurns
	if raw := args["turns"]; raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("turns must be a positive whole number, not %q", raw)
		}
		turns = parsed
	}

	session, how, err := s.findSession(ctx, args["session"], args["topic"])
	if err != nil {
		return nil, err
	}

	loaded, err := s.vault.LoadSession(ctx, vault.LoadSessionRequest{ID: session, Transcript: true})
	if err != nil {
		return nil, notFound(Address{URI: URI(record.KindSession, session)}, err)
	}
	replayed := loaded.Messages
	var skipped int
	if len(replayed) > turns {
		skipped = len(replayed) - turns
		replayed = replayed[skipped:]
	}

	result := &sdk.GetPromptResult{
		Description: fmt.Sprintf("Resuming %q, %d of %d turn(s), %s",
			loaded.Session.Title, len(replayed), loaded.Session.Counts.Messages, how),
	}
	result.Messages = append(result.Messages, &sdk.PromptMessage{
		Role:    "user",
		Content: &sdk.TextContent{Text: resumeFraming(loaded, replayed, skipped, how)},
	})

	// The head, embedded rather than summarised. A host that renders embedded
	// resources shows the user what is being brought back, and a model reading
	// it gets the same bytes `open` would return for the same address, because
	// it is the same resolver.
	head, err := s.Resolve(ctx, URI(record.KindSession, session), ResolveOptions{})
	if err != nil {
		return nil, err
	}
	result.Messages = append(result.Messages, &sdk.PromptMessage{
		Role: "user",
		Content: &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
			URI: head.Address.URI, MIMEType: head.MIMEType, Text: head.Body,
		}},
	})

	// The memories drawn from this conversation, embedded whole. They are what a
	// replay of the words alone would leave out: a transcript reproduces what
	// was said and not what the assistant knew while saying it.
	for _, memory := range loaded.Session.Links.Memories {
		resolved, err := s.Resolve(ctx, URI(record.KindMemory, memory), ResolveOptions{})
		if err != nil {
			// A memory that has been forgotten since the conversation named it
			// is an ordinary state, not a reason to refuse the resume.
			continue
		}
		result.Messages = append(result.Messages, &sdk.PromptMessage{
			Role: "user",
			Content: &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: resolved.Address.URI, MIMEType: resolved.MIMEType, Text: resolved.Body,
			}},
		})
	}

	result.Messages = append(result.Messages, &sdk.PromptMessage{
		Role:    "user",
		Content: &sdk.TextContent{Text: renderTranscript(replayed)},
	})
	return result, nil
}

// resumeFraming tells the model what it has been handed and what it has not.
//
// The honesty is load-bearing rather than decorative. A replay of the last turns
// of a long conversation looks exactly like the whole conversation to a model
// that was not told otherwise, and a model that believes it has read everything
// will answer confidently about the part it never saw.
func resumeFraming(loaded vault.LoadedSession, replayed []record.Message, skipped int, how string) string {
	session := loaded.Session
	var text strings.Builder

	fmt.Fprintf(&text, "Resume this conversation from the user's own Sennit vault.\n")
	// How it was chosen, because /resume with a topic picks the nearest
	// conversation rather than only an exact one. If this is not the
	// conversation the user meant, that is visible here rather than three turns
	// later.
	fmt.Fprintf(&text, "It was chosen as %s.\n\n", how)
	fmt.Fprintf(&text, "# %s\n\n", session.Title)
	if session.Summary != "" {
		fmt.Fprintf(&text, "%s\n\n", session.Summary)
	}
	fmt.Fprintf(&text, "It ran from %s to %s across %d turn(s)",
		session.Created.String(), session.Updated.String(), session.Counts.Messages)
	if session.Agent.Name != "" {
		fmt.Fprintf(&text, " in %s", session.Agent.Name)
	}
	if len(session.Models) > 0 {
		fmt.Fprintf(&text, ", with %s", strings.Join(session.Models, " and "))
	}
	fmt.Fprint(&text, ".\n\n")

	if skipped > 0 {
		fmt.Fprintf(&text, "⚠ You are being given the LAST %d turn(s). The %d before them are stored "+
			"and are NOT below. The summary above covers the whole conversation; the turns do not. "+
			"If the answer to something might be in the earlier part, read it with `open` on %s "+
			"rather than assuming what you have is everything.\n\n",
			len(replayed), skipped, TranscriptURI(session.ID))
	} else {
		fmt.Fprintf(&text, "All %d turn(s) are below. The full record is at %s.\n\n",
			len(replayed), TranscriptURI(session.ID))
	}

	if n := len(session.Links.Memories); n > 0 {
		fmt.Fprintf(&text, "%d memory record(s) drawn from this conversation follow the summary. "+
			"They are what the assistant knew, as distinct from what it said.\n\n", n)
	}
	if len(loaded.Subagents) > 0 {
		fmt.Fprint(&text, "Work this conversation delegated, stored separately:\n")
		for _, subagent := range loaded.Subagents {
			fmt.Fprintf(&text, "  - %s (%d turn(s)): %s\n",
				subagent.Title, subagent.Messages, URI(record.KindSession, subagent.ID))
		}
		fmt.Fprint(&text, "\n")
	}
	fmt.Fprintf(&text, "Continue from where it left off. Append new turns with `save_session` and "+
		"the address %s, sending only the new ones.\n", URI(record.KindSession, session.ID))
	return text.String()
}

// renderTranscript replays turns as text.
//
// It is one text block rather than one prompt message per turn, deliberately. A
// stored transcript's roles are a record of who spoke in *that* conversation, and
// injecting them as this conversation's own turns would tell the host that the
// model already said things it has not said. Handing it over as material to read
// is the honest shape, and it is the one that survives a host with its own view
// of what a conversation may contain.
func renderTranscript(messages []record.Message) string {
	var text strings.Builder
	fmt.Fprint(&text, "## The conversation so far\n\n")
	for i := range messages {
		fmt.Fprintf(&text, "### %s\n", messages[i].Role)
		for _, part := range messages[i].Parts {
			renderPart(&text, part)
		}
		fmt.Fprint(&text, "\n")
	}
	return text.String()
}

func renderPart(text *strings.Builder, part record.Part) {
	switch part.Type {
	case record.PartText:
		fmt.Fprintf(text, "%s\n", part.Text)
	case record.PartReasoning:
		if part.Text != "" {
			fmt.Fprintf(text, "(thinking) %s\n", part.Text)
		}
	case record.PartToolCall:
		fmt.Fprintf(text, "→ called %s(%s)\n", part.Name, string(part.Input))
	case record.PartToolResult:
		fmt.Fprint(text, "← returned ")
		if part.IsError {
			fmt.Fprint(text, "an error: ")
		}
		for _, inner := range part.Content {
			renderPart(text, inner)
		}
		fmt.Fprint(text, "\n")
	case record.PartFile:
		fmt.Fprintf(text, "[%s attachment %s]\n", part.MediaType, part.Filename)
	case record.PartResourceLink:
		fmt.Fprintf(text, "[see %s]\n", part.URI)
	default:
		// A part type this build does not render is named rather than dropped.
		// A transcript that silently loses a turn's content is worse than one
		// that says it is holding something it cannot display.
		fmt.Fprintf(text, "[a %s part this build does not render]\n", part.Type)
	}
}

// findSession resolves which conversation to bring back.
//
// Three ways in, in order of how sure each is: an address the caller already
// has, a search over titles and summaries, and the most recent conversation.
// The last is what makes /resume work with no arguments at all, which is the
// whole demo.
func (s *Server) findSession(ctx context.Context, address, topic string) (record.ID, string, error) {
	if address != "" {
		id, err := addressOf(address, FormSession)
		if err != nil {
			return record.ID{}, "", err
		}
		return id, "by address", nil
	}

	if topic != "" {
		// Scoped to sessions because the caller named a container: /resume is a
		// request for a conversation, and answering it with a memory would be
		// the wrong answer rather than a worse one. This is the one place in the
		// surface where a scope is set without the model asking for it, and it
		// is set from the operation rather than from any query text.
		found, err := s.vault.Recall(ctx, recall.Request{
			Query: topic,
			Scope: []record.Kind{record.KindSession},
			Limit: 1,
		})
		if err != nil {
			return record.ID{}, "", err
		}
		if len(found.Hits) == 0 {
			return record.ID{}, "", fmt.Errorf("this vault holds no conversation to resume. " +
				"Conversations are stored with `save_session`; `browse` lists what is there")
		}
		// The similarity is reported rather than tested against a threshold.
		//
		// A ranked search always returns its nearest candidate, so a topic that
		// matches nothing well still resolves to something, and the honest
		// response is to say how well it matched, not to invent a cutoff.
		// This project measured exactly one unanswerable query and recorded that
		// one observation is not enough to ship a threshold on; a wrong cutoff
		// would refuse to resume a conversation the user can see is there.
		return found.Hits[0].ID(),
			fmt.Sprintf("the closest match for %q, at similarity %.3f", topic, found.Hits[0].Similarity),
			nil
	}

	recent, err := s.vault.ListSessions(local.SessionQuery{Limit: 1})
	if err != nil {
		return record.ID{}, "", err
	}
	if len(recent) == 0 {
		return record.ID{}, "", fmt.Errorf("this vault holds no conversations yet. " +
			"They are stored with `save_session`")
	}
	return recent[0].ID, "the most recent conversation", nil
}
