# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Verified end to end against live Sia

- **The same object is still byte exact at 38.0 days.** The durability marker written on
  2026-08-05, 6,191 B of plaintext, was re-read twice after the release went out: at 834.3 h
  (34.8 d) on 2026-09-09 and at 912.3 h (38.0 d) on 2026-09-12, byte exact both times. These are
  two more readings at two more ages and nothing more. The spans between checks are still
  unobserved, so this remains readability at those ages and not continuous readability.

### Fixed

- **The durability figure in the 0.1.0-beta-mvp entry was corrected from 30.3 days to 28.3 days.**
  No check at 30.3 days exists in the evidence ledger, and the checks that back that entry are 0 h,
  18 h and 28.3 d. The published release notes for `v0.1.0-beta-mvp` still carry the original
  wording and will disagree with this file until the release page itself is edited.

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
