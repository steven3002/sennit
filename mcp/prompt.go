package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// MaxResumeTurns is the most turns a resume replays. A count above it is refused
// rather than cut down to it, for the reason readResume gives.
//
// It is set by what a replayed turn costs, measured rather than assumed over
// thirteen real Claude Code conversations, the owner's own and not in this
// repository. Rendered the way a resume renders them, with each API message a
// turn, their turns average 1,816 bytes. Nine in ten runs of a hundred
// consecutive turns replay in no more than about 250 KiB, which is about
// 91,000 tokens at the 2.8 bytes a token measured on the replies in the same
// conversations that hold only text. That is under half of a 200,000-token
// context window, which many current models still have, so nine resumes in ten
// at the ceiling leave the conversation they resume at least the other half.
// Past 110 turns, fewer would. With each content block a turn instead, a turn
// is smaller and the margin wider.
const MaxResumeTurns = 100

// ResumeRecent is the word that resumes the most recent conversation without
// asking which.
//
// It is reserved only as the first word after the command, where it is read
// before a topic can be. Anywhere later in a topic it is one more word to search
// for.
const ResumeRecent = "recent"

// ResumeChoices is how many recent conversations a resume offers when nothing
// after it names one.
//
// The list is read whole rather than searched, so it is kept to what a person
// takes in at a glance. It has to be quick to read for a second reason, measured
// rather than assumed: Claude Code 2.1.283 gives a prompt sixty seconds, and a
// dialog still open when they run out closes and takes the prompt with it. A
// conversation older than the list is better found by what it was about, with
// `recall`, than by scrolling, and the list says so.
const ResumeChoices = 15

// ChoiceWidth is how many characters of one choice a dialog shows.
//
// Measured rather than assumed: Claude Code 2.1.283 shows a choice of up to 48
// characters whole and cuts a longer one to 47 and an ellipsis, at every
// terminal width tried from 80 columns to 160. The agent and the date come last
// in a label, so a title that ran on would push them out of sight. The title is
// shortened instead, to the room they leave.
const ChoiceWidth = 48

// ChoiceTitle is how many characters of a title a list given as text shows.
//
// An agent writes the title and nothing bounds its length. Text has no width
// to fit, but a list is still one line per conversation, and seventy-two
// characters keep enough of a long title to recognise it by. The whole title is
// one `open` away.
const ChoiceTitle = 72

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
		// How to use the prompt comes first, because Claude Code's command menu
		// shows only the start of the description, and the menu is where the
		// user finds this prompt: typed in full, the name it shows there is an
		// unknown command.
		Description: "Choose a stored conversation to resume, or add what it was about, or " +
			ResumeRecent + " for the newest one. Brings back its summary, its most recent turns and " +
			"the memories drawn from it, in this agent or a different one.",
		// A host that fills arguments in order puts the first word typed into
		// the first argument declared, so the order here is part of the
		// interface. Each argument is read for what it holds rather than for its
		// name (see readResume), and each description says what it accepts from
		// a host that fills them in order as well as from one that fills them by
		// name.
		Arguments: []*sdk.PromptArgument{
			{
				Name: "session",
				Description: "The conversation to resume: its address, as sennit://session/{id}, that {id} " +
					"alone, or " + ResumeRecent + " for the most recent one. Anything else is the start of " +
					"what the conversation was about, searched for together with the words in topic. Leave " +
					"it and topic empty to choose from a list of recent conversations.",
			},
			{
				Name: "topic",
				Description: "What the conversation was about, or more of it after the words in session. " +
					"Searched against titles and summaries. After an address, an id or " + ResumeRecent +
					" in session, only the number of turns to replay can go here, in place of turns.",
			},
			{
				Name: "turns",
				Description: "How many recent turns to replay, a positive whole number up to " +
					strconv.Itoa(MaxResumeTurns) + ". Default " + strconv.Itoa(ResumeTurns) + ". A whole " +
					"number here is always the count, so one outside that range is refused. When session " +
					"starts a topic, anything else here is searched for as one more word of it, so a " +
					"number that belongs to the topic has to come earlier.",
			},
		},
	}, s.resume)
}

