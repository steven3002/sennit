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

The Go build cache and the model download both land in temporary space, and a full `/tmp` fails in
ways that read as compiler errors. Point them somewhere with room:

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
