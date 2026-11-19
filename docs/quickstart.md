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

On Windows, in PowerShell:

```powershell
$tag  = "v0.1.0-beta-mvp"
$file = "sennit_0.1.0-beta-mvp_windows_amd64.zip"
$base = "https://github.com/steven3002/sennit/releases/download/$tag"

Invoke-WebRequest "$base/$file" -OutFile $file
Invoke-WebRequest "$base/SHA256SUMS" -OutFile SHA256SUMS

# The next two lines must print the same thing.
(Get-FileHash $file -Algorithm SHA256).Hash.ToLower()
(Select-String -Path SHA256SUMS -Pattern $file -SimpleMatch).Line.Split(" ")[0]

Expand-Archive $file -DestinationPath .
$dir = "$env:LOCALAPPDATA\Programs\sennit"
New-Item -ItemType Directory -Force $dir | Out-Null
Move-Item ".\$($file -replace '\.zip$','')\*.exe" $dir
$env:Path += ";$dir"
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

Anywhere on your `PATH` works; `/usr/local/bin` is the other usual choice and needs `sudo`.

On Windows the lines above put the two `.exe` files in `%LOCALAPPDATA%\Programs\sennit` and add
that folder to `Path` for the window they ran in. To keep it for new windows:

```powershell
[Environment]::SetEnvironmentVariable("Path",
  [Environment]::GetEnvironmentVariable("Path", "User") + ";$env:LOCALAPPDATA\Programs\sennit", "User")
```

Any folder works there too, and *Edit environment variables for your account* in Settings does the
same thing by hand.

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

In PowerShell the last two lines are:

```powershell
$env:CGO_ENABLED = "0"
go build -o "$env:LOCALAPPDATA\Programs\sennit\sennit.exe" ./cmd/sennit
go build -o "$env:LOCALAPPDATA\Programs\sennit\sennit-mcp.exe" ./cmd/sennit-mcp
```

The first build downloads dependencies and takes a few minutes. A binary you built yourself reports
`built from source` rather than a commit, because it is not claiming a provenance anyone can check.

## 2. Get a recovery phrase

```sh
sennit init --new-phrase
```

It prints twelve words to stdout and stores nothing, and says what they are worth on stderr, so a
script that captures the phrase captures the phrase alone. Yours will differ from every example in
this file; the words below stand in for a real phrase and are not a working one:

```
<word1> <word2> <word3> ... <word12>

 WARNING  this phrase is the vault: anyone holding it can read
          every memory, and losing it loses the data
  Nothing has stored it. `sennit connect` keeps a copy for this
  account, but that copy is lost with this machine: write the
  phrase down.
```

> ⚠️ **This phrase is the vault.** Anyone holding it can read every memory, and losing it loses the
> data, there is no reset, because nobody else ever has the key. Write it down somewhere durable
> before continuing. The copy step 3 stores is on this machine, and goes with it.

Every command needs it. **Step 3 stores it**, with the app key, and every later command finds it
there. Until then, **at a terminal you do not have to supply it in advance:** a command that finds
no phrase asks for one, and nothing you type is echoed.

```
Recovery phrase:
```

To run without being asked, in a script or anywhere the typing would get in the way, put it in the
environment instead:

```sh
export SENNIT_PHRASE="<the twelve words init printed>"
```

```powershell
$env:SENNIT_PHRASE = "<the twelve words init printed>"
```

In cmd it is `set SENNIT_PHRASE=<the twelve words init printed>`, with no quotes, because cmd
would keep them as part of the phrase.

The environment comes first, before the phrase step 3 stores as well.

Or pipe it in, and pass `--phrase-stdin` to have it read:

```sh
sennit status --phrase-stdin < phrase.txt
```

PowerShell has no `<`, so there the file is piped:

```powershell
Get-Content phrase.txt | sennit status --phrase-stdin
```

Stdin is read only when `--phrase-stdin` asks for it, so a command inside a script or an ssh session
never takes the script's next line for a phrase. The flag comes before the phrase step 3 stores, so
it is also how to use another phrase once one is stored. Use it or `SENNIT_PHRASE`, not both: given
the two together, a command refuses rather than guess which phrase you meant.

The environment is the least private of the three, because every child process a command starts
inherits it. Typing it when asked keeps it out of your shell's history and out of the process
table.

## 3. Approve this installation

Storing on Sia needs an **app key**, which an indexer issues after you approve it in a browser.

```sh
sennit connect
```

It prints a link and then waits on one line, counting up. Open the link, approve, and come back:

```
Connecting to https://sia.storage
This needs one approval in a browser. The link below expires after about ten
minutes; a fresh one is issued automatically until you approve or the budget runs out.

  approve this: https://sia.storage/approve?...

