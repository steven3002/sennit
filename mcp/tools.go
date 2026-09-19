package mcp

import (
	"strings"

	"github.com/steven3002/sennit/record"
)

// The tool descriptions are product surface, not documentation.
//
// Recall quality rests on an agent supplying two or three accurate tags and a
// type at both ends of the loop: a record written without them is unreachable by
// the mechanism that makes retrieval work, and a query issued without them
// leaves that mechanism idle. Measured, the difference between a query filtered
// with usable tags and one without is the difference between hit@5 of 0.949 and
// 0.881, and in a vault where most records concern one subject it is the
// difference between finding an answer and reading five near misses.
//
// These strings are therefore written with the same care as the ranker. They are
// defined here, apart from any server, so that the contract exists and can be
// reviewed before anything speaks the protocol.
//
// ⚠ Whether a real agent tags this well is UNTESTED. The filters that produced
// the measured numbers were written by a model that had read the whole corpus.
// These descriptions are built on the assumption that a well-briefed agent does
// nearly as well, and that assumption is scheduled for validation against a real
// agent, it is not something this package may claim.

// A Tool is one operation exposed to an agent, with the description that tells
// the agent how to use it well.
type Tool struct {
	Name        string
	Title       string
	Description string
}

// RememberTool asks for the two fields that decide whether a record can ever be
// found again.
//
// The description spends most of its length on `context` and `tags` because
// those are what fail. A statement is easy to write and hard to retrieve; the
// context is what makes it resolvable once it has been separated from the
// conversation it came from, and it is worth more to retrieval than every model
// choice available put together.
var RememberTool = Tool{
	Name:  "remember",
	Title: "Store a memory",
	Description: strings.TrimSpace(`
Store one durable fact, preference, insight or correction in the user's own encrypted vault.

Write ONE self-contained proposition per call. If you have three things to remember, call this three
times: a record holding several claims is retrieved for whichever one dominates it and is useless for
the others.

REQUIRED FIELDS, and why each one matters:

- statement: the proposition itself, in a single sentence. Write it so it will still make sense to
  someone who has never seen this conversation. "The rollup runs hourly" is not a memory; "Tidepool's
  reading rollup runs hourly, at ten past the hour" is.

- context: what makes the statement resolvable on its own, where it came from, what was being
  decided, what it is in contrast to. This is NOT optional and the vault will reject the record
  without it. It is the single largest thing that determines whether this record is ever found
  again, worth more than any other choice in the retrieval pipeline. One or two sentences.

- type: exactly one of fact, preference, insight, doc, profile, correction.
    fact        something observed or measured about the world or a system
    preference  how the user wants things done
    insight     a conclusion drawn rather than observed
    doc         reference material held roughly verbatim
    profile     a durable attribute of a person, team or system
    correction  something that was NEVER TRUE, as opposed to something that has become outdated.
                Use this when you are recording that a previous belief was wrong. Something that
                was true and has since changed is not a correction, supersede it instead.

- tags: two to four, lowercase, no spaces. This is where recall is won or lost, so it is worth a
  moment's thought:
    * Prefer the SPECIFIC over the general. In a vault about one project, tagging everything with
      that project's name tells nothing apart. "cache-invalidation" separates; "backend" does not.
    * REUSE the vault's existing tags rather than coining a synonym. Two tags meaning one thing
      split the records that should have been found together, and tags are matched exactly.
    * Add one tag naming the SUBJECT and one naming the ASPECT, what it is about, and what about
      it. "prediction" plus "accuracy" beats two words for the same idea.
  The response reports how many records already carry each tag you used. A tag already on most of
  the vault will not narrow anything, and that is worth correcting on the next write.

OPTIONAL, and worth supplying when you know them:

- supersedes: the id of a record this one replaces. The old record is kept and stays retrievable as
  history; it simply stops being the current answer. Use this when something has CHANGED. Use a
  correction record instead when something was never true.
- links: ids of related records, for navigation and provenance.
- importance, confidence: your own judgement, 0 to 1. The vault runs no model and will not infer them.
- validFrom, validUntil: when the statement is true OF THE WORLD, as distinct from when you learned
  it. Leave them out unless the statement really does have a window.

WHAT THE RESPONSE TELLS YOU, and what to do about it:

- neighbours: the records already in the vault closest to what you just wrote. Read them.
- conflicts: neighbours close enough to be the same statement again. If one is there, you have
  probably just duplicated or contradicted an existing record. Decide which: leave both if they are
  genuinely different, or supersede the old one if this replaces it. Do not simply move on.
- tags: how many records already carry each tag you used, and whether any of them is too common in
  this vault to narrow a search.

The record is durable on the user's device before this call returns; it reaches the network shortly
afterwards. The response says which. Never tell the user something is stored on the network before
the response says it is.
`),
}

