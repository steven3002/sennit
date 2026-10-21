# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Verified end to end against live Sia

- **An interrupted flush leaves storage that can be found and released again.** A flush of 320
  records was cancelled 642 ms into the 1.9 s its object pinning takes, on a throwaway vault. It
  left one 40 MiB slab billed and holding **136 of the 320 objects**, and this device's ledger named
  it, so `sennit status` reported it as this installation's rather than another's, and a plain
  `sennit reclaim` deleted those 136 objects and released the slab. The account read 83,886,080 B
  before the run, 209,715,200 B at its peak across three slabs, and 83,886,080 B afterwards, holding
  the same two slabs it started with. The flush had been told that 121 pins succeeded and 16 failed,
  and the slab held 136: fifteen of the pins reported as failures had already been applied by the
  indexer, which is why a write records its slab rather than trying to undo itself.
- **Releasing a slab that never existed is not an error.** The indexer answers `slab <id> not found`
  for an id it has never seen, the same wording it uses for one it has released, so an entry left by
  a write whose very first pin failed is dropped by the next reclamation rather than blocking it.
- **The upload percentage is real.** One live flush of a single record on a throwaway vault moved
  the progress through 24 distinct percentages over 9.2 s of uploading, from the SDK's own
  per-shard callback, and the storage it used was released again: the account read 80.00 MiB
  before and 80.00 MiB after, with the flush's 40 MiB slab freed by `reclaim --release-all`.
- **The same object is still byte exact at 38.0 days.** The durability marker written on
  2026-08-05, 6,191 B of plaintext, was re-read twice after the release went out: at 834.3 h
  (34.8 d) on 2026-09-09 and at 912.3 h (38.0 d) on 2026-09-12, byte exact both times. These are
  two more readings at two more ages and nothing more. The spans between checks are still
  unobserved, so this remains readability at those ages and not continuous readability.

### Added

- **One live line while a command waits.** Every command now says what it is doing, in place, with
  the elapsed time and `ctrl+c to cancel` once a wait is worth naming, and ends on one past-tense
  line. An upload shows a real percentage, counted from the shards it has written. Interrupted, it
  says what was stopped and what that left behind. Where output is piped, redirected, in CI or
  under `TERM=dumb`, the same run prints one plain line per step instead, and a `Still ...` line
  every ten seconds so a long wait is never silent.
- **`sennit recall` prints a table**, with `--memory-text` to expand each row to the whole
  statement, its context and its id; tab-separated output with nothing cut off when stdout is a
  pipe; and `--json`, which is the shape the MCP server answers a recall with, so a script and an
  agent read the same fields.
- **Errors, warnings and hints as badges**, short and lowercase, with the problem and the fix kept
  apart. Off a terminal they fall back to `error:`, `warning:` and `hint:` labels.
- **Grouped help**, to stdout, exiting 0: `sennit`, `sennit help`, `sennit --help` and every
  `sennit <command> --help`. An unknown command suggests what was probably meant instead of
  printing the whole usage.
- **`--color=auto|always|never` on every command, and `--verbose`** on every command that opens a
  vault. `NO_COLOR`, `FORCE_COLOR` and `TERM=dumb` are honoured, `NO_COLOR` wins over
  `FORCE_COLOR`, and **`SENNIT_NO_PROGRESS=1`** turns the live line into plain lines.
- **`SENNIT_GLYPHS=full|basic|ascii`**, for a terminal whose font draws boxes instead of the
  progress glyphs. Without it the set is chosen from the locale and the console.
- **The recovery phrase can be typed when a command asks for it.** A command that finds no phrase
  and has a terminal to ask at now prompts, with the terminal's echo turned off, so the words do
  not reach the screen, the scrollback, the shell's history or the process table. `SENNIT_PHRASE`
  and a piped phrase both still work and both still come first, and a run with no terminal to ask
  at refuses exactly as it did before, so nothing scripted behaves differently. ctrl+c at the
  prompt ends the command and leaves the terminal as it found it.
- **Progress events on the Go SDK surface**: an optional `OnProgress` on `vault.Options` reports
  the phase an operation is in and, for an upload, how many shards of how many are written. A nil
  callback is exactly the behaviour of a vault opened before it existed.
- **`/resume` with nothing after it asks which conversation to resume, where it used to take the
  newest.** A host that can show a dialog lists up to fifteen of the conversations that changed most
  recently, each labelled with its title, the agent it happened in and the day it last changed, and
  the answer is checked against what was offered. A host that cannot is given the same list as text,
  and the agent is told to ask the user rather than pick one itself. Closing the dialog resumes
  nothing, and the answer is an error that says how to resume the most recent conversation instead.
  `recent` after the command now does that without asking, which is what a bare `/resume` did.

### Changed

- **`sennit remember` no longer waits for the upload.** `--flush` now defaults to false, so a write
  returns once the record is sealed on the device, in well under a second rather than the tens of
  seconds a slab takes on the wire. Sia bills a slab whole, 40 MiB, so waiting bought an entire
  slab for one memory of a few hundred bytes, which is what the packer exists to avoid.
  `sennit remember --flush` still waits and still reports the upload. Nothing about what the
  command claims has changed: it has always said whether a record is on this device or on Sia, and
  a queued write says so. **Worth knowing:** at the command line nothing uploads on a clock. The
  queue is flushed when a later write makes one due, or when you run `sennit flush`, which
  `sennit status` will tell you is owed.
