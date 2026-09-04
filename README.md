# Sennit

**User-owned storage for an AI's memory, sessions, and skills, encrypted, on the [Sia](https://sia.tech) network.**

Sennit gives an AI agent a long-term memory that belongs to **you** rather than to a vendor: encrypted on your device, stored on decentralized infrastructure that cannot read it, retrieved by meaning, and portable across apps, models and machines.

> **Status: beta.** The substrate, the CLI and the MCP server are built and measured against the live
> Sia network, and the [quickstart](#quickstart) below is tested end to end. **Prebuilt binaries are
> available**, see [`v0.1.0-beta-mvp`](https://github.com/steven3002/sennit/releases/tag/v0.1.0-beta-mvp).
> It is beta because it works and is measured, and because it has been used by very few people. See
> [Status](#status) for what is and is not proven.

---

## Why

An AI assistant's memory is trapped. It lives inside one provider, it can't move with you, and you can't inspect, own, or truly export it. To have it available everywhere, you normally hand it to a cloud that can read it.

Sennit takes the other path:

- **You own it.** Keys are derived from your recovery phrase and never leave your device.
- **Nobody can read it.** Records are encrypted client-side before they touch the network; storage providers and the coordinating indexer see only ciphertext.
- **It's portable.** Memory follows you across agents, tools and machines, not locked to one vendor.
- **It's searched by meaning.** Semantic recall over your own records, computed locally.

This is **storage plumbing**, not an AI assistant. "AI" describes the *data being stored*, AI apps and agents are consumers of it.

## How it works

Two record types today on one encrypted, content-addressed, versioned substrate, with a third designed for and not yet built:

| Record | What it holds | Retrieved by | Built? |
|---|---|---|---|
| `memory` | facts, preferences, learned context | **semantic** (vector) search | yes |
| `session` | past conversations you can resume | metadata + version | yes |
| `skill` | reusable procedures an agent can load | name + version | not yet |

Agents talk to it over **[MCP](https://modelcontextprotocol.io)** (Model Context Protocol), so any MCP-capable client can use the same memory.

**The search never leaves your machine.** Query embedding and vector search run locally against a local index; only opaque fetches of already-identified records hit the network. Nobody learns what you searched for.

```
remember ──▶ embed + encrypt locally ──▶ pack ──▶ Sia
recall   ──▶ embed query locally ──▶ local vector search ──▶ fetch those records ──▶ decrypt locally
```

---

## Quickstart

Roughly ten minutes, most of it waiting for one download and one approval click.

### What you need

- **About 300 MB of free disk**, mostly the ~130 MB embedding model. Building from source instead
  wants about 1 GB, for the Go build cache.
- **A browser**, once, to approve this installation with an indexer.
- **Go 1.26.5 or newer**, *only if you build from source* (`go.mod` sets it). If `go version` does
  not print it, see [Troubleshooting](#go-is-installed-but-go-version-fails).
- No Sia node. No wallet. No payment, the hosted indexer's free tier covers the quickstart.

### 1. Install

**Download a release.** Pick the archive for your machine from
[`v0.1.0-beta-mvp`](https://github.com/steven3002/sennit/releases/tag/v0.1.0-beta-mvp). `amd64` is an
ordinary Intel or AMD machine; `arm64` on macOS is any Apple Silicon Mac, an M1 or later.

```sh
tag=v0.1.0-beta-mvp
file=sennit_0.1.0-beta-mvp_linux_amd64.tar.gz     # change to match your platform
base=https://github.com/steven3002/sennit/releases/download/$tag

curl -sSLO "$base/$file"
curl -sSLO "$base/SHA256SUMS"
sha256sum -c SHA256SUMS --ignore-missing          # macOS: shasum -a 256 -c

tar xzf "$file"
cd "${file%.tar.gz}"
./sennit version
```

**Verify the checksum rather than skipping it.** These binaries derive and hold the keys to
everything you store, so "it downloaded, it is probably fine" is not a good enough standard for
them.

> ⚠️ **macOS will refuse to open it the first time, and that is expected.** These binaries are
> unsigned and un-notarized, so Gatekeeper quarantines anything downloaded from a browser and says
> *"cannot be opened because the developer cannot be verified."* Nothing is wrong with the file.
> Clear the flag:
>
> ```sh
> xattr -d com.apple.quarantine ./sennit ./sennit-mcp
> ```
>
> Signing this properly needs a paid Apple Developer ID, which is not worth it at beta.

> ⚠️ **On Windows** the vault defaults to `C:\Users\<you>\.sennit`, which works but is a Unix
> convention rather than a native one. Set `SENNIT_HOME` if you would rather it lived elsewhere.

**Or build from source.** No cgo and no system libraries, so this works anywhere Go does:

```sh
git clone https://github.com/steven3002/sennit
cd sennit
CGO_ENABLED=0 go build -o sennit ./cmd/sennit
CGO_ENABLED=0 go build -o sennit-mcp ./cmd/sennit-mcp
```

The first build downloads dependencies and takes a few minutes. A binary you built yourself reports
`built from source` rather than a commit, because it is not claiming a provenance anyone can check.

### 2. Get a recovery phrase

```sh
./sennit init -new-phrase
```

It prints twelve words and stores nothing. Yours will differ from every example in this file,
the words below stand in for a real phrase and are not a working one:

```
<word1> <word2> <word3> ... <word12>
```

> ⚠️ **This phrase is the vault.** Anyone holding it can read every memory, and losing it loses the
> data, there is no reset, because nobody else ever has the key. Write it down somewhere durable
> before continuing.

Put it in your environment. It is read on every run and never written to disk:

```sh
export SENNIT_PHRASE="<the twelve words init printed>"
```

### 3. Approve this installation

Storing on Sia needs an **app key**, which an indexer issues after you approve it in a browser.

```sh
./sennit connect -out sennit.key
```

It prints a link. Open it, approve, and come back:

```
Connecting to https://sia.storage
This needs one approval in a browser. The link below expires after about ten
minutes; a fresh one is issued automatically until you approve or the budget runs out.

  approve this: https://sia.storage/approve?...

  waiting...
```

Then load the key it wrote:

```sh
export SENNIT_APP_KEY="$(cat sennit.key)"
```

**Two things about this step are worth knowing in advance**, because both look like failures:

- **The link expires after about ten minutes.** If you go and find a browser and the link is dead,
  `connect` has already issued a fresh one, use the newest link it printed.
- **Approval is not readiness.** After you approve, the indexer funds host accounts, which took
  **~16 s** when we measured it. `connect` waits for that on your behalf. If you skip `connect` and
  write immediately, the write fails with an error about *hosts*, which says nothing about waiting.

`sennit.key` is a secret. It is written `0600` and it belongs in `.gitignore`.

### 4. Prepare the vault

```sh
./sennit init
```

```
preparing vault in /home/you/.sennit
  keys derived, model loaded, device store ready in 9.00 s
  connected: https://sia.storage
  quota:     40.00 MiB used of 46.57 GiB (46.53 GiB free)
```

The first run downloads the embedding model (~130 MB, once).

### 5. Remember something

```sh
./sennit remember \
  -context "Recorded while checking the README quickstart from a clean environment." \
  -tags "sia,storage" \
  "Sia bills a slab whole, so packing many records into one slab is a cost decision rather than an optimisation."
```

```
<record id>
  cid       <content id>
  embed     940 ms
  seal      1 ms
  on Sia    420 B in 1 object(s), 1 slab(s)
            upload 4.49 s · pin slabs 196 ms · pin objects 158 ms
```

> **`-context` is required, and flags come before the text.** The context is what makes a statement
> findable once it is separated from the conversation it came from; leaving it out measurably costs
> retrieval quality, and records are immutable, so it cannot be added later. And because Go's flag
> parsing stops at the first plain word, `remember "..." -offline` reads `-offline` as part of your
> sentence.

### 6. Recall it by meaning

```sh
./sennit recall "how is storage billed"
```

```
  embed 383 ms · search 0.74 ms over 1 vector(s) and 1 term match(es) · fetch 8 ms
1. [0.6257] Sia bills a slab whole, so packing many records into one slab is a cost decision rather than an optimisation.
   context: Recorded while checking the README quickstart from a clean environment.
   <record id> · fact · sia, storage · from local in 8 ms
```

Note the query shares no words with the record beyond "billed"/"bills", the match is semantic.

### 7. Connect an MCP client

`sennit-mcp` speaks MCP over stdio. For Claude Code:

```sh
claude mcp add sennit \
  -e SENNIT_PHRASE="$SENNIT_PHRASE" \
  -e SENNIT_APP_KEY="$SENNIT_APP_KEY" \
  -- /absolute/path/to/sennit-mcp
```

Other hosts take a JSON config naming the same binary and the same two environment variables. We
have verified the command above on Claude Code 2.1.223; **we have not verified the config file
locations for Cursor, VS Code or Claude Desktop**, so this README does not guess at them.

The server exposes `remember`, `recall`, `open`, `forget`, `save_session`, `resume_session` and a
`/resume` prompt. Two clients can run against one vault at once, each in its own process.

### Trying it without Sia

Every command takes `-offline`, which uses the device's own copy and contacts no indexer. It needs
no app key and no approval, so it is the fastest way to see recall working:

```sh
./sennit init -offline
./sennit remember -offline -context "..." "..."
./sennit recall -offline "..."
```

Records written offline stay on the device until a connected run flushes them.

---

## "Saved" is not "on Sia"

Sennit distinguishes the two everywhere, because the gap between them is real and can be up to an
hour under the standing flush cadence:

- **On this device**, the record is sealed and durable locally the moment `remember` returns.
- **On Sia**, the record is on the network, and only a completed flush puts it there.

`remember` says which it achieved (`on Sia 420 B in 1 object(s)` versus `on Sia not yet, held on
this device, 1 record(s) queued`), `sennit status` reports what is still queued, and `sennit
flush` closes the gap on demand. **No output claims durability on Sia before a flush has
completed.**

## Storage, quota and reclaiming

Sia bills a **slab** whole, 40 MiB, whether it holds one record or a thousand, and a partly filled
slab can never be extended. So every flush strands a slab, and an account fills up over months no
matter how little you actually store.

```sh
./sennit status          # what is held, what is queued, what is billed
./sennit reclaim         # release storage nothing points at any more
./sennit reclaim -repack # rewrite live records into fewer slabs first
```

`status` warns you once reclaimable storage has built up, rather than leaving you to find out when a
write fails. Reclamation is two-phase and refuses to run on a keep-set that does not account for
everything the vault holds, an empty keep-set means the computation is wrong, not that the data is
dead.

**This is also the only thing that costs money.** Reclaimed, the free tier holds around **57 million
records**; written one record per object it holds **1,250**. Beyond the free tier, storing 100,000
memories for a year is about a third of a cent. The arithmetic, both price sources, and a plain
statement of what has and has not actually been paid for are in [`docs/cost.md`](docs/cost.md).

---

## Status

**Working, and measured against live Sia:**

- End-to-end round-trip, byte-exact, with client-side encryption
- A thousand records written into one storage slab, and any one of them read back on its own
- A vault rebuilt from the recovery phrase and the indexer with its catalog deleted
- A vault **hydrated onto a machine that has never held it**, and read there
- Reclamation that returns exactly the space it should and nothing that is still in use
- Repack that rewrites every storage location without disturbing a single record identity, including
  **while a second process is writing to the same vault**
- An interrupted repack loses no data
- Three-tier reads, from the device's own copy through a cached location to a cold fetch
- A conversation saved on one machine and replayed **byte for byte** on another
- An MCP server two different clients, on two different protocol revisions, drive at once

**Not yet built:** the `skill` record type, the local viewer, session forking.

**Stated plainly, because it would be easy to imply otherwise:**

- **A conversation comes back exactly. The label on it does not.** Replayed on a second machine, every
  message is byte for byte what was saved, including each tool call and the result it was correlated
  with. What is *not* on Sia is the head that described the conversation. Of its 26 fields, **11 come
  back from the transcript itself**, and where the rest land depends on the rebuild rather than on
  the schema: a rebuild at index depth with no memory linking back to the conversation reconstructs
  **4** and loses **11**, the summary, tags, project and lineage among them. A linked memory restores
  `links.memories` through the other record's edge, and a catalog-depth rebuild leaves the embedding
  unset, so **do not read those two numbers as fixed.** Measured on a live two-machine run,
  2026-09-04. **The vault reports the origin of every field it returns**, rather than handing back a
  rebuilt head as though it were the original.
- **Opening a vault on a second machine needs the phrase *and* one browser approval.** The phrase
  alone is not sufficient, and we do not claim seed-only recovery.
- **Cost figures we quote are advertised rates, not invoices.** The free tier has covered everything
  so far; we have never paid a bill. The arithmetic, its sources and its limits are in
  [`docs/cost.md`](docs/cost.md).
- **Recall quality numbers come from synthetic corpora**, not from a real personal vault.

## Troubleshooting

#### Go is installed but `go version` fails

Go is often somewhere other than `/usr/local/go`, a toolchain manager, a package manager, or a
per-user install under `~/.local`. Find it and put it on `PATH`:

```sh
command -v go || ls ~/.local/**/go/bin/go /usr/lib/go*/bin/go 2>/dev/null
export PATH="/path/to/go/bin:$PATH"
```

#### The build fails with "no space left on device"

The Go build cache and the model download both land in temporary space, and a full `/tmp` fails in
ways that read as compiler errors. Point them somewhere with room:

```sh
export TMPDIR="$HOME/.tmp" && mkdir -p "$TMPDIR"
```

#### "no recovery phrase"

Set `SENNIT_PHRASE`, or pipe the phrase on stdin. If you do not have one, `sennit init
-new-phrase` prints one and stores nothing.

#### "no Sia app key"

You have not completed step 3, or the key is not in the environment. Run `sennit connect -out
sennit.key` and `export SENNIT_APP_KEY="$(cat sennit.key)"`. To work without an indexer
entirely, pass `-offline`.

#### An error about hosts, or "not enough hosts"

The indexer has not finished funding host accounts. `sennit connect` waits for this; if you
skipped it, wait a minute and retry. The first write of any process waits for readiness on its own.

#### "not found" or a 502 from the indexer

The hosted indexer sits behind a CDN that occasionally returns a gateway error. These are transient
and retryable; nothing is lost, because a failed flush leaves the records queued on the device.

## Design principles

1. **Storage consumer, not AI operator**, Sennit puts data *on* Sia; it does not drive Sia with an LLM.
2. **Client-side confidentiality**, encryption, keys and search stay on the device.
3. **User-owned and portable**, memory follows the user across apps, models and devices.
4. **With the grain of Sia**, built on the first-party SDK and indexer.
5. **No LLM in our stack**, Sennit embeds, stores, indexes and ranks. The calling agent decides what is worth remembering.

## Built on

- [Sia](https://sia.tech), decentralized storage with client-side encryption and user-held keys
- [`go.sia.tech/siastorage`](https://pkg.go.dev/go.sia.tech/siastorage), the first-party Go SDK
- [Model Context Protocol](https://modelcontextprotocol.io), the open standard for connecting data to AI applications

## Reporting a problem in the beta

This is the point of a beta, so a report of something being confusing is as useful as a report of
something crashing. **Include the output of `sennit version`**, because a bug report that cannot name
a build cannot be reproduced:

```
sennit 0.1.0-beta-mvp (3615850, 2026-09-04)
```

⚠️ **Never paste your recovery phrase into an issue.** It is the key to every memory in the vault.
Nothing in this project ever needs it in order to help you, and no output here prints it.

**Two checks are worth more than any bug report**, because they cover the two things automated tests
cannot reach. Both are scripted in [`docs/host-checks.md`](docs/host-checks.md) and take about ten
minutes: whether the `/resume` prompt shows up as a slash command in your MCP host, and **whether an
agent reading only the tool descriptions supplies usable tags.** The second one decides whether this
project's retrieval quality claim survives contact with a real agent, and it has never been run by
anyone who did not write the code.

## Contributing

Early and in flux. See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and design discussion are
welcome; please open an issue before a large pull request.

## Security

Sennit handles encryption keys and personal data. Please report vulnerabilities responsibly, see [SECURITY.md](SECURITY.md). Do not open public issues for security problems.

## License

[MIT](LICENSE), matching the Sia SDK and the wider Go ecosystem.