func (s *Server) resume(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	want, err := readResume(req.Params.Arguments)
	if err != nil {
		return nil, err
	}

	var (
		session record.ID
		how     string
	)
	if want.namesNothing() {
		// Nothing named, so nothing is guessed: the user chooses. The first
		// time through, the list goes out; the second, the answer is here.
		answer, answered := req.Params.InputResponses[choiceInput]
		if !answered {
			return s.offerRecent(req)
		}
		session, err = s.chosen(answer, req.Params.RequestState)
		how = "picked by the user from a list of recent conversations"
	} else {
		session, how, err = s.findSession(ctx, want)
	}
	if err != nil {
		return nil, err
	}

	loaded, err := s.vault.LoadSession(ctx, vault.LoadSessionRequest{ID: session, Transcript: true})
	if err != nil {
		return nil, notFound(Address{URI: URI(record.KindSession, session)}, err)
	}
	replayed := loaded.Messages
	var skipped int
	if len(replayed) > want.turns {
		skipped = len(replayed) - want.turns
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
	result.Messages = append(result.Messages, &sdk.PromptMessage{
		Role:    "user",
		Content: &sdk.TextContent{Text: resumeClosing},
	})
	return result, nil
}

// resumeClosing is what the model is asked to do with what it was handed.
//
// It comes after the turns because it is what the model reads last, and it asks
// for a short answer and a wait because a host sends a prompt's result to the
// model the moment the user runs it. An instruction to carry on sends the model
// straight into the open work, spending a reply, and often tool calls, on
// something the user has not asked for yet. What happens next in a resumed
// conversation is the user's to say.
const resumeClosing = "That is everything brought back. Reply in one or two sentences, saying what " +
	"this conversation was about and where it stopped. Then wait for the user. Do not call a " +
	"tool, save anything or start on open work until they ask."

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
	fmt.Fprintf(&text, "How it was chosen: %s.\n\n", how)
	fmt.Fprintf(&text, "# %s\n\n", session.Title)
	if session.Summary != "" {
		fmt.Fprintf(&text, "%s\n\n", session.Summary)
	}
	// When it happened is read from its turns, which record when each was said,
	// and not from the head. On the device that saved the conversation, the
	// head's two times are when its record was first and last written, so one
	// saved in a single call at its end would show a span of milliseconds. The
	// turns are the whole transcript and not only the ones replayed below, so a
	// first turn left out of the replay still dates the start.
	turns := loaded.Messages
	first, last := datedEnds(turns)
	// Both ends dated is all a span needs, whatever the turns between them
	// record.
	spanned := first == 0 && last == len(turns)-1
	if spanned {
		fmt.Fprintf(&text, "It ran from %s to %s across %d turn(s)",
			turns[first].Created.String(), turns[last].Created.String(), session.Counts.Messages)
	} else {
		fmt.Fprintf(&text, "It ran for %d turn(s)", session.Counts.Messages)
	}
	if session.Agent.Name != "" {
		fmt.Fprintf(&text, " in %s", session.Agent.Name)
	}
	if len(session.Models) > 0 {
		fmt.Fprintf(&text, ", with %s", strings.Join(session.Models, " and "))
	}
	fmt.Fprint(&text, ".")
	switch {
	case first < 0:
		// Nothing dates the conversation, so what is given is when it was
		// saved, as a bound on when it happened and not as a span. The head's
		// Updated is that bound wherever it is set, because whatever sets it
		// does so after the last save. Where the
		// conversation was saved, it is the last save or a later change such as
		// a memory drawn from it. On a device that rebuilt the head from the
		// network it is unset, because a rebuild dates a head only by its turns,
		// until a change on that device sets it, and until then no bound is
		// given. Created would not do as the bound, since where the conversation
		// was saved it is only the first save, and turns appended after it were
		// saved later.
		fmt.Fprint(&text, " Its turns do not record when they were said, so when it took place is "+
			"not known.")
		if !session.Updated.IsZero() {
			fmt.Fprintf(&text, " It was saved no later than %s.", session.Updated.String())
		}
	case !spanned:
		// Some turns are dated and at least one end is not. A turn before the
		// first dated one was said before it, and a turn after the last dated
		// one was said after it, which is as far as the dated turns go. No save
		// time is given as a bound on the end, because the head's times are not
		// always save times: a head rebuilt from the network takes both from
		// these dated turns, and the undated turns after them were said later.
		began, ended := "at "+turns[first].Created.String(), "at "+turns[last].Created.String()
		if first > 0 {
			began = "before " + turns[first].Created.String()
		}
		if last < len(turns)-1 {
			ended = "after " + turns[last].Created.String()
		}
		fmt.Fprintf(&text, " Only some of its turns record when they were said, and they show it "+
			"began %s and ended %s.", began, ended)
	}
	fmt.Fprint(&text, "\n\n")

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
	fmt.Fprintf(&text, "To keep this conversation's new turns, append them with `save_session` and "+
		"the address %s, sending only the new ones.\n", URI(record.KindSession, session.ID))
	return text.String()
}