- **Results go to stdout and everything a person watches goes to stderr.** `sennit status` used to
  print its whole report to stderr; the report is now on stdout, so it can be read by a script
  while the progress stays out of the way.
- **Timings, read tiers and internal accounting moved behind `--verbose`**, in the words they had
  before. Nothing was dropped: what a command used to print unasked it still prints when asked.
- **A bare `sennit` prints a short screen and exits 0**, rather than the whole usage and exit 2.
  Help is something a reader asked for.
- **`sennit connect` writes everything to stderr.** None of it is a result another program reads.
- **`sia` passes the erasure coding it uses, 10 data and 20 parity shards, explicitly** rather than
  relying on the SDK's defaults, so the upload percentage has an exact denominator. The bytes on
  the wire are unchanged.
- **An interrupt while pinning says what it may leave billed.** A write pins its slab, then the
  records on it, and writes the slab into the device's ledger only once both have finished, so an
  interrupt between the two leaves a 40 MiB slab billed that nothing on the device records. Once
  pinning has started, the cancel line of `flush`, `remember --flush` and `reclaim --repack` now
  ends with "A slab pinned before the interrupt may stay billed; `sennit status` shows it." Before
  pinning starts the line is unchanged, since nothing has been pinned. `sennit status` already
  names such a slab, and `sennit reclaim --orphans` releases it once nothing is queued.
- **The `recall` tool returns up to 100 results a page, and its description says when to search
  again.** `limit` stopped at 50, and a larger value was cut to 50. It now stops at 100, the default
  is still 5, and its description says to raise it when the user wants a thorough search and accepts
  what that costs in context. The tool's description now tells an agent that one search is often not
  enough: to search again when the results hold the user's own earlier requests rather than facts
  about them, or when two results disagree, and to stop once it can answer.
- **`remember` asks for the conversation a memory came from, and refuses a `span` without one.** The
  tool's description and its `session` and `span` fields now say they are expected whenever the
  memory came from a saved conversation, because the statement is the agent's rewrite of what was
  said, and a rewrite can lose a detail, such as a date, that the conversation still holds. Both
  stay optional in the schema. A `span` sent without a `session` used to be dropped without a word,
  and is now refused with a message saying to pass the session too or leave the span out.
- **`/resume` reads the words after it by what they hold, whichever argument a host puts them in.**
  A host that fills a prompt's arguments in order put the first word of a topic into `session`,
  where it was refused as an address, so a topic could not be searched for from such a host. Now an
  address, or a conversation's id on its own, resumes that conversation, `recent` resumes the
  newest, and any other words are searched for as one topic, whichever arguments they arrived in.
  After `recent`, an address or an id, only a number of turns to replay can follow, and any other
  word is refused with the command to run instead, where a word after an address used to be dropped.
  The descriptions of the prompt and of its three arguments say what each one takes.
- **A resume replays at most 100 turns.** How many to replay, given in `turns` or typed after
  `recent`, an address or an id, must be a whole number from 1 to 100, and the default is still 30.
  There was no ceiling before. A whole number in `turns`, which is where a host that fills arguments
  in order puts the third word typed, is always read as the count, and one outside that range is
  refused rather than cut down to 100, because it may not be a count at all: a year typed as the
  third word of a topic, for example. The refusal says to put such a number among the topic's other
  words instead.
- **A resume ends by asking the agent for a short recap and a wait.** A host hands a prompt's result
  to the model as soon as the user runs it, and the result told the model to continue from where the
  conversation left off, so it went straight on with the open work before the user had said what
  they wanted. The result now ends with one more message, after the turns, asking for one or two
  sentences on what the conversation was about and where it stopped, and then no tool call, no save
  and no open work until the user asks. It still gives the address to append new turns to.
- **A conversation's `created` and `updated` are left out when they are not known.** When none of a
  conversation's turns records a time, a device that rebuilt it with `sennit hydrate` cannot date
  it, and `open` on its address, a read of `sennit://session/{id}`, `recall`'s results and their
  full records, the record a `/resume` embeds and `sennit recall --json` now leave its dates out.
  **For scripts:** `recall`'s output schema no longer lists `created` as required on a result, and
  `sennit recall --json` can leave `created` out of a result and `created` and `updated` out of its
  `detail`. A memory's `created` is unchanged. The two fields are now described as what they are,
  when the record was first saved and when it was last written on this device, by a save, a memory
  recorded against it or a delegated run added or removed, where they were described as when the
  conversation started and when it was last appended to. `save_session`'s schema, `sennit://guide`
  and the server's instructions now ask an agent to give a turn a `created` time only from its own
  record of when it was said, and to leave it out rather than invent one.
- **`sennit://vault` has two new optional fields, `restore` and `catalog`.** `restore` is set when
  this device has restored nothing of the vault from the network, or knows of records there that it
  has not restored, so that the counts beside it are not read as the vault's, and it names the
  `sennit hydrate` that restores them. `catalog` is set when the server has dropped a change to the
  catalog that a crash cut off, and says what that may have undone and what to run again. Both are
  absent otherwise.
