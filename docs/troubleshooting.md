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

You have not completed step 3 as this account. Run `sennit connect`: it approves this installation
and stores the key, with the phrase, for every later run. To work without an indexer entirely, pass
`--offline`.

## A command waits half a minute, then says the credential store did not answer

The phrase and the key are stored in your operating system's credential store, and it did not
answer within 30 seconds. On Linux that is almost always a Secret Service keyring that is locked,
with nobody there to unlock it, as in an SSH session to a desktop machine. Unlock the keyring in the
desktop session, or set `SENNIT_PHRASE` and `SENNIT_APP_KEY` for the run, which are read first.

## Removing the stored phrase and key

There is no command for it yet. **Write the phrase down first**: if the stored copy is the only
one, removing it loses the vault. Then remove two items, `default/phrase` and `default/app-key`,
filed under `sennit`:

- **Windows:** in Credential Manager, under Windows Credentials, the two generic credentials named
  `sennit:default/phrase` and `sennit:default/app-key`. Or `cmdkey /delete:sennit:default/phrase`
  and `cmdkey /delete:sennit:default/app-key`.
- **macOS:** in Keychain Access, the two items named `sennit`. Or `security delete-generic-password
  -s sennit -a default/phrase`, and the same with `-a default/app-key`.
- **Linux:** in your desktop's keyring manager, or `secret-tool clear service sennit username
  default/phrase`, and the same for `default/app-key`. Where they were kept in a file, delete
  `~/.config/sennit/secrets.json`.

With both gone, Sennit treats the profile as removed and asks for the phrase again.

## Boxes or question marks in the progress line

The line that counts up while a command works uses characters your terminal's font may not carry.
Nothing is wrong with the vault; pick a set your font has:

```sh
export SENNIT_GLYPHS=basic   # circles and dots, which almost every font carries
export SENNIT_GLYPHS=ascii   # | / - \ and plain ok and x
```

```powershell
$env:SENNIT_GLYPHS = "basic"
$env:SENNIT_GLYPHS = "ascii"
```

`SENNIT_GLYPHS=full` forces the full set back on. Colour and the live line follow the usual
conventions on their own: both turn off when the output is piped, redirected or in CI, `NO_COLOR`
turns colour off, `SENNIT_NO_PROGRESS=1` turns the live line into one plain line per step, and
`--color=always` forces colour on where you know it is safe.

## `remember` says the upload failed

The memory is stored on this device and stays queued for Sia; only its upload failed. A plain
`remember` succeeds and warns, and `remember --flush` exits with an error, because the upload it was
asked to wait for did not happen. Either way, do not run `remember` again, which would store a second
copy: `sennit flush` retries the upload. The next two entries cover the errors an upload most often
gives.

## An error about hosts, or "not enough hosts"

The indexer has not finished funding host accounts. `sennit connect` waits for this; if you
skipped it, wait a minute and retry. The first write of any process waits for readiness on its own.

## "not found" or a 502 from the indexer

The hosted indexer sits behind a CDN that occasionally returns a gateway error. These are transient
and retryable; nothing is lost, because a failed flush leaves the records queued on the device.

## "the vault's folder can be read by other accounts on this machine"

The vault is kept in a folder that was there before Sennit, and other accounts on this machine can
look inside it. Sennit keeps every file it makes there for your account alone, but the folder shows
what is there and how large it is, and a file any other program puts in it takes the folder's
access. Sennit leaves a folder it did not make as it was set, since it may be shared on purpose.

To make it yours alone, on Linux or macOS run `chmod 700` on the folder. On Windows, open the
folder's Properties, and under Security remove every account but yours. A folder Sennit makes itself,
such as the default `~/.sennit`, is yours alone from the start.

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