◍  Waiting for approval… (48s · ctrl+c to cancel)
```

When you approve, it registers this installation, waits for the account to become usable, and ends:

```
✓ Connected: app key 3f9a1c2b… stored in Windows Credential Manager (1m42s)

Approved  after 1m12s, over 1 request
Ready     after 16s, 40.00 MiB of 46.57 GiB quota in use

 HINT   every sennit command and sennit-mcp run as this account
        now find the phrase and the key there, so a shell needs
        nothing set and an MCP host needs only sennit-mcp's path
```

**The phrase and the key are now stored as the default profile**, in your operating system's
credential store: Credential Manager on Windows, the login keychain on macOS, the Secret Service
keyring on Linux. Every later `sennit` command and `sennit-mcp` read them from there, so nothing
needs setting in a new shell. The indexer that issued the key is kept beside them.

**Where there is no credential store**, as on a Linux server with no desktop, they are kept in a file
only your account can open, `~/.config/sennit/secrets.json`, and `connect` says so. A file promises
less than a credential store: anyone who can act as your account on that machine can read it.

`SENNIT_PHRASE` and `SENNIT_APP_KEY` still work, and come before what is stored, so a script can
supply its own. `sennit status` says where the phrase and the key it used came from.

**For scripts and CI**, `sennit connect --out sennit.key` writes the key to a file instead and stores
nothing. It ends with the line that loads that file into your shell, for each shell you might be in.
The file is a secret: it is written for your account alone, and it belongs in `.gitignore`.

**Two things about this step are worth knowing in advance**, because both look like failures:

- **The link expires after about ten minutes.** If you go and find a browser and the link is dead,
  `connect` has already issued a fresh one, use the newest link it printed.
- **Approval is not readiness.** After you approve, the indexer funds host accounts, which took
  **~16 s** when we measured it. `connect` waits for that on your behalf. If you skip `connect` and
  write immediately, the write fails with an error about *hosts*, which says nothing about waiting.

## 4. Prepare the vault

```sh
sennit init
```

```
✓ Vault ready (10.2s)

Vault    /home/you/.sennit
Indexer  https://sia.storage
Quota    80.00 MiB used of 46.57 GiB (46.49 GiB free)
```

While it works it shows one line saying what it is waiting on, which is worth watching the first
time: most of those ten seconds are spent connecting to Sia's hosts. The first run also downloads
the embedding model (~130 MB, once), and says so.

## 5. Remember something

```sh
sennit remember \
  --context "Recorded while checking the README quickstart from a clean environment." \
  --tags "sia,storage" \
  "Sia bills a slab whole, so packing many records into one slab is a cost decision rather than an optimisation."
```

PowerShell continues a line with a backtick where a POSIX shell uses a backslash:

```powershell
sennit remember `
  --context "Recorded while checking the README quickstart from a clean environment." `
  --tags "sia,storage" `
  "Sia bills a slab whole, so packing many records into one slab is a cost decision rather than an optimisation."
```

```
✓ Remembered on this device, queued for Sia (0.8s)
<record id>
```

**The memory is on this device, and it is not on Sia yet.** `remember` seals the record here and
returns; the upload happens later. Sia bills a slab whole, 40 MiB, so uploading one memory of a few
hundred bytes on its own would buy a whole slab for it. `sennit status` says how much is waiting and
`sennit flush` uploads it, which is the subject of [Storage](storage.md).

A connected run takes longer than the time above: every online command first contacts the indexer,
and that is most of the wall time you see in the next step.

The line above the id counts up while the write happens. The id is the only thing on stdout, so
`id=$(sennit remember ...)` captures it and nothing else, as does `$id = sennit remember ...` in
PowerShell. `--verbose` adds the content id and the
tag counts.

To wait for the upload on one write, add `--flush`. It shows a percentage while the upload runs and
ends with the only wording that means the record is on Sia:

```
✓ Remembered, and stored on Sia (24.4s)
```