- **Every resource link a tool returns now has a name.** The specification requires one and an empty
  name is left out of the JSON, so a strict host rejected the whole result over one link. The usual
  case was a `save_session` that did not restate the title, as an append or an edit usually does
  not: it was reported as failed after the save had happened, and an agent saved again. A link is
  now named by the record's title or statement, by the title the vault holds after such a save, and
  by its address when this device has nothing else to call the record. A memory whose statement
  opens on a line break is called by its first line of text, in `remember`, `recall` and `open` and
  in the `title` that `sennit recall --json` gives it.
- **The embedding model is fetched by Sennit itself, from one pinned revision.** Each of the six
  files the model is made of comes from a fixed revision of its repository, arrives under a
  temporary name, and is renamed into the model folder only once its size and SHA-256 match the
  pinned ones, so an interrupted download leaves nothing that passes for the model, and the model
  cannot change under a vault. The download needs no symbolic links and does not read `HOME`. A
  folder that already holds the six files at their pinned sizes is used as it is, and one that holds
  some of them is completed. Two processes that start together share one download, a failure names
  the file and the cause, only a broken transfer is retried, and `HF_ENDPOINT` names a mirror, which
  is held to the same hashes. This applies on every platform.
- **The vault's directory holds two empty lock files, `manifest.lock` and `index.lock`.** Every
  process with the vault open takes the matching one while it appends to the catalog or the search
  index, reads it at open or compacts it, so that a line another process is still writing is never
  taken for one a crash cut off, and the operating system lets go of it if the process dies. A
  filesystem that cannot take the lock refuses to open the vault rather than running without it. A
  server started from an earlier build takes no lock, so restart it after upgrading.

### Fixed

- **Two printed lines agree with their own counts.** A release that found a single slab read `1 slab
  hold nothing`, and an interrupted flush with a single record queued followed "The record stays
  queued on this device." with "They have not reached Sia yet." Both now take their verb and pronoun
  from the count, and the release row says how many slabs are not in this device's ledger rather than
  "of them", which reads as a plural of one. The hint beside each of them was already built this way.
- **A flush interrupted while it is pinning no longer strands storage nothing can release.** A write
  uploads its records, registers the slab, then registers each record's location, one round trip per
  record and sixteen at a time, which for a large flush is seconds of pinning. The slab was written
  into this device's ledger only once all of that had succeeded, so an interrupt part way through
  left a slab that was billed, that held part of the flush, that `sennit status` blamed on another
  installation of the vault, and that no command could release: an ordinary `sennit reclaim` is
  bounded by the ledger, `--orphans` reaches only slabs holding nothing, and `--take-ownership` only
  rewrites entries the ledger already has. Each slab is now recorded before it is pinned, so an
  interrupt anywhere in the pinning leaves a slab this device knows about, `sennit status` describes
  it correctly, and `sennit flush` followed by `sennit reclaim` returns it. The same applies to
  `sennit reclaim --repack`, which writes through the same path. What this costs is that
  `sennit status` counts such a slab among those this device has pinned until the next reclaim, and
  a ledger entry for a slab that never got pinned is harmless, because releasing a slab that is not
  there already counts as success.
- **A tag used for the first time is reported as new.** `remember` reports how well each tag
  separates the new record from the rest of the vault, and a tag nobody had used before was always
  reported as already sitting on one record, because the record being written was counted before
  the counting ran. The advice exists to catch a mistyped tag, and a typo is exactly a tag the
  vault has never seen, so the one case it was for could never be reported. The figures now
  describe the vault the record is joining: a first use says so, and the count beside it is of
  other records.
- **The durability figure in the 0.1.0-beta-mvp entry was corrected from 30.3 days to 28.3 days.**
  No check at 30.3 days exists in the evidence ledger, and the checks that back that entry are 0 h,
  18 h and 28.3 d. The published release notes for `v0.1.0-beta-mvp` still carry the original
  wording and will disagree with this file until the release page itself is edited.
- **`reclaim --repack` reports everything it freed.** The quota line took its readings from the
  sweep, which runs after the repack, so everything the repack released fell outside the window
  being measured and did not appear. A repack that recovered a gigabyte and a sweep that recovered
  nothing reported nothing recovered, on the headline as well as in the quota line. The figure now
  covers every stage that ran, and the repack and the sweep each name their own share so the two
  can be told apart. Nothing about what is released or retained changed.
- **A resumed conversation is dated by its turns rather than by its saves.** The resume gave the
  times its record was first and last saved as the span the conversation ran over, so one saved in a
  single call at its end showed a span of milliseconds however long it ran. The span now comes from
  the times its turns record, read from the whole transcript even when only the last turns are
  replayed. When no turn records a time, the resume says that when the conversation took place is
  not known, and gives only a time it was saved by, where this device has one. When only some turns
  record one, it says what they show of when it began and ended.
- **A conversation rebuilt from the network is no longer dated by the rebuild.** When none of a
  conversation's turns records a time, `sennit hydrate` gave the record it rebuilt the moment of the
  rebuild as both of its dates. A listing is newest first, so every such conversation came ahead of
  all the dated ones however old it was, and `/resume` took it for the most recent. Both dates are
  now unknown: `hydrate`'s warning lists them among the fields that are not recoverable rather than
  among those reconstructed, the conversation is listed after every dated one until a change on this
  device dates it, the `/resume` picker shows no day for it, and the answers that carry a
  conversation's dates leave them out.
