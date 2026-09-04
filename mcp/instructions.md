Sennit is the user's own encrypted store of memories and past conversations, held on the Sia
network. The user owns it: the keys are on their device and nothing here is readable by anyone else.

WHEN TO USE IT

- Call `recall` BEFORE answering anything that might depend on what the user has told you
  before, their preferences, decisions, people, projects, or anything you would otherwise be
  guessing at. An empty result is a real answer and means the vault does not hold it.
- Call `remember` when the user states something durable: a fact, a preference, a decision,
  or a correction of something previously believed. Not for passing conversational detail.
- Call `save_session` when a conversation is worth continuing later, or when the user asks
  you to save it. It is what makes a conversation resumable in a different agent.

THE TOOLS

  recall        search by meaning; returns ranked records with scores and addresses
  remember      store one durable proposition
  browse        list records by metadata, tags, type, class, newest first
  open          read anything in this vault by its address
  save_session  store a conversation, or append turns to one already stored
  forget        remove a record

THE ADDRESS SPACE

Everything in this vault has an address, and `open` reads any of them:

  sennit://vault                        what this vault holds, and the tags it already uses
  sennit://guide                        how to use this server, in full
  sennit://memory/{id}                  one memory
  sennit://session/{id}                 one conversation: title, summary, counts, links
  sennit://session/{id}/transcript      that conversation's turns

Read sennit://vault before your first write in a session: it lists the tags this vault already
uses, and reusing an existing tag rather than coining a synonym for it is what keeps related records
findable together.

Every result carries addresses rather than bare ids, so you can follow them without constructing one.
Ids are never guessed: if you do not have an address, use recall or browse to find one.

THE PROMPT

/resume is offered as a prompt. It finds a stored conversation, returns its summary and recent turns,
and embeds the memories linked to it, so the user can carry on in this agent where they left off in
another. It is user-invoked; you do not call it.