// RecallTool asks for the other half of the same contract.
//
// It states plainly that filters prefer rather than exclude, because an agent
// that believes a filter is a WHERE clause writes narrow, brittle filters to
// avoid false positives, which is precisely the behaviour that made hard
// filtering a cliff. Knowing that a wrong tag is survivable is what makes an
// agent willing to supply tags at all.
var RecallTool = Tool{
	Name:  "recall",
	Title: "Search memory by meaning",
	Description: strings.TrimSpace(`
Search the user's vault by meaning and return the records most likely to answer the question.

Pass the user's actual question as the query, in their words. Do not reduce it to keywords: the
search ranks mostly by meaning and only partly by matching words, so a whole question retrieves
better than the nouns extracted from it, and the words are still there for the part that wants
them.

ALWAYS supply tags and types when you can infer them from the question. They are the difference
between finding the answer and returning five near misses, and in a vault where most records concern
one subject they matter more, not less.

- tags: two or three you would expect the answer to carry. Guess from the question. Prefer the
  specific: for "why is deployment slow", "deployment" and "latency" beat "engineering".
- types: which kinds of record could answer this.
    "why" and "should we" questions are usually answered by insight
    "what is" and "how much" by fact
    "how does the user like" by preference
    "what did we get wrong" by correction

FILTERS PREFER, THEY DO NOT EXCLUDE. A tag you guessed wrongly costs you a little ranking quality
and nothing else, records that do not match are still returned, just lower. You will never lose an
answer by guessing, so guess. Supplying three plausible tags is strictly better than supplying none
for fear of being wrong.

SCOPE IS THE OPPOSITE AND IT DOES EXCLUDE. Read this even if you skipped the paragraph above.
"scope" names which class of record may answer at all, memory, session, or both, and anything
outside it is removed rather than ranked lower. It is not a filter and the rule about guessing does
not apply to it: set it ONLY when the user has named a container, as in "list my sessions" or "what
conversations do I have about this". Never set it because a question's wording suggests a class. Leave
it empty for an ordinary question and you get one ranked list holding both, which is what you want
almost every time. When a scope does remove records the response says how many, so you can see that
an answer was shortened rather than absent.

By default this returns the vault's CURRENT state: records that have been superseded by a later
version are held back, and the response says how many. Ask for history explicitly when the user
wants to know what something used to be, or how a decision changed.

An empty result is a real answer. It means the vault does not hold this. Say so rather than
presenting the closest record as if it were responsive, the user knows what they told you, and a
confident wrong memory is worse than none.

ONE SEARCH IS OFTEN NOT ENOUGH, AND YOU MAY RUN MORE. Use what a search returns to decide what to
search for next, then search again. This matters most when the user asks for something in words
that describe what they WANT rather than what they ARE: "suggest a hotel", "what should I cook",
"recommend something to watch". The vault stores both the durable fact about the user and the
record of them having asked for the same thing before, and a question in the user's own words
matches the asking, which ranks the fact that would actually answer it far down. A first search
phrased their way is still the right opening move, because it is correct for most questions. When
it comes back holding requests rather than facts about the user, search again for the attribute:
what they own, prefer, or habitually do.

Search again too when the results disagree with each other. A vault can hold two records that
cannot both be true, and a second search aimed at the disagreement is how you find out which is
current rather than averaging them into an answer the user never gave you.

Stop when you have enough to answer. Each search costs the user context, so two or three aimed
searches beat five speculative ones.

Results are snippets with addresses, not whole records. Call "open" on an address when you need the
full statement and its context. Each result carries the similarity it earned and the boost the filter
gave it, so you can see how much of a record's position came from meaning and how much from matching
your filter.
`),
}