- **An empty `browse` gives the reason that is true.** Every empty page blamed its filters and
  suggested dropping a tag, even with no filter set. With no filter, an empty first page now says
  the vault holds nothing to list yet and how records arrive. A page past a cursor says whether the
  listing has ended there or everything past the cursor has been replaced, and how to list those.
  With filters, the hint names each filter that ran, with its values, says which to drop to widen
  the listing, and points to `recall` only for tags and types, the filters `recall` treats as a
  preference. The tool's description gives that advice only for a browse that set tags or types, and
  an empty page's text is now its hint alone, without a `Nothing matches.` line before it.
- **A device that has not restored the vault no longer calls it empty.** A `recall` with nothing on
  the device to search and a `/resume` asked for the most recent conversation read only what this
  device holds, so on a device that had never restored the vault from the network, or had restored
  only its catalog, they said the vault held nothing. They now say that this device holds nothing
  and why that does not show the vault is empty, and name the `sennit hydrate` that restores it,
  with `--depth index` where the answer is about search, and so do an unfiltered `browse` that lists
  nothing and the `/resume` list. A vault just created on the device looks the same until its first
  flush, so the answer says there is then nothing to restore. A device that has restored the vault
  answers as before.
- **`forget` works on a record that has not reached the network yet, and takes its vector out of the
  search index the process is using.** Forgetting a memory still queued on this device failed with
  `no manifest entry` and left the memory in place for a later flush to upload, and a conversation
  forgotten before its first flush left its transcript queued, so the next flush uploaded and
  catalogued it. Both now leave the upload queue first, a transcript all of it or none. If a flush
  in progress has already claimed what is being forgotten, nothing is removed and the answer says to
  forget it again once that flush has finished. A claim left by a flush that crashed holds until it
  expires, ten minutes by default. A forget also took the record's vector off the disk but not out
  of the search index the process had loaded, so a later `recall` in that process that ranked the
  record failed outright, flushed or not. A change written to a conversation while it was being
  forgotten, such as an append, brought it back, and a change now updates only a record that is
  still there. The runs a conversation delegated are conversations of their own and are still not
  forgotten with it, and the answer to `forget` now says so and gives their addresses.
- **A memory or a delegated run naming a conversation the vault does not hold is refused before
  anything is stored.** `remember` with such a `session`, and a run saved through the Go SDK with
  such a parent, stored the whole record and then refused it, so the caller was told it was not
  stored and a retry stored a second copy. Both are now checked first, and `remember`'s refusal
  names the address and says it was never saved here or has been forgotten.
- **A `save_session` refused part way takes back the turns it had queued.** A save queues its turns
  before it validates the conversation, embeds it and writes its record, so a save refused at any of
  those steps, for want of a title, because the embedder failed, or because another writer had moved
  the conversation on, left its turns for the next flush to upload, though the caller had been told
  nothing was saved. They are now taken back.
- **Memories recorded against one conversation at the same time are all linked.** Each link read the
  conversation's record, added the memory and wrote the record back against the version it had read,
  so of two links landing together one was refused with `the session was changed by another writer`
  after its memory had been stored. A link now reads the record again and makes its change to it as
  it now is, and so does a delegated run added to its parent or taken off it.
- **A durable `save_session` whose write to the network fails says the conversation is stored.**
  With `durable` set, a save whose flush failed answered with the bare network error, though the
  conversation was already stored on this device, so an agent saved it again as a second
  conversation. The answer now says it is stored, gives its address, says a later flush takes it to
  the network, and says to append to that address rather than save it again.
- **A catalog whose last line a crash cut off no longer stops the vault from opening.** An append
  interrupted by a crash leaves the catalog's log ending part way through a line, and any line that
  could not be read stopped the load, so no command, `sennit recover` included, and no server could
  open the vault. A last line missing only its newline did load, but the next append was written
  onto it, and the vault then failed to open for good. A last line that cannot be read is now
  dropped and the log cut back to the line before it, and every append first makes sure the log ends
  where a line ends. A line that cannot be read anywhere else still stops the load, because that is
  damage rather than a crash. Every command warns when it has dropped one, `sennit-mcp` says the
  same on stderr, and `sennit://vault` carries it as `catalog`: the record whose change it was is
  back to what the catalog held for it before, and a forget, `sennit hydrate` or `sennit recover`
  running at the time should be run again. The search index's own log had the same flaw in another
  form and lost every vector appended after a record a crash cut off, and such a record is now taken
  off too. Opening a vault can make either repair, whatever the command.
- **Compacting the search index or the catalog no longer loses a change made while it runs.** The
  index read its files and wrote its new base under two separate holds of its lock, so a vector
  added or removed in between was lost with the log it was folded from: a forgotten record came back
  in the next process, or a new one could not be found by meaning. The catalog's snapshot was
  written from the compacting process's own copy of the catalog, so entries another process had
  appended since it opened the vault were dropped from the disk. Both now compact under one hold of
  the lock every append takes.