// datedEnds finds the first and last turns that record when they were said, as
// positions in the transcript, or -1 for both when none does.
//
// They are the first and last in the order the turns were said, and not the
// earliest and latest times. Order comes from the sequence and never from the
// clock, as record.Message says of the field, so a turn dated by a device with a
// skewed clock does not change which turn begins the conversation or ends it.
func datedEnds(turns []record.Message) (first, last int) {
	first, last = -1, -1
	for i := range turns {
		if turns[i].Created.IsZero() {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	return first, last
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

// A resumeRequest is what the words after the command ask for: a conversation,
// named in one of four ways or in none, and how many of its turns to replay.
type resumeRequest struct {
	// session is the conversation an address or an id named, and how says which
	// of the two named it, for the framing to repeat. how is empty when neither
	// did.
	session record.ID
	how     string
	// recent asks for the conversation that changed last.
	recent bool
	// topic is the words to search titles and summaries for.
	topic string
	// turns is how many of the most recent turns to replay.
	turns int
}

// namesNothing reports whether the words named no conversation at all, which is
// when the user is given the recent ones to choose from.
func (r resumeRequest) namesNothing() bool {
	return r.how == "" && !r.recent && r.topic == ""
}

// readResume reads the words after the command for what they name.
//
// They are read for what they hold rather than for the argument each arrived
// in, because hosts disagree about where a word goes. A host that fills
// arguments by name, the MCP Inspector among them, puts each value where the
// user put it. Claude Code fills them in the order they are declared, one typed
// word each, and that was measured in 2.1.283 rather than assumed: a word past
// the last argument is dropped without a warning, and quotes stay in the words
// instead of holding a phrase together. There, /mcp__sennit__resume harbour
// survey arrives as session "harbour" and topic "survey", so a topic can only
// start in the first argument.
//
// The first argument is resolved in a fixed order: an address, then an id on
// its own, then the reserved word, then a topic. Each of the first three names
// one conversation, so all that can follow one is how many of its turns to
// replay, which a host that fills arguments in order puts in topic. A count is
// read from there, or from turns, where a host that fills them by name puts
// it. Any other word in topic is refused rather than dropped: nothing after such
// a word is searched for, and a word dropped without telling the user leaves
// them believing it was used.
//
// A topic takes the words in all three arguments. From a host that fills them
// one each that is at most three words, because a fourth is lost before it
// arrives. The exception is a whole number in turns, which is always the count
// of turns to replay, because the third word is the only place such a host can
// put one after a topic. That settles the one ambiguity left, a topic whose
// third word is a number, toward the count. A number meant as part of the topic
// is searched for as one when it comes first or second, and the framing quotes
// the words that were searched, so a number read as a count shows there rather
// than going missing.
//
// A count a resume does not replay, zero, a negative number or one above
// MaxResumeTurns, is refused wherever it arrives, and the refusal says what to
// type instead. It is not cut down to the ceiling: such a number may not be a
// count at all, a year typed as the last word of a topic, say, and replaying the
// ceiling would then spend the most context a resume can on a reading the user
// never meant, with a note saying so reaching them only through the model,
// which errNothingChosen explains is not to be relied on. Nor is it searched for
// as a word of the topic, because a whole number in that place would then be a
// count or a word depending on its value, and a count the user got wrong would
// be searched for without a word to them.
func readResume(args map[string]string) (resumeRequest, error) {
	first := strings.TrimSpace(args["session"])
	topic := strings.TrimSpace(args["topic"])
	last := strings.TrimSpace(args["turns"])
	want := resumeRequest{turns: ResumeTurns}

	// named is what a refusal calls a first word that names one conversation,
	// and again is that word as the command takes it, for a refusal to say what
	// to run instead.
	var named, again string
	switch {
	case isAddress(first):
		// A malformed address is refused here, before any word after it is read,
		// because it is the mistake to put right first. Searching for it instead
		// would resume whichever conversation lies nearest to a string the user
		// meant as an address.
		id, err := addressOf(first, FormSession)
		if err != nil {
			return resumeRequest{}, err
		}
		want.session, want.how = id, "by address"
		named, again = "an address", first
	case isID(first):
		// `sennit recall --memory-text` shows each result's id alone, and an id
		// names a conversation as exactly as its address does. Searching for it
		// instead would resume whichever conversation lies nearest to a string
		// the user meant as an id.
		id, _ := record.ParseID(first)
		want.session, want.how = id, "by id"
		named, again = "an id", first
	case strings.EqualFold(first, ResumeRecent):
		// Matched regardless of case, because a person types it after a slash
		// command and the host passes the word on exactly as it was typed.
		want.recent = true
		named, again = ResumeRecent, ResumeRecent

	// A conversation's name is matched here, once a conversation can be given
	// one: after an id and the reserved word, so that no name can hide the
	// conversation an id names or the newest one, and before a topic, so that a
	// name resumes the one conversation it names rather than the nearest match
	// for its words. A name spelled as an address, an id or the reserved word
	// would never be reached here, which makes it one to refuse when the name is
	// given. Like the words above, a name names one conversation, so what follows
	// it is read by countAfter.

	case first != "":
		// Every word that arrived is part of the topic, the last one as well
		// unless it is written as a whole number, which is read as a count.
		want.topic = first
		if topic != "" {
			want.topic += " " + topic
		}
	default:
		// Only a host that fills arguments by name can leave the first one
		// empty and still send a topic.
		want.topic = topic
	}

	if named != "" && topic != "" {
		return countAfter(want, named, again, topic, last)
	}
	if last == "" {
		return want, nil
	}
	count, number := readCount(last)
	switch {
	case number && count == 0:
		return resumeRequest{}, cannotReplay(last, want.topic != "")
	case number:
		want.turns = count
	case named == "" && first != "":
		// Only a topic that starts in the first argument makes a word in turns
		// part of it.
		want.topic += " " + last
	default:
		// After a conversation named exactly, or nothing at all, a word in turns
		// that is not a count is a mistake the user is told about.
		return resumeRequest{}, fmt.Errorf("nothing was resumed, because turns must be a positive "+
			"whole number up to %d, not %q", MaxResumeTurns, last)
	}
	return want, nil
}

// countAfter reads what a host that fills arguments in order puts after a word
// that names one conversation exactly, which can only be how many of its turns
// to replay. named is what a refusal calls that word, and again is the word as
// the command takes it.
func countAfter(want resumeRequest, named, again, word, next string) (resumeRequest, error) {
	resumes := "that conversation"
	if named == ResumeRecent {
		resumes = "the most recent conversation"
	}
	count, number := readCount(word)
	switch {
	case !number:
		instead := fmt.Sprintf("Run %s %s to resume %s", resumeCommand, again, resumes)
		if named == ResumeRecent {
			// The word may have been meant to start a topic, which the reserved
			// word ahead of it keeps from being searched for.
			instead += ", or leave " + ResumeRecent + " out to search for one by what it was about"
		}
		return resumeRequest{}, fmt.Errorf("nothing was resumed, because only a number of turns can "+
			"follow %s, and %q is not one. %s", named, word, instead)
	case count == 0:
		return resumeRequest{}, cannotReplay(word, false)
	case next != "":
		return resumeRequest{}, fmt.Errorf("nothing was resumed, because only a number of turns can "+
			"follow %s, and %q came after %q. Run %s %s %d to resume %s",
			named, next, word, resumeCommand, again, count, resumes)
	}
	want.turns = count
	return want, nil
}

// readCount reads a word as how many turns to replay. number reports whether it
// is written as a whole number, with or without a sign, and count is how many
// turns it asks for, or zero when a resume does not replay that many.
func readCount(word string) (count int, number bool) {
	digits := word
	if strings.HasPrefix(digits, "+") || strings.HasPrefix(digits, "-") {
		digits = digits[1:]
	}
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	// A number too large for an int is too large to replay, and Atoi reports
	// it as an error rather than as a count.
	count, err := strconv.Atoi(word)
	if err != nil || count < 1 || count > MaxResumeTurns {
		return 0, true
	}
	return count, true
}

// cannotReplay refuses a whole number that was read as how many turns to
// replay and is not a number a resume replays. After a topic it also says how to
// search for the number instead, because there such a number may be a word of
// the topic, a year, say, typed last.
func cannotReplay(word string, topic bool) error {
	text := fmt.Sprintf("nothing was resumed, because %q was read as the number of turns to replay, "+
		"which must be a positive whole number up to %d, or left out for the default of %d",
		word, MaxResumeTurns, ResumeTurns)
	if topic {
		text += fmt.Sprintf(". To search for %q as part of the topic instead, put it among the "+
			"topic's other words rather than after them", word)
	}
	return errors.New(text)
}

// resumeCommand is the command a refusal tells the user to run, in the one form
// Claude Code 2.1.283 takes typed out in full, for the reason errNothingChosen
// gives.
const resumeCommand = "/mcp__sennit__resume"

// isAddress reports whether a word is meant as an address, which is whether it
// starts with the scheme. The scheme's case is not held against it here, so an
// address typed in capitals is refused as an address rather than searched for as
// a topic.
func isAddress(word string) bool {
	return len(word) >= len(Scheme) && strings.EqualFold(word[:len(Scheme)], Scheme)
}

// isID reports whether a word is a record's id on its own: the 32 hex digits an
// address ends in, in either case.
func isID(word string) bool {
	_, err := record.ParseID(word)
	return err == nil
}

// findSession resolves the conversation a resume names.
//
// Four ways to name one: an address the caller already has, the id an address
// ends in, `recent` for the conversation that changed last, and a search over
// titles and summaries, which is the only one of the four that can come back
// with a near miss. Which of them a resume's words mean is settled by
// readResume, which also reads an address or an id to the conversation it
// names. A resume that names nothing is not guessed at. It never reaches here,
// because the user is given the recent conversations to choose from instead.
func (s *Server) findSession(ctx context.Context, want resumeRequest) (record.ID, string, error) {
	if want.how != "" {
		return want.session, want.how, nil
	}
	if want.recent {
		return s.mostRecent()
	}

	if topic := want.topic; topic != "" {
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
	return record.ID{}, "", fmt.Errorf("name the conversation to resume by its address or its id, "+
		"by a topic, or with %s", ResumeRecent)
}

// mostRecent is the conversation `recent` resumes: the one that changed last,
// which is also the first a list of recent conversations shows.
func (s *Server) mostRecent() (record.ID, string, error) {
	recent, err := s.vault.ListSessions(local.SessionQuery{Limit: 1})
	if err != nil {
		return record.ID{}, "", err
	}
	if len(recent) == 0 {
		return record.ID{}, "", s.noConversations()
	}
	return recent[0].ID, "the most recent conversation", nil
}

// errNoConversations is what a resume says when the vault holds nothing to
// resume, whether it was asked for the most recent conversation or for a list.
var errNoConversations = errors.New("this vault holds no conversations yet. " +
	"They are stored with `save_session`")

// noConversations is what a resume says when this device holds no conversation
// to offer, whether it was asked for the most recent one or for a list.
//
// That is errNoConversations only on a device that has restored what the network
// holds of the vault. One that has not says so instead, and names the command
// that restores it. Memories it has not restored go unmentioned, because they are
// not what a resume is short of.
func (s *Server) noConversations() error {
	restoration, err := s.vault.Restoration()
	if err != nil {
		return err
	}
	switch {
	case restoration.Untouched:
		return errors.New("this device holds no conversations yet, and it has not restored any record " +
			"of this vault from the network, so this does not show that the vault has none. If this vault " +
			"was just created here, there is nothing to restore; otherwise `sennit hydrate` restores its " +
			"records from the network, conversations included. New ones are stored with `save_session`")
	case restoration.Chunks > 0:
		return errors.New("this device holds no conversations yet, though it knows of at least one " +
			"stored conversation on the network that it has not restored. `sennit hydrate` restores " +
			"this vault's records from the network, conversations included")
	}
	return errNoConversations
}

// offerRecent answers a resume that names nothing with the recent
// conversations, for the user to choose from.
//
// A client that can show a form gets them as a dialog, and the model hears
// nothing until the user has chosen. The dialog goes back as an input request
// rather than through Session.Elicit because that is the one shape both sides of
// the protocol's 2026-07-28 revision accept. A client on that revision retries
// the prompt with the answer itself, and the SDK refuses Session.Elicit there.
// For a client on an earlier revision, Claude Code among them, the SDK asks the
// question on the prompt's behalf and runs this handler once more with the
// answer. That leaves one question per prompt, which is why the list is the
// whole dialog. A client that cannot show a form gets the same list as text.
func (s *Server) offerRecent(req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	rows, err := s.recentConversations()
	if err != nil {
		return nil, err
	}
	total, err := s.vault.CountMatchingSessions(local.SessionQuery{})
	if err != nil {
		return nil, err
	}
	// Two reads, so a conversation archived between them could leave the count
	// short of the page it counts.
	total = max(total, len(rows))

	if !canAsk(req) {
		return listRecent(rows, total), nil
	}
	offered := make([]string, len(rows))
	for i, row := range rows {
		offered[i] = URI(record.KindSession, row.ID)
	}
	return &sdk.GetPromptResult{
		InputRequests: sdk.InputRequestMap{
			choiceInput: &sdk.ElicitParams{
				Message:         choiceMessage(len(rows), total),
				RequestedSchema: choiceSchema(rows),
			},
		},
		// What was offered goes out with the question and comes back with the
		// answer, so the answer is checked against the list the user saw and not
		// against one that has moved since.
		RequestState: strings.Join(offered, " "),
	}, nil
}

// recentConversations is what a resume that names nothing offers, newest first.
//
// It is the listing `recent` reads, with a longer limit, so the first choice is
// always the conversation `recent` would resume, and an archived conversation is
// no more offered here than it is listed anywhere else.
func (s *Server) recentConversations() ([]local.SessionRow, error) {
	rows, err := s.vault.ListSessions(local.SessionQuery{Limit: ResumeChoices})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, s.noConversations()
	}
	return rows, nil
}

// canAsk reports whether the client can show the user a form, which is what the
// list of conversations becomes when it can.
//
// It reads the request that asks, not the session. A client on the 2026-07-28
// revision need never send initialize: it declares its capabilities on every
// request, and the session keeps only what its first request declared. A client
// on an earlier revision declared them once, at initialize, and the SDK's
// accessor falls back to that. Elicitation declared with no mode means form,
// which is what it meant before modes existed, and a client that declares only
// the URL mode cannot show a list.
func canAsk(req *sdk.GetPromptRequest) bool {
	capabilities := req.ClientCapabilities()
	if capabilities == nil || capabilities.Elicitation == nil {
		return false
	}
	return capabilities.Elicitation.Form != nil || capabilities.Elicitation.URL == nil
}

// choiceInput names the one question the dialog asks, in the request that asks
// it and in the answer that comes back.
const choiceInput = "conversation"

// choiceMessage is what the dialog says above the list: the question, how much
// of the vault the list shows, and where an older conversation is found.
func choiceMessage(shown, total int) string {
	const question = "Which conversation do you want to resume? "
	switch {
	case total == 1:
		return question + "Showing the only one."
	case shown == total:
		return question + fmt.Sprintf("Showing all %d.", total)
	default:
		return question + fmt.Sprintf("Showing the %d most recent of %d. For an older one, "+
			"close this and ask your agent to find it.", shown, total)
	}
}

// choiceSchema is the dialog's form: one required choice among the offered
// conversations, each shown by its label and answered with its address.
//
// A titled single choice, a oneOf of const and title, is the shape the
// elicitation schema has for a choice whose label is not its value. It is what
// lets the user read a title while the answer carries an address, and it is the
// shape the SDK checks both the form and the answer against.
func choiceSchema(rows []local.SessionRow) map[string]any {
	now := time.Now()
	choices := make([]map[string]string, len(rows))
	for i, row := range rows {
		about := choiceAbout(row, now)
		// A floor under the title, so an agent with a very long name costs the
		// label its date rather than the whole of its title.
		room := max(ChoiceWidth-utf8.RuneCountInString(about), ChoiceWidth/3)
		choices[i] = map[string]string{
			"const": URI(record.KindSession, row.ID),
			"title": clipTitle(row.Title, room) + about,
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			choiceInput: map[string]any{
				"type":  "string",
				"title": "Conversation",
				"oneOf": choices,
			},
		},
		"required": []string{choiceInput},
	}
}

// choiceAbout is what a label says after a conversation's title: the agent it
// happened in and the day it last changed, which is the order a list is in.
func choiceAbout(row local.SessionRow, now time.Time) string {
	var about strings.Builder
	// A head rebuilt from the network on another device does not know which
	// agent wrote it, so the agent is left out rather than shown as a blank.
	if row.Agent != "" {
		fmt.Fprintf(&about, " · %s", row.Agent)
	}
	// A head rebuilt from turns that do not record when they were said does not
	// know when it last changed either, and a list puts it after every head that
	// does, so the day is left out rather than shown as the first day of year one.
	if row.Updated.IsZero() {
		return about.String()
	}
	// In this machine's time zone, because the server runs on the user's own
	// machine and a person remembers the day in theirs. It is written short,
	// with no year when it is this year's, because every character it does not
	// use is one more of the title a dialog can show.
	day := row.Updated.Local()
	layout := "Jan 2"
	if day.Year() != now.Year() {
		layout = "Jan 2 2006"
	}
	fmt.Fprintf(&about, " · %s", day.Format(layout))
	return about.String()
}

// clipTitle shortens a title to at most limit characters, the mark of the cut
// included, cutting at a word boundary when one is near. Line breaks and runs of
// spaces become single spaces first, because a choice is one line.
func clipTitle(title string, limit int) string {
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) <= limit {
		return title
	}
	cut := limit - 1
	for cut > limit/2 && runes[cut] != ' ' {
		cut--
	}
	if runes[cut] != ' ' {
		cut = limit - 1
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

// chosen reads the user's answer to the dialog and checks it against what the
// dialog offered.
//
// The offer comes back in the request state. For a client on an earlier
// revision it never left this process. A client on the 2026-07-28 revision
// echoes it back, so there it is untrusted input like the answer itself. It is
// only ever used to test the answer, and the answer still has to parse as a
// conversation's address and load from this vault to be resumed. A client that
// dropped the state is checked against the same list read again.
func (s *Server) chosen(answer sdk.InputResponse, state string) (record.ID, error) {
	result, ok := answer.(*sdk.ElicitResult)
	if !ok {
		return record.ID{}, fmt.Errorf("the list of conversations came back as %T rather than as an "+
			"answer, so nothing was resumed", answer)
	}
	if result.Action != "accept" {
		return record.ID{}, errNothingChosen
	}
	choice, _ := result.Content[choiceInput].(string)

	offered := strings.Fields(state)
	if len(offered) == 0 {
		rows, err := s.recentConversations()
		if err != nil {
			return record.ID{}, err
		}
		for _, row := range rows {
			offered = append(offered, URI(record.KindSession, row.ID))
		}
	}
	if !slices.Contains(offered, choice) {
		return record.ID{}, fmt.Errorf("the list of conversations came back with %q, which is not "+
			"one it offered, so nothing was resumed", choice)
	}
	return addressOf(choice, FormSession)
}

// errNothingChosen is the answer to a dialog closed without a choice.
//
// It is an error rather than a message on purpose, and the reason was measured
// in Claude Code 2.1.283 rather than assumed. A host sends a prompt's messages to
// the model the moment the prompt returns, so a message saying nothing was
// resumed costs a whole model turn to repeat one sentence, and in that
// measurement the model's paraphrase dropped the words saying nothing was
// resumed. A result with no messages was worse: the model was handed the bare
// command and went through the project looking for what it meant. An error is
// shown under the command, word for word, and the model is never called.
//
// It names the command in the one form that works typed out in full in Claude
// Code 2.1.283. /sennit:resume is only the name its menu shows: typed with an
// argument after it, it is reported as an unknown command.
var errNothingChosen = errors.New("nothing was resumed, because the list was closed without " +
	"choosing a conversation. Run /mcp__sennit__resume recent to resume the most recent one")

// listRecent is the list for a client that cannot show a dialog: the same
// conversations as text, each with the address it opens at, and an instruction
// to put the choice to the user.
func listRecent(rows []local.SessionRow, total int) *sdk.GetPromptResult {
	var text strings.Builder
	fmt.Fprint(&text, "The user asked to resume a conversation from their own Sennit vault "+
		"without naming one, so none has been loaded. ")
	switch {
	case total == 1:
		fmt.Fprint(&text, "This is the only one it holds.\n\n")
	case len(rows) == total:
		fmt.Fprintf(&text, "These are all %d it holds, most recent first.\n\n", total)
	default:
		fmt.Fprintf(&text, "These are the %d most recent of the %d it holds.\n\n", len(rows), total)
	}
	now := time.Now()
	for i, row := range rows {
		fmt.Fprintf(&text, "%d. %s%s\n   %s\n", i+1, clipTitle(row.Title, ChoiceTitle),
			choiceAbout(row, now), URI(record.KindSession, row.ID))
	}
	if len(rows) < total {
		fmt.Fprint(&text, "\nAn older one is found by what it was about, with `recall` scoped to "+
			"session.\n")
	}
	return &sdk.GetPromptResult{
		Description: fmt.Sprintf("%d of %d conversation(s) to choose from", len(rows), total),
		Messages: []*sdk.PromptMessage{
			{Role: "user", Content: &sdk.TextContent{Text: text.String()}},
			{Role: "user", Content: &sdk.TextContent{Text: chooseClosing}},
		},
	}
}

// chooseClosing is what the model is asked to do with a list it was handed in
// place of a conversation.
//
// The one thing it must not do is choose. Picking the likeliest entry is what a
// helpful model does with a list, and here it would take the decision from the
// user it belongs to. Opening one waits for the answer for the same reason, and
// what follows is the same short reply and wait as any other resume.
const chooseClosing = "Show the user this list and ask which conversation to resume. Do not pick " +
	"one yourself, and do not open any of them until the user has chosen. When they choose, call " +
	"`open` on its address, then reply in one or two sentences saying what that conversation was " +
	"about and where it stopped, and wait for the user."
