# Quickstart

> Part of the [Sennit documentation](README.md).

Roughly ten minutes, most of it waiting for one download and one approval click.

## What you need

- **About 300 MB of free disk**, mostly the ~130 MB embedding model. Building from source instead
  wants about 1 GB, for the Go build cache.
- **A browser**, once, to approve this installation with an indexer.
- **Go 1.26.5 or newer**, *only if you build from source* (`go.mod` sets it). If `go version` does
  not print it, see [Troubleshooting](troubleshooting.md#go-is-installed-but-go-version-fails).
- No Sia node. No wallet. No payment, the hosted indexer's free tier covers the quickstart.

## 1. Install

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

mkdir -p ~/.local/bin
mv sennit sennit-mcp ~/.local/bin/
sennit version
```

**Verify the checksum rather than skipping it.** These binaries derive and hold the keys to
everything you store, so "it downloaded, it is probably fine" is not a good enough standard for
them.

If `sennit version` reports *command not found*, `~/.local/bin` is not on your `PATH`. Add it, and
keep it for new shells:

```sh
export PATH="$HOME/.local/bin:$PATH"
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc     # or ~/.zshrc
```

Anywhere on your `PATH` works; `/usr/local/bin` is the other usual choice and needs `sudo`. On
Windows, put the two `.exe` files in a folder of your choosing and add that folder to `Path` under
*Edit environment variables for your account*.

> ⚠️ **macOS will refuse to open it the first time, and that is expected.** These binaries are
> unsigned and un-notarized, so Gatekeeper quarantines anything downloaded from a browser and says
> *"cannot be opened because the developer cannot be verified."* Nothing is wrong with the file.
> Clear the flag:
>
> ```sh
> xattr -d com.apple.quarantine ~/.local/bin/sennit ~/.local/bin/sennit-mcp
> ```
>
> Signed and notarized macOS builds are planned; see [What's changing](whats-changing.md). Until
> they ship, clearing the flag as above is the only step needed.

> ⚠️ **On Windows** the vault defaults to `C:\Users\<you>\.sennit`, which works but is a Unix
> convention rather than a native one. Set `SENNIT_HOME` if you would rather it lived elsewhere.

**Or build from source.** No cgo and no system libraries, so this works anywhere Go does:

```sh
git clone https://github.com/steven3002/sennit
cd sennit
CGO_ENABLED=0 go build -o ~/.local/bin/sennit ./cmd/sennit
CGO_ENABLED=0 go build -o ~/.local/bin/sennit-mcp ./cmd/sennit-mcp
```

The first build downloads dependencies and takes a few minutes. A binary you built yourself reports
`built from source` rather than a commit, because it is not claiming a provenance anyone can check.

## 2. Get a recovery phrase

```sh
sennit init -new-phrase
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

## 3. Approve this installation

Storing on Sia needs an **app key**, which an indexer issues after you approve it in a browser.

```sh
sennit connect -out sennit.key
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

## 4. Prepare the vault

```sh
sennit init
```

```
preparing vault in /home/you/.sennit
  keys derived, model loaded, device store ready in 9.00 s
  connected: https://sia.storage
  quota:     40.00 MiB used of 46.57 GiB (46.53 GiB free)
```

The first run downloads the embedding model (~130 MB, once).

## 5. Remember something

```sh
sennit remember \
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

## 6. Recall it by meaning

```sh
sennit recall "how is storage billed"
```

```
  embed 383 ms · search 0.74 ms over 1 vector(s) and 1 term match(es) · fetch 8 ms
1. [0.6257] Sia bills a slab whole, so packing many records into one slab is a cost decision rather than an optimisation.
   context: Recorded while checking the README quickstart from a clean environment.
   <record id> · fact · sia, storage · from local in 8 ms
```

Note the query shares no words with the record beyond "billed"/"bills", the match is semantic.

## 7. Connect an MCP client

`sennit-mcp` speaks MCP over stdio. For Claude Code:

```sh
claude mcp add sennit \
  -e SENNIT_PHRASE="$SENNIT_PHRASE" \
  -e SENNIT_APP_KEY="$SENNIT_APP_KEY" \
  -- "$(command -v sennit-mcp)"
```

**An MCP host needs the absolute path even though the binary is on your `PATH`**, because a host does
not launch the server from your shell and will not resolve `~` or search your `PATH`.
`command -v sennit-mcp` prints the full path, which is what to paste anywhere a config wants one.

Other hosts take a JSON config naming the same binary and the same two environment variables. We
have verified the command above on Claude Code 2.1.223; **we have not verified the config file
locations for Cursor, VS Code or Claude Desktop**, so this README does not guess at them.

The server exposes `remember`, `recall`, `browse`, `open`, `save_session` and `forget`, plus a
`/resume` prompt. Two clients can run against one vault at once, each in its own process.

## Trying it without Sia

Every command takes `-offline`, which uses the device's own copy and contacts no indexer. It needs
no app key and no approval, so it is the fastest way to see recall working:

```sh
sennit init -offline
sennit remember -offline -context "..." "..."
sennit recall -offline "..."
```

Records written offline stay on the device until a connected run flushes them.

---