- **On Windows, the model is fetched without symbolic links, and a compacted file is emptied through
  a handle of its own.** `sennit init` fetched no model there, because the download library builds
  paths with forward slashes, needs the right to create symbolic links and finds its cache through
  `HOME`, and the cause was hidden behind a count of retries. A vault set up by hand stopped taking
  writes once its search index compacted, because the index and the catalog were emptied through the
  handle they are appended through, which Windows does not let shorten a file. The model is now
  fetched as described under Changed, and both files are emptied through a separate handle.
  Replacing a file that another program has open for a moment, such as a virus scanner, is retried
  there for up to two seconds. The app key file that `sennit connect --out` writes is now limited to
  the account that wrote it, since the 0600 mode it asked for means nothing on Windows, and on every
  platform the file is made private before the key goes into it rather than after. Checked by
  running natively on Windows Server 2025: the test suite passed apart from one package that the
  virus scanner kept from running, `sennit init` fetched the model with `HOME` unset, and memories
  written one command at a time all succeeded, including after the index had compacted.

## [0.1.0-beta-mvp], 2026-09-04

**First package anyone else can run.** Same substrate as 0.1.0, now released as prebuilt binaries
for Linux, macOS and Windows, on amd64 and arm64, with checksums. Tagged beta because it works end
to end and is measured, and because it has been used by about one person.

### Verified end to end against live Sia on 2026-09-04
- **The two-machine demo passes with 0 problems in 56 s**: 28 s to write and put on Sia, 18 s to
  rebuild on a directory that has never held the vault, 10 s to read it back with a **different MCP
  client in a different language**.
- **Data written to Sia is byte exact after 28.3 days.** Checked at 0 h, 18 h and 28.3 d.
  The span between 18 h and 28.3 d was not observed, so this is readability at those ages and not
  continuous readability.
- Semantic recall works across a rebuild, on a query sharing almost no words with its answer.
- The cold path works: a 128 MB embedding model downloads into an empty model directory in 6 s.

### Added
- **`sennit version`**, and a `build` package so the command line and the MCP server cannot disagree
  about what they are. Binaries built by `scripts/release.sh` are stamped with their commit and
  date; a binary you build yourself says "built from source" rather than claiming a provenance it
  does not have.
- **`scripts/release.sh`**, which cross-compiles every supported platform, stamps the build, and
  writes `SHA256SUMS`. No cgo, so every target builds from any host.

### Fixed
- **The two-machine demo stranded 40 MiB of quota on every run and said nothing.** Its cleanup
  forgot every record and then called plain `sennit reclaim`, but forgetting everything is precisely
  what empties the catalog, and a sweep refuses an empty catalog while a slab is still pinned,
  because that is indistinguishable from a catalog it failed to load. The guard was right and the
  caller was wrong. Cleanup now passes `-release-all`, and a cleanup that fails says so loudly
  instead of being swallowed by `|| true`.

### Changed
- **The head-rebuild claim was corrected, and the correction is that it was never a fixed number.**
  The README described a rebuilt session head as 11 fields restored, 3 reconstructed and 12 gone, as
  though that were a property of the schema. Two fields move depending on the run: `links.memories`
  comes back through another record's edge only if some memory links back, and the embedding is
  invented only if the rebuild embeds, which a catalog-depth hydrate does not. A live run at index
  depth with no backlink scores 11, 4 and 11. Both are right for their conditions, so the conditions
  now travel with the numbers.

### Known limitations
- The `skill` record type, the local viewer, the graph map and session forking are not built.
- Opening a vault on a second machine needs the phrase **and** one browser approval. This is not
  seed-only recovery.
- The `/resume` prompt is verified in Claude Code only.
- Recall quality figures come from synthetic corpora, not a real personal vault.
- Cost figures are advertised rates. No bill has ever been paid.

## [0.1.0], 2026-08-07

**First release.** Pre-alpha, and complete enough that someone else can clone the repository and run
it: the [README quickstart](docs/quickstart.md) is tested end to end from a fresh clone in an
environment that has never seen this project. The storage substrate, a command-line interface and an
MCP server over it exist; the viewer and the `skill` record type do not.

Everything below has been exercised against the live Sia network unless it says otherwise.

### Added
- **Storage substrate.** Records are sealed on the device, batched, and written to Sia in shared
  slabs. The write queue is held on disk, so a record that has been accepted but not yet written to
  the network survives the process that accepted it, and an offline write is owed to the network
  rather than silently kept local.
- **Catalog as an append-only log with periodic snapshots**, compacted on a size ratio. Total bytes
  written stay proportional to the number of records instead of to its square.
- **Recovery from the recovery phrase and the indexer alone.** Every stored record carries its own
  identity beside its ciphertext, so a vault that has lost its catalog can be rebuilt completely.
  Damage is bounded: a truncated or corrupted object yields the records before the break, and the
  authenticated envelope rejects anything that parses but is not what it claims to be.
- **Three-tier reads**, the device's own copy, a cached storage location, and a cold fetch, with
  every read counted by the tier that served it.
- **Reclamation.** Storage that nothing points at is released, including storage stranded by an
  installation that no longer exists. Repack rewrites live records into fewer slabs and runs only
  when asked.
- **Semantic recall with soft metadata filtering.** Tags and a record type supplied with a query
  *prefer* matching records; they never exclude any. A tag guessed wrongly costs a little ranking
  quality and nothing else, which is deliberate, as an exclusion, one wrong tag was measured
  returning nothing at all for a sixth of queries.