> **`--context` is required, and flags come before the text.** The context is what makes a statement
> findable once it is separated from the conversation it came from; leaving it out measurably costs
> retrieval quality, and records are immutable, so it cannot be added later. And because Go's flag
> parsing stops at the first plain word, `remember "..." --offline` reads `--offline` as part of your
> sentence.

## 6. Recall it by meaning

```sh
sennit recall "how is storage billed"
```

```
✓ Found 1 memory (11.1s)

MATCH  TYPE  MEMORY                                                TAGS
0.63   fact  Sia bills a slab whole, so packing many records in…   sia, storage
```

Note the query shares no words with the record beyond "billed"/"bills", the match is semantic.

**To see a result in full**, with its context and the id a supersession needs, add `--memory-text`:

```
MATCH  TYPE  MEMORY                                                TAGS
0.63   fact  Sia bills a slab whole, so packing many records into  sia, storage
             one slab is a cost decision rather than an
             optimisation.
             context  Recorded while checking the README quickstart from a
                      clean environment.
             id       a02e046a088c93ee953033588b317b1c
```

Piped into another program the same command prints one tab-separated line per record, with nothing
cut off and no colour, and `--json` prints the whole record in the shape the MCP server answers
with. `--verbose` adds the timings and which tier served each read.

## 7. Connect an MCP client

`sennit-mcp` speaks MCP over stdio. For Claude Code:

```sh
claude mcp add sennit -- "$(command -v sennit-mcp)"
```

```powershell
claude mcp add sennit -- (Get-Command sennit-mcp).Source
```

**In Claude Code, its own memory can take "remember" before Sennit does.** Claude Code keeps a
memory of its own, on by default, and its [documentation](https://code.claude.com/docs/en/memory)
says that when you ask it to remember something, it saves it there. In our checks with that memory
on ([host checks](host-checks.md)), ten requests to remember something in one Claude Code 2.1.325
session all went to it, and none reached the vault. With 2.1.328 and a fresh session for each
request, 2 of 4 requests that did not name Sennit reached the vault, and all 4 that named it did.
Sennit does not change Claude Code's settings, so the choice is yours:

- **Turn Claude Code's memory off**, and "remember" goes to Sennit: encrypted on this device before
  it is stored on Sia, and there for any other MCP client you connect to the vault. Claude Code's
  own memory stays on the machine it was written on. To turn it off, open `/memory` in a Claude
  Code session and use its auto memory toggle, which "saves `autoMemoryEnabled` to your user
  settings", and [user settings](https://code.claude.com/docs/en/settings) apply in every project
  on this machine. To turn it off for one project only, set `"autoMemoryEnabled": false` in that
  project's Claude Code settings.
- **Or keep it on, and name Sennit when you ask**, as in "remember in Sennit that the staging
  database is reset every Monday".

**No secret goes in the host's configuration.** The server reads the phrase and the key from the
profile step 3 stored, as long as the host runs it as the same account. To give a server other
credentials, set `SENNIT_PHRASE` and `SENNIT_APP_KEY` in its environment, which in a host's MCP
config is the server's `env` block; they come before what is stored. The server's log says which it
used.

**An MCP host needs the absolute path even though the binary is on your `PATH`**, because a host does
not launch the server from your shell and will not resolve `~` or search your `PATH`.
`command -v sennit-mcp` prints the full path, which is what to paste anywhere a config wants one. In
PowerShell that is `(Get-Command sennit-mcp).Source`.

Other hosts take a JSON config naming the same binary and the same two environment variables. We
have verified the registration command above on Claude Code 2.1.223 with both secrets passed
in its environment; the form without them has not yet been run against a host.
**We have not verified the config file locations for Cursor, VS Code or Claude Desktop**, so this
README does not guess at them.

The server exposes `remember`, `recall`, `browse`, `open`, `save_session` and `forget`, plus a
`/resume` prompt. Two clients can run against one vault at once, each in its own process.

## Trying it without Sia

`init`, `remember`, `recall` and `status` work with `--offline`, which uses the device's own copy and
contacts no indexer. It needs no app key and no approval, so it is the fastest way to see recall
working:

```sh
sennit init --offline
sennit remember --offline --context "..." "..."
sennit recall --offline "..."
```

Records written offline stay on the device until a connected run flushes them. The commands that
move data to or from Sia need the indexer.

---
