# The two-machine demo

**Save on A, resume on B.** An end-to-end exercise of the claim the project rests on: that the memory
is yours and follows you. It runs the real binaries against live Sia, with a second, independent MCP
client reading what the first one wrote.

```sh
export SENNIT_PHRASE=...      # your recovery phrase; never a flag, never on screen
export SENNIT_APP_KEY=...     # issued by `sennit connect`
./demo/two-machines.sh
```

**Measured end to end: 56 s**, 22 s to write and put on Sia, 25 s to rebuild on a machine that has
never held the vault, 9 s to read it back with a different client. Building the binaries and
installing the second client's dependencies happen before the clock starts; both are one-time.

## What it shows

| Step | What is being demonstrated |
|---|---|
| **A**, an MCP client stores a memory and a four-turn conversation, then flushes | Ordinary use. The transcript carries a tool call correlated to its result by id, and a provider field this build has never heard of. |
| **B**, a directory that has never existed runs `sennit hydrate` | Everything B has is the recovery phrase. No catalog, no index, no record bodies, no session heads. |
| **B**, a *second, different* MCP client reads the vault | The TypeScript MCP SDK, against the same stdio server the Go client used. The memory is not bound to the tool that wrote it. |

## What it does not show, and says so on screen

**Two clean environments on one host, not two machines.** They share a clock, a network path and a
page cache. What is exercised is the cryptographic and protocol path, the keys derive from the
phrase, the addresses derive from the keys, and the records come back from an indexer that never saw
a plaintext. A genuinely separate machine would additionally exercise a different clock and a
different route to the network.

**The approval round is not re-run.** A new device needs the phrase *and* one approval in a browser,
because the app key is derived from the phrase together with a secret the indexer issues. `sennit
connect` walks that path, it auto-renews the request when the indexer's ten-minute expiry beats the
human to it, and waits for the account to become writable afterwards, but it needs a person at a
browser, so the demo starts after it. **Do not describe this as seed-only recovery.**

**A rebuilt conversation is not the conversation that was saved.** The messages are exact. The head
that named them is not on Sia at all, so B reconstructs it from the transcript.

⚠️ **The four groups below are not fixed sizes.** Two fields move between them depending on the run,
not on the schema: `links.memories` is restored through the other record's edge **if some memory
links back**, and the embedding is invented **only if the rebuild embeds**, which a catalog-depth
hydrate does not. The live run on 2026-09-04 was index depth with no backlink and scored
**11 restored, 4 invented, 11 gone**. Quote the grouping with the conditions attached.

- **comes back exactly**, the record id, the schema, the chunk list, the message and chunk counts,
  the byte count, the last message
- **read out of the messages**, when it started and ended, which models spoke
- **invented here**, the title (taken from the first thing the user said), the head version, the
  session's class, and, when the rebuild embeds, the embedding
- **gone**, the summary, the tags, the project, the archived flag, the agent that wrote it, the
  token counts, the duration, the lineage, the preserved tail

`vault.RebuildHead` reports the origin of every field it returns, and
`TestAHeadRebuiltFromChunksIsMeasuredFieldByField` asserts the whole verdict, so a change that moves
a field between those four groups has to change the test.

## Recording it

Captures are deliberately **not** committed, they hard-code timings, a protocol revision and record
ids that go stale on the next change, and a stale recording is a claim the code no longer backs. Make
your own:

```sh
script --timing=demo/recordings/run.timing demo/recordings/run.typescript \
  -c ./demo/two-machines.sh

scriptreplay --timing demo/recordings/run.timing demo/recordings/run.typescript
```

⚠️ **Before recording anything, check that the phrase is not on screen.** The script never prints it,
never passes it as an argument and never writes it to a file, but a shell prompt showing
`SENNIT_PHRASE=...` from an earlier command, or a `set -x`, would put it in the recording
permanently. Read the capture before you share it, a terminal recording keeps whatever was on the
screen, and a phrase is the key to the whole vault.

## Cleaning up after a run

The demo releases its own storage on exit: it forgets what it wrote and then reclaims, in that order.
The order is not a preference, a slab is billed whole and comes back only once nothing live is left
in it, so reclaiming without forgetting frees exactly nothing. Set `SENNIT_DEMO_KEEP=1` to leave
the vaults in place for inspection, and remember that each run then costs 40 MiB of quota until you
reclaim it yourself.

## The pieces

- `two-machines.sh`, the sequence
- `agent/`, a Go MCP client standing in for the agent on machine A
- `cross-agent/read-vault.mjs`, the TypeScript MCP client that reads the vault on machine B, and
  checks the surface it finds while it is there
- `recordings/`, where your own captures land. Git-ignored, so it is empty in a fresh clone.