// BrowseTool lists rather than searches, and its filters are the hard kind.
//
// It is stated in its own voice for exactly that reason. Recall's description
// spends a paragraph teaching an agent that filters prefer and never exclude,
// which is correct there and wrong here, and an agent carrying the one rule into
// the other tool will either believe a browse cannot narrow or believe a recall
// can empty.
var BrowseTool = Tool{
	Name:  "browse",
	Title: "List what is stored",
	Description: strings.TrimSpace(`
List what the vault holds, newest first, by metadata rather than by meaning.

Use this when the user asks what is in there, "what have I saved about the deploy", "show me my
recent conversations", and use "recall" when they ask a question the vault might answer. Browsing
orders by time and never ranks; searching ranks and never orders by time.

EVERY FILTER HERE EXCLUDES. This is the opposite of "recall", deliberately, and the difference is
about who is guessing. When you infer a tag from the wording of a question you are guessing, and a
wrong guess must never lose an answer, so recall only ever prefers. When the user asks to see the
records carrying a tag, nobody is guessing, and returning records without it would be the wrong
answer rather than a slightly worse one. So here:

- kinds: "memory", "session", or both. Omit for both.
- types: memory types. A record of another type will not be listed.
- tags: every listed tag is required. A record carrying two of your three tags will NOT appear.

Because these exclude, an empty result here means exactly what it says and is not evidence that the
vault holds nothing related. If a browse comes back empty, try recall with the same words as a soft
filter before telling the user there is nothing.

Returns a page and, when more remain, a cursor. Pass the cursor back verbatim for the next page; it
is opaque and is not an offset, so a page boundary stays correct even while the vault is being
written to. When no cursor comes back, you have seen everything.

Superseded records are held back by default, as in recall. Ask for them when the user wants history.
`),
}

// OpenTool is the one reader for the whole address space.
var OpenTool = Tool{
	Name:  "open",
	Title: "Read anything by its address",
	Description: strings.TrimSpace(`
Read anything in this vault by its address. One opener for the whole namespace: you never need to
know which part of the system owns an address.

  sennit://vault                        what this vault holds, and the tags it already uses
  sennit://guide                        how to use this vault well
  sennit://memory/{id}                  one memory, in full
  sennit://session/{id}                 one conversation: title, summary, counts, links
  sennit://session/{id}/transcript      that conversation's turns

NEVER GUESS AN ADDRESS. Every result from "recall", "browse", "remember" and "save_session" carries
addresses, and those are the only ones that exist. An id you assembled yourself will not resolve, and
inventing one that looks plausible is worse than saying you do not have it.

Read sennit://vault before your first write in a conversation. It lists the tags this vault already
uses, and reusing one rather than coining a synonym for it is what keeps related records findable
together, tags are matched exactly, so "cache-invalidation" and "cache_invalidation" are two
different tags holding half the answer each.

A transcript can be long. Pass a limit to read a page of it, and pass the returned "nextFrom" back as
"from" to continue. Reading a conversation's head costs nothing like reading its transcript: the head
is about a kilobyte whatever the conversation's size, so prefer opening the session first and the
transcript only if you need the turns.
`),
}

