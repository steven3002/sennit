# Connecting Sennit to an MCP host, and checking it works

Sennit's automated tests drive the server with a real MCP client over a real stdio transport, so
the protocol surface is covered. **Two things they cannot cover**, because both live above the wire:

1. **Does the `/resume` prompt render as a slash command** in a host's user interface?
2. **Does a real agent supply usable tags**, given the tool descriptions as written?

Both need a person at an interactive session. This page is the script. It takes about ten minutes,
and there is a results table at the end to fill in.

---

## 0. Build and configure

```bash
CGO_ENABLED=0 go build -o ~/bin/sennit-mcp ./cmd/sennit-mcp
```

The server takes **no arguments**. Both secrets come from its environment, never from a flag or a
tool argument:

| Variable | What it is |
|---|---|
| `SENNIT_PHRASE` | Your BIP-39 recovery phrase. Keys are derived from it on every run and never stored. |
| `SENNIT_APP_KEY` | The Sia app key issued when you approved this installation with your indexer. |
| `SENNIT_HOME` | Optional. The vault directory (default `~/.sennit`). |
| `SENNIT_MODEL_DIR` | Optional. Where the embedding model is kept. |

> The phrase cannot be piped in here, unlike the `sennit` command line. **stdin carries the
> protocol**, so a phrase on stdin would be read as the client's first message.

Check it runs before involving a host. This writes nothing and spends no storage:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"probe","version":"1"},"capabilities":{}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
| ~/bin/sennit-mcp | head -2
```

A working server prints one line naming the negotiated protocol version and one listing six tools,
and writes `sennit-mcp: serving a vault on …` to stderr. If it reports having no vault, it will
still start and every tool will tell you what is missing, that is deliberate, so a misconfigured
server explains itself instead of failing to launch.

### Adding it to a host

Every host below launches a **stdio** server: a command, no arguments, and two environment
variables. The mechanism differs per host and each one's own documentation is authoritative.

**Claude Code**, verified on this machine against **2.1.223**:

```bash
claude mcp add sennit \
  -e SENNIT_PHRASE="…" \
  -e SENNIT_APP_KEY="…" \
  -- ~/bin/sennit-mcp

