# Troubleshooting

> Part of the [Sennit documentation](README.md).

## Go is installed but `go version` fails

Go is often somewhere other than `/usr/local/go`, a toolchain manager, a package manager, or a
per-user install under `~/.local`. Find it and put it on `PATH`:

```sh
command -v go || ls ~/.local/**/go/bin/go /usr/lib/go*/bin/go 2>/dev/null
export PATH="/path/to/go/bin:$PATH"
```

## The build fails with "no space left on device"

The Go build cache lands in temporary space, and a full `/tmp` fails in ways that read as compiler
errors. Point it somewhere with room:

```sh
export TMPDIR="$HOME/.tmp" && mkdir -p "$TMPDIR"
```

## "no recovery phrase"

```
 ERROR  no recovery phrase

 HINT   set SENNIT_PHRASE, or pipe the phrase in on stdin
 HINT   no phrase yet? `sennit init --new-phrase` prints one and
        stores nothing
```

**At a terminal you should not see this**, because a command with no phrase asks for one instead.
It appears when there is nobody to ask: stdin or stderr is redirected, which is the normal shape of
a script, a cron job or a CI step. Set `SENNIT_PHRASE` for those, or pipe the phrase in on stdin.

Every error is shaped like this: the problem, then what to do about it. Piped or redirected, the
same text arrives as `error:` and `hint:` lines with no colour.

## "no Sia app key"

You have not completed step 3, or the key is not in the environment. Run `sennit connect --out
sennit.key` and `export SENNIT_APP_KEY="$(cat sennit.key)"`. To work without an indexer
entirely, pass `--offline`.

## Boxes or question marks in the progress line

The line that counts up while a command works uses characters your terminal's font may not carry.
Nothing is wrong with the vault; pick a set your font has:

```sh
export SENNIT_GLYPHS=basic   # circles and dots, which almost every font carries
export SENNIT_GLYPHS=ascii   # | / - \ and plain ok and x
```

`SENNIT_GLYPHS=full` forces the full set back on. Colour and the live line follow the usual
conventions on their own: both turn off when the output is piped, redirected or in CI, `NO_COLOR`
turns colour off, `SENNIT_NO_PROGRESS=1` turns the live line into one plain line per step, and
`--color=always` forces colour on where you know it is safe.

## An error about hosts, or "not enough hosts"

The indexer has not finished funding host accounts. `sennit connect` waits for this; if you
skipped it, wait a minute and retry. The first write of any process waits for readiness on its own.

## "not found" or a 502 from the indexer

The hosted indexer sits behind a CDN that occasionally returns a gateway error. These are transient
and retryable; nothing is lost, because a failed flush leaves the records queued on the device.

## "the search index on this device was damaged, and has been rebuilt"

The search index is two files in the vault's directory, `index.base` and `index.delta`, and a part
of either that cannot be read loses the vectors it held. The records themselves are untouched, so
before the vault opens it gives a vector to every memory and conversation it holds that has none,
and says what it found and what it rebuilt. Nothing is asked of you.

Embedding takes a few hundred milliseconds a record, so the first open after the damage can take
minutes on a large vault, or on one restored by `sennit hydrate` at its default depth, which leaves
every record without a vector. The live line counts the records as it goes.

`sennit-mcp` rebuilds before it starts serving, and says so in the host's log. A host that stops
waiting for it loses none of the work: whatever was embedded keeps its vector, and the next run to
open the vault, the server or any `sennit` command, finishes the rest. `sennit status --offline` in
a terminal lets it finish while you watch.