- **Hybrid ranking: a full-text index beside the vector one.** Three signals decide the order, in
  one stated sequence, the vector pass ranks the candidate pool by meaning, a BM25 pass reranks it
  by the words the query used, and the metadata filter boosts what the caller asked for. The two
  passes are combined by weighted rank fusion, so no score scale has to be reconciled between them,
  and a record either pass found on its own still ranks.
  The lexical weight is small on purpose. It was chosen as the largest value that costs a query
  sharing *no* word with its answer nothing at all, the case a memory store exists for, and the one
  a term index cannot help and can actively harm. Measured against that criterion, the lexical pass
  buys precision near the top of the list (hit@1 0.627 → 0.678) rather than a better hit@5, and the
  configuration that ships outranks giving the two passes an equal vote on every metric measured.
- **Context is required on every record**, and a record without it is rejected rather than stored
  with a default. It is the single largest measured contributor to whether a record can be found
  again, and records are immutable, so a missing context cannot be repaired afterwards.
- **Supersession.** A record can replace an earlier one. The replaced version stops being the
  current answer and stays retrievable as history.
- **Write-time feedback.** Storing a record reports the records nearest to it, flags any close
  enough to be a restatement, and says how many records already carry each tag used, so a tag too
  common to narrow a search is visible while it is still cheap to change.
- **The search index persists** as an immutable base plus appended deltas, folded together on a size
  ratio, so restarting does not mean re-deriving every vector.
- **A mixed-model index is detected.** Vectors from two embedding models cannot be compared, and
  comparing them yields a plausible number rather than an error; vectors from another model are
  refused, counted, and reported instead of silently searched.
- **A recall regression suite** over a small committed corpus, reporting hit@k and mean reciprocal
  rank on every run, alongside two axes that an aggregate hides: how crowded each query's
  neighbourhood is, and which part of the vault its answers live in.
- **Sessions: a small head plus ordered, immutable chunks.** A conversation is stored as a head, its
  title, summary, tags, project, counts and lineage, that names the transcript without containing
  it. Measured, a four-hundred-turn conversation is a **1,119-byte head over 809 KiB of transcript**,
  and a thousand heads list in a few milliseconds while reading no transcripts at all. Appending
  writes new chunks and rewrites only the head, so the cost of a turn is the size of the turn rather
  than the size of the conversation, and no chunk that already exists is ever touched again.
- **A portable message format, chosen by intersection rather than invented.** Content is an ordered
  list of typed parts and never a flat string; a tool call and its result carry the same explicit
  correlation id, so the link survives however the turns are interleaved; and every provider field
  this format has no name for is carried through untouched and comes back byte for byte. Those three
  are what five independent vendors agree on, and the first is what the existing cross-agent
  converters concede they lose.
- **Sessions and memories cross-reference each other.** A memory extracted from a conversation
  records the session and the exact turns it came from; the conversation records the memories it
  produced. One step from a fact to the exchange that produced it, in either direction.
- **Sub-agent conversations are separate records with a containment edge**, and loading a session
  names them without opening them unless it is asked to. A delegated run can be as large as the
  conversation that delegated it.
- **Conversations rank beside memories in one list, and a caller can address one class explicitly.**
  The class selector is deliberately a different thing from the metadata filter: a filter is a guess
  about what an answer will be about and must never cost an answer, while asking for sessions is a
  statement about what is being looked at and is honoured exactly, with the count of what it set
  aside reported alongside.
- **An MCP server**, `sennit-mcp`, over stdio, no listening socket, and both secrets read from the
  environment rather than from a flag or a tool argument. Six tools: `recall`, `remember`, `browse`,
  `open`, `save_session`, `forget`. Every one returns a structured result against a declared schema
  *and* a mirrored text block, and links to records by address rather than by bare id.
- **One address space, and one function that resolves it.** Memories, conversations and transcripts
  are addresses in a single namespace, and the tool that opens an address and the protocol's own
  resource reads are the same code path, not by convention but structurally, because there is one
  function that turns an address into bytes and both call it. Two entry points into one namespace
  drift quietly, since each looks correct on its own.
- **Everything reachable is named in the server's instructions**, and a test asserts that against the
  server's own registrations in both directions. A host fetches the resource listing when it connects
  and keeps it to itself, so registering an address does not make it reachable; a model told nothing
  about an address will correctly refuse to guess one.
- **A `resume` prompt.** It finds a stored conversation, by address, by topic, or just the most
  recent, and returns its summary, the memories drawn from it, and its recent turns, so a
  conversation can be continued in a different agent from the one it happened in. It says how the
  conversation was chosen and how well it matched, and when it hands over only the recent turns it
  says so rather than letting them pass for the whole conversation.
- **Results are concise by default**, with snippets, explicit similarity scores, and addresses to
  open for the rest. An empty result is a success with a hint, never an error.
- **A conversation format that survives a real agent's transcript.** Validated against 159 real
  Claude Code session logs, 46,311 messages, every one of which converts into this format and back
  with every field intact. Fixing that revealed a defect worth naming: the format's part vocabulary
  was closed, so a single part type it had not heard of rejected the entire message rather than the
  part. The vocabulary is now open on write.
- **Commands:** `init`, `connect`, `remember`, `recall`, `flush`, `status`, `reclaim`, `recover`,
  `hydrate`.
