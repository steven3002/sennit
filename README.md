# Sennit

**User-owned storage for an AI's memory, sessions, and skills, encrypted, on the [Sia](https://sia.tech) network.**

Sennit gives an AI agent a long-term memory that belongs to **you** rather than to a vendor: encrypted on your device, stored on decentralized infrastructure that cannot read it, retrieved by meaning, and portable across apps, models and machines.

> **Status: beta.** The substrate, the CLI and the MCP server are built and measured against the live
> Sia network, and the [quickstart](docs/quickstart.md) is tested end to end. **Prebuilt binaries are
> available**, see [`v0.1.0-beta-mvp`](https://github.com/steven3002/sennit/releases/tag/v0.1.0-beta-mvp).
> It is beta because it works and is measured, and because it has been used by very few people. See
> [Status](docs/status.md) for what is and is not proven, and [What's changing](docs/whats-changing.md)
> for the parts of the quickstart a coming release replaces.

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

## Get started

Download a release, get a recovery phrase, approve the installation once in a browser, and remember
something. About ten minutes, most of it one download and one approval click.

**→ [Quickstart](docs/quickstart.md)**

No Sia node, no wallet and no payment are needed; the hosted indexer's free tier covers it. Every
command also takes `--offline` if you want to see recall working before connecting to Sia.

## Documentation

| Page | What it covers |
|---|---|
| [Quickstart](docs/quickstart.md) | Install, recovery phrase, approval, first memory, recall, connecting an MCP client |
| [Storage](docs/storage.md) | Why "saved" is not "on Sia", flushing, quota, and reclaiming storage |
| [Cost](docs/cost.md) | What storage costs, the arithmetic and its sources |
| [Status](docs/status.md) | What is built, what is measured against live Sia, and what is not proven |
| [What's changing](docs/whats-changing.md) | Planned changes that will replace parts of the quickstart |
| [Troubleshooting](docs/troubleshooting.md) | Common errors and what to do about them |
| [Design principles](docs/design.md) | What Sennit is and is not, and what it is built on |
| [Reporting a problem](docs/reporting-problems.md) | What to include in a beta report, and the two checks worth more than any bug report |
| [Host checks](docs/host-checks.md) | The two ten-minute checks, scripted |

## Contributing

Early and in flux. See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and design discussion are
welcome; please open an issue before a large pull request.

## Security

Sennit handles encryption keys and personal data. Please report vulnerabilities responsibly, see [SECURITY.md](SECURITY.md). Do not open public issues for security problems.

## License

[MIT](LICENSE), matching the Sia SDK and the wider Go ecosystem.
