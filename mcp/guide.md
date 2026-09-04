# Sennit

The user's own store of memories and past conversations, encrypted with keys that never leave their
device and held on the Sia network. You are reading its guide because you asked for
`sennit://guide`.

## What is in here

Two classes of record share one address space and one search:

- **memories**, single durable propositions, retrieved by meaning
- **sessions**, stored conversations: a small head naming an immutable transcript

`recall` searches across both. That is deliberate: "have I worked on this before" can be
answered by a fact, by a past conversation, or by both, and they are not separate systems.

## Writing a memory that can be found again

This is the part that decides whether the vault is useful, and it is worth the attention.

**One proposition per record.** A record holding three claims is retrieved for whichever one
dominates it and is useless for the other two.

**Context is not optional and the vault will reject a record without it.** It is what makes a
statement resolvable once it has been separated from the conversation it came from, where it came
from, what was being decided, what it stands in contrast to. Measured on this project's own corpus,
adding it moved retrieval more than every model choice available put together.

**Two to four specific tags.** Tags are matched exactly, so:

- prefer the specific to the general, in a vault about one project, tagging everything with that
  project's name tells nothing apart
- reuse the vault's existing tags rather than coining a synonym; `sennit://vault` lists them
- one tag naming the subject and one naming the aspect beats two words for the same idea

The response to a write tells you how many records already carry each tag you used, and names any tag
too common in this vault to narrow a search. That is the one moment at which it is cheap to fix:
records are immutable once written.

## Searching

Pass the user's actual question, in their words. The ranking is mostly by meaning and partly by
matching words, so a whole question retrieves better than the nouns pulled out of it.

**Filters prefer; they never exclude.** Supplying tags and types you expect the answer to carry
improves the ranking, and a tag you guessed wrongly costs a little ranking quality and nothing else,
records that do not match are still returned, just lower. You cannot lose an answer by guessing, so
guess.

**Scope is different and it does exclude.** `scope` selects which class of record may answer:
memory, session, or both. Use it when the user is asking for a container ("list my sessions"), never
to narrow a question about a subject. A scope is a statement about what is being addressed; a filter
is a guess about what the answer will be about.

## Reading

`recall` and `browse` return snippets and addresses. Call `open` on an
address for the whole record. That keeps a search cheap and lets you spend context only on what you
decided was worth it.

## Conversations

`save_session` stores a conversation as a title, a summary, tags, and its turns. Only the
title, summary and tags are searchable, the transcript is never embedded, because a conversation is
mostly filler and tool noise, and the summary is what a search is actually looking for. Write the
summary as though someone else will read it to decide whether to open the conversation.

Appending is cheap: pass the session's address and only the new turns. Nothing already stored is
rewritten.

## What this vault will not do

It runs no model of its own. It will not summarise, infer importance, or decide that two records are
the same thing, it reports the evidence and leaves the judgement to you. When a write lands near an
existing record, the response says so and it is yours to decide whether you have added a fact,
restated one, or contradicted one.

It also does not claim more than is true. A record is durable on the user's device before a write
returns and reaches the network shortly afterwards; the response says which has happened. Never tell
the user something is stored on the network before the response says it is.