- **`sennit connect`,** the whole onboarding path in one command: it holds a live approval link
  open across the roughly ten minutes each one lasts and reissues on expiry, writes the issued app
  key to a file at `0600`, **reads that file back and confirms it carries the key the indexer
  issued**, and then waits for the account to become writable, because approval is not readiness,
  and a write before the indexer has funded host accounts fails with a message about hosts that says
  nothing about waiting.
- **`sennit hydrate`,** which rebuilds a vault on a machine that has never held it, in three
  stages whose costs are three orders of magnitude apart, so the vault is usable before it is
  complete.
- **Continuous integration**: build without cgo, vet, gofmt, the full test suite, a race-detector
  pass over the concurrent packages, and the **recall regression harness reporting hit@1/3/5/10** on
  the committed synthetic corpus, so a change in retrieval quality moves a number in a build log
  instead of going unnoticed.
- Project scaffolding: README, license, contribution guide, security policy, code of conduct, and
  `docs/host-checks.md` for connecting the server to an MCP host and checking that it works.

### Fixed

- **The MCP server no longer exits without closing the vault.** Any transport error left the process
  through `os.Exit`, which runs no deferred function, so the close that stops the flush timer and
  releases the manifest, the search index, the embedder, the indexer connection, the sealer and the
  device store never happened. A flush in flight at that moment holds a claim on the records it is
  writing so that two installations cannot pay for the same slab twice, and abandoning it left them
  claimed until the claim expired rather than handing them back. The close's own error was discarded
  as well, and now reaches stderr like every other diagnostic here, without changing the exit code:
  a vault that did not shut down cleanly is exactly what the next run has to recover from, and it
  used to say nothing at all. `go vet` has no check for this, which is how it survived a green
  pipeline, so CI now runs one.
- **The command line says when a vault did not shut down cleanly.** All eight subcommands that open
  a vault closed it through a deferred `Close()` whose error went nowhere, so a device store that
  failed to finalize still reported success and exited 0. The close now reports to stderr in the
  same form the server uses. It deliberately does not change the exit code: by the time it runs the
  command's work is done and its result decided, and a close failure is information about the *next*
  run, not a reason to call this one failed.
- **A damaged app key is refused with a sentence instead of a stack trace.** An app key is an
  ed25519 private key, and `types.PrivateKey` is a byte slice whose length the compiler cannot
  enforce, so anything but 64 bytes panicked when it went to sign. The panic was measured at 4 and
  31 bytes on a slice bound, and at 32 and 63 inside `crypto/ed25519`. Only "non-empty hex" was ever
  checked. The length is now checked where the key is read *and* on the line above the conversion,
  so no caller can reach the panic. A truncated key, which is the likely way this happens because a
  copy dropped characters, gets its own instruction rather than the one for a key that was never
  set.
- **An already-released slab no longer fails a reclamation.** The code intended to treat a slab the
  indexer had released on its own as success, and the check never matched: the sentinel is
  `slab not found`, and the service sends `slab <id> not found`, with the id in the middle. The
  tolerance had been in place since the behaviour was anticipated and had never once fired. When
  `sia.storage` deployed automatic slab release, **every repack began reporting a failure after
  completing its work**, and left a ledger entry that made every later sweep fail on the same slab.
- **`recall` no longer fails when the indexer is unreachable.** Query embedding, vector search and
  the device's own copy of a record are all local, so an outage was turning a working local search
  into a hard failure. A vault that cannot reach the indexer now opens degraded, says so, answers
  reads from the device and queues writes.
- **Indexer errors no longer quote the request URL or the response body.** The SDK's URLs carry the
  app key's signed authentication parameters, so a connection failure was printing credentials into
  the terminal; a gateway error was printing a CDN's HTML. Failures now name the operation, the
  service, the status and what to do about it.
- **Flags written after the text are refused rather than silently read as part of it.** Go's flag
  parsing stops at the first plain word, so `remember "..." -offline` took `-offline` as prose and
  then failed asking for an app key the user had just said they did not want to use.
- **`-context` is required and now says so where you meet it**, naming the flag rather than
  explaining the concept, and the top-level usage shows it.
- **The "no recovery phrase" error names `init -new-phrase`.** A first-time user has no phrase, and
  nothing in the error said the tool could make one.

### Notes on behaviour worth knowing

- **A saved record is not yet a stored record.** Writes are durable on the device immediately and
  reach the network on a flush. Nothing in the interface conflates the two.
- **Records share a storage slab and a slab is billed whole**, so forgetting one record out of a
  slab returns no space; the space returns when a slab has nothing live left in it.
  ⚠️ **Changed under us, measured 2026-08-07:** the hosted indexer now releases a slab **within
  300 ms** of its last object being deleted, with no explicit unpin. It did not do this on
  2026-08-05, when the same check waited six minutes and the slab stayed pinned. Reclamation still
  deletes objects first and releases slabs second, that order is what makes it correct under both
  regimes, and the reverse order strands objects permanently either way.
- **Reclamation fails closed.** A sweep refuses to run when the keep-set it computes does not account
  for every record the catalog holds, and an empty catalog over pinned slabs stops it rather than
  authorising it. An empty keep-set means the computation is wrong, not that the data is dead, a
  fail-open default destroyed data here twice.