claude mcp list          # should show sennit as connected
claude mcp get sennit  # health-checks it
```

**Claude Desktop, Cursor, VS Code**, each takes a JSON entry of the same shape. Use the host's own
current documentation for the file's location and exact key names; the entry itself is:

```json
{
  "command": "/absolute/path/to/sennit-mcp",
  "args": [],
  "env": {
    "SENNIT_PHRASE": "…",
    "SENNIT_APP_KEY": "…"
  }
}
```

⚠️ Use an **absolute path**. A host does not launch the server from your shell, so `~` and anything
on your `PATH` may not resolve.

⚠️ This file will contain your recovery phrase. It is the key to everything in the vault. Do not
commit it, and do not put it in a repository-level config that gets shared.

---

## Check 1, does `/resume` render as a slash command?

**Why it matters.** The headline of the product is *open a different agent and carry on*. If the
prompt renders, that is one keystroke. If it does not, the same conversation is still reachable,
the agent can call `recall` scoped to sessions and then `open`, but it stops being deterministic
and stops being the user's own decision. The surface is deliberately not built so that anything
depends on the prompt rendering; this check decides how the demo is framed, not whether it works.

**Give it something to resume first.** In any host that has Sennit connected, in a fresh session:

> Save this conversation to my memory with the title "Host check" and a one-line summary.

Then:

### In each host, in a **new** session

1. Type `/` and look for **`resume`** in the command list. It may be namespaced, hosts commonly
   prefix MCP prompts with the server name, so look for `resume`, `sennit:resume`, or
   `/mcp__sennit__resume`.
2. Run it **with no arguments**. It should resume the most recent conversation.
3. Run it again with a topic, e.g. `resume host check`.

**A pass looks like:**

- The prompt appears in the list without being typed out in full.
- Running it fills the input with, or sends, a block that begins
  *"Resume this conversation from the user's own Sennit vault"* and names how the conversation was
  chosen.
- The assistant answers as though it had been in that conversation, it can say what the
  conversation was about without calling a tool first.

**A partial pass**, worth recording separately: the prompt exists but only through a menu, or only
by typing its full name, or the arguments cannot be supplied.

**A fail:** no prompt appears anywhere, even though `mcp list`/the host's own inspector shows the
server connected. Record what the host *does* show.

### Record, per host

| Host | Version | Prompt visible? | How it is named | Arguments work? | Notes |
|---|---|---|---|---|---|
| Claude Code | | | | | |
| Claude Desktop | | | | | |
| Cursor | | | | | |
| VS Code | | | | | |

---

## Check 2, does a real agent supply usable tags?

**Why it matters, and this is the more important of the two.** Sennit's retrieval quality rests on
the agent emitting two or three accurate tags at both ends: on the write, so the record is
reachable, and on the query, so the mechanism has something to prefer. On the shipped pipeline that
soft filter is worth about five points of hit@5 and the lexical pass is worth nothing at all, which
means **the filter is the entire difference between dense-only retrieval and the quality claim.**

Everything measured so far was filtered by a model that had already read the whole corpus. Whether
an agent reading only the tool description does as well is **unknown**, and this check is how it
stops being unknown.

Filtering here only ever *prefers*, so a wrong tag costs ranking quality and never an answer. That
is why the check measures tag **quality**, not whether anything breaks.

### The script

Use a **fresh session** so nothing is primed. Do not mention tags, types, or how the vault works,
the whole point is to see what the agent does with the tool description alone.

**Step 1, ten writes.** Say each of these to the agent, one at a time, as though in passing:

1. "Remember that our reading rollup runs hourly, at ten past the hour."
2. "Remember I prefer review comments phrased as questions, not instructions."
3. "Remember that the dashboard reads from the rollup table and never from the raw feed."
4. "Remember the north pier gauge is the one we trust when two gauges disagree."
5. "Remember that Ana owns the ingest pipeline and Dele owns the dashboard."
6. "Remember we decided against caching the tide predictions, it was never the bottleneck."
7. "Remember the staging indexer is rebuilt every Sunday night."
8. "Actually, I was wrong earlier, the rollup runs at ten past, not on the hour. Remember that
   correction."
9. "Remember that deploys are blocked while a rollup is in flight."
10. "Remember the harbour survey found silting at the eastern approach."

**Record for each write**, from the tool call the host shows you:

- the `type` it chose, is it defensible?
- the `tags` it chose, how many, and how specific?
- whether it wrote a real `context` or restated the statement
- whether it used `supersedes` on #8, or wrote a `correction`, or neither

**Step 2, ten questions.** In a **second fresh session**, ask:

1. "When does the rollup run?"
2. "How does Ana like review comments?" *(deliberately attributes a preference to the wrong person)*
3. "Which gauge do we trust?"
4. "Why did we not cache the predictions?"
5. "Who owns the dashboard?"
6. "What did we get wrong about the rollup schedule?"
7. "What stops a deploy going out?"
8. "What did the harbour survey find?"
9. "What happens on Sunday nights?"
10. "What reads the rollup table?"

**Record for each question:**

- did the agent call `recall` **before** answering, or answer from the conversation?
- did it supply `tags`? how many, and did any match the stored record's tags?
- did it supply `types`? was the type right for the question shape?
- did it set `scope`? **It should not have**, none of these names a container. A `scope` set on any
  of these is a finding, and an important one: `scope` excludes, and one set on a guess is the
  failure the soft filter exists to avoid.
- was the right record in the top five?

### What counts as a pass

| | Pass | Concerning |
|---|---|---|
| Tags per write | 2–4 | 0–1, or 5+ |
| Tag specificity | subject + aspect, e.g. `rollup` + `schedule` | one broad tag on everything, e.g. `tidepool` |
| Tag reuse across writes | the same concept gets the same tag | synonyms, `rollup` and `rollups` and `roll-up` |
| Context field | adds what the statement does not say | restates the statement |
| Tags per query | 1–3, at least one matching | none supplied at all |
| `scope` on a subject question | never set | set on any of the ten |
| Right record in top 5 | 8+ of 10 | 6 or fewer |

**The single most important number: how many of the ten questions put the right record in the top
five.** Everything else explains that number.

⚠️ **If the agent supplies no tags at all on queries, that is the finding**, and it is a bigger
result than a low hit rate, it means the descriptions are not reaching the agent, and the
descriptions are the only lever there is. Note whether the host uses progressive tool discovery: if
it shows names first and fetches schemas on demand, the agent may be deciding to call `recall`
before it has ever read the description, which would point at the one-line summary rather than the
body.

### Record

| | Claude Code | Other host |
|---|---|---|
| Right record in top 5, of 10 | | |
| Queries with ≥1 tag supplied | | |
| Queries with ≥1 *matching* tag | | |
| Writes with 2–4 tags | | |
| Writes with a real context | | |
| Used `supersedes` or `correction` on #8 | | |
| Set `scope` on a subject question | | |

---

## What to do with the results

- **Check 1 passes everywhere** → the demo is one keystroke; frame it that way.
- **Check 1 passes only in some hosts** → name the hosts it works in and demo in one of those. Do
  not claim it generally.
- **Check 1 fails everywhere** → the demo becomes "ask the agent to pick up where we left off", which
  still works through `recall` and `open`. Say so plainly rather than showing a keystroke that does
  not exist.
- **Check 2 at 8+/10** → the measured quality figure survives contact with a real agent.
- **Check 2 below that** → the tool descriptions are the lever, and the per-question notes say which
  half is failing: bad tags on the write make records unreachable, bad tags on the query leave the
  mechanism idle. They are different fixes.