// SaveSessionTool stores the conversation itself.
//
// The description spends its length on the summary for the same reason
// `remember` spends it on context: the summary and the tags are the entire
// searchable surface of a conversation. The transcript is never embedded, so a
// session saved with a thin summary is a conversation that cannot be found
// again, however much of it was stored.
var SaveSessionTool = Tool{
	Name:  "save_session",
	Title: "Store a conversation",
	Description: strings.TrimSpace(`
Store this conversation in the user's vault so it can be found and continued later, including in a
different agent.

Call this when a conversation is worth returning to, or whenever the user asks you to save it.

WHAT DECIDES WHETHER IT IS EVER FOUND AGAIN:

- title: one line, specific. "Tide station registry disagreement" is a title; "Chat" is not.
- summary: what was decided, what was tried, what is still open. THE TRANSCRIPT IS NOT SEARCHABLE,
  only the title, summary and tags are, because a conversation is mostly filler and tool noise and
  the summary is what a search is actually looking for. Write it for someone deciding whether to open
  the conversation, not as a record of it. Two to five sentences.
- tags: two to four specific ones, reused from the vault's own vocabulary where they fit.

- messages: the turns. Each needs an id unique within the conversation, a role, and content as an
  ordered list of typed parts, text, reasoning, toolCall, toolResult, file, resourceLink. A tool call
  and the result it produced MUST carry the same callId; that correlation is the one thing no later
  reader can reconstruct, and a call without it is refused rather than stored broken. Anything your
  runtime knows that this schema does not name goes in "ext" on the message or the part, and comes
  back exactly as it went in.

APPENDING. To add turns to a conversation already stored, pass its address as "session" and ONLY the
new turns. Nothing already stored is rewritten, so the cost of an append is the size of the append.
The title, summary and tags are updated only if you supply them, so you can add turns without
restating what you already said, but do refresh the summary when the conversation has moved on, or
it will be found by what it used to be about.

DURABILITY. The conversation is on the user's device before this returns and reaches the network on
the vault's ordinary schedule, within the hour. The response says which has happened. Do not tell the
user a conversation is stored on the network until the response says it is. Set "durable" to write it
to the network now, do that at the end of a long session, not on every append, because each forced
write costs a whole storage block whatever it holds.
`),
}

// ForgetTool is stated in the same place because deleting is part of the same
// contract, and because what it does not do needs saying.
var ForgetTool = Tool{
	Name:  "forget",
	Title: "Remove a memory",
	Description: strings.TrimSpace(`
Remove a record from the user's vault, by its address.

Prefer superseding to forgetting. A record that has become wrong is usually worth keeping as
history, and superseding it removes it from ordinary recall while leaving the trail intact. Forget is
for things the user does not want stored at all.

Requires "confirm": true. Ask the user first unless they have just asked you to delete this
particular thing, you are removing something from a store they own, and it is the one operation here
that cannot be undone from inside the vault.

Forgetting a conversation removes its transcript too. Forgetting a memory drawn from a conversation
leaves the conversation alone.

This does not immediately return storage. Records share storage with everything written alongside
them, so space comes back later, when nothing in a batch is still needed. Do not report freed space.
`),
}

// Tools is the surface in the order an agent meets it.
//
// The order is not alphabetical and is not arbitrary. A host that discovers
// tools progressively shows the model names and one-line descriptions long
// before it fetches a schema, so the first two are the ones the product is named
// around and the ones a model should reach for without thinking.
var Tools = []Tool{RecallTool, RememberTool, BrowseTool, OpenTool, SaveSessionTool, ForgetTool}

// TypeGuidance is the one-line gloss for each type in the vocabulary, for
// building a tool schema's enum documentation without restating it by hand.
//
// It is derived from the vocabulary rather than written beside it, so a type
// added without guidance is a compile error rather than an agent guessing.
func TypeGuidance() map[record.Type]string {
	return map[record.Type]string{
		record.TypeFact:       "something observed or measured",
		record.TypePreference: "how the user wants things done",
		record.TypeInsight:    "a conclusion drawn rather than observed",
		record.TypeDoc:        "reference material held roughly verbatim",
		record.TypeProfile:    "a durable attribute of a person, team or system",
		record.TypeCorrection: "something that was never true, as opposed to something now outdated",
	}
}