- **A hydrated vault may read storage it did not write and may not release it.** Hydration reaches
  every slab holding a record the phrase opens, which would otherwise let a second device unpin
  storage the first one is still pointing at. `reclaim -take-ownership` is the deliberate act for a
  vault whose writing installation is gone for good.
- **Repack is manual, and it holds the old and new storage at the same time.** Measured under
  concurrent writes and under interruption (below); nothing triggers it automatically.
- **An interrupted repack loses no data and can strand one slab.** Killed inside its write phase, it
  leaves the records readable at their old locations and the catalog untouched, and may leave a slab
  pinned that the device's ledger never learned about, the network answers with the slab id after
  the process is gone. `sennit reclaim` cannot see it, `sennit reclaim -orphans` releases it, and
  `sennit status` now reports storage billed to the account that the ledger does not know.
- **Retrieval quality depends on what else is in the vault.** A vault where nearly everything
  concerns one subject is measurably harder to search than a varied one, because the records
  competing with the right answer share its tags and the filter cannot tell them apart. Quoted
  figures below say which case they describe.
- **Recovering a vault re-derives every vector**, at roughly 0.45 s per record, so a large vault
  takes a while to become searchable again after a recovery. The records themselves come back first.
- **Only a conversation's summary is searched, never its transcript.** Embedding every message of a
  long conversation is a large cost for a poor result, transcripts are mostly filler and tool
  output, so a session is found by its title, summary and tags. The transcript is always there to
  be read; it is not what the search reads.
- **A conversation's transcript is on the network; the head that names it is on the device.** The
  head changes every time the conversation is added to, and storage here holds immutable content, so
  the two live in different places. A vault rebuilt from the network alone currently recovers the
  transcripts and not the records naming them.
- **A saved conversation is subject to the same flush window as anything else, and this is
  deliberate.** Forcing a write to the network on every append would bill a whole storage slab per
  turn, measured, forty mebibytes for a transcript of under a kilobyte, so a long conversation
  would consume a large fraction of a free account for a few hundred kilobytes of text. Saving
  reports plainly whether the network has it yet, and a caller who wants it written now can say so.

### Notes

Design and feasibility work completed against the **live Sia network**. Findings that shaped the architecture:

- **Storage is viable.** End-to-end encrypted round-trip is byte-exact; cold read p50 ~210 ms, warm ~42 ms; storage cost measured in the low single digits of dollars per TiB per month.
- **Small records must be packed.** A storage slab is billed whole regardless of how little it holds, and a partially-filled slab can never be extended, so writes are batched into shared slabs, and periodic repack is a requirement rather than housekeeping.
- **Writes parallelise.** Committing a thousand records went from ~361 s to ~4.8 s by pinning slabs once per flush and objects concurrently.
- **Recovery does not depend on the index.** Records are self-describing on disk, so a full vault can be reconstructed from the recovery phrase and the indexer alone.
- **Retrieval quality, and which case each number describes.** On a labelled corpus with metadata
  filtering, semantic recall reached **0.949 hit@5 in a vault of varied subject matter**. In a vault
  of ten thousand records all concerning one subject, the same pipeline holds **0.797**. Filtering
  helps in both cases and helps most where the competition is thickest, but it does not close the
  gap. Both figures are the same measurement reported honestly; the first alone would not be.
- **Filtering must prefer rather than exclude.** As an exclusion, a single wrong tag took hit@5 from
  0.949 to 0.729, below not filtering at all, and returned nothing whatever for a sixth of
  queries. As a preference, the same wrong filter scores exactly what no filter scores.
- **Recall is uneven inside a single vault**, and an average conceals it: the queries with the most
  competing records scored 0.690 against 0.933 for the rest of the same vault. Quality is reported
  per query as well as in aggregate for that reason.

### Measured for this release, on the live network

Configuration for every figure: hosted indexer `sia.storage`, free tier, 46.57 GiB ceiling, 40 MiB
slabs, one 2-vCPU Linux host, 2026-08-07. A number without its configuration is not a number.

- **Repack under concurrent writes: no penalty and no lost writes.** 12 records over 3 slabs into 1,
  transient peak 4 slabs, **10.64 s under load against 10.83 s idle** on the identical path. Every
  repacked record read back byte-exact from its new location. While it ran, a **second process**
  wrote and flushed 8 more records into the same vault; after reopening, the catalog held all 12
  relocations and all 8 new records, 20 of 20.
- **Repack interrupted: no data loss.** Killed inside its write phase, all 12 records stayed readable
  byte-exact at their old locations and the catalog was unchanged. The cost is up to one slab
  (40 MiB) pinned and unknown to the ledger, fully recovered by `reclaim -orphans`.
- **Deleting every object over a slab returns its quota in ~300 ms** with no unpin call.
- **Free-tier exhaustion behaviour is NOT measured.** Reaching it means filling 46.57 GiB, about
  1,192 slabs, on the only account this project has, and the failure it induces is precisely the one
  that leaves an account unable to repack itself. A second account needs a browser approval by a
  person. What is checked instead is the gate in front of it: repack is refused on the **transient
  peak** it needs rather than on the steady state it would end at, since an account can be under its
  limit both before and after a repack and unable to afford the moment in between.

[0.1.0]: https://github.com/steven3002/sennit
