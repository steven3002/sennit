# Contributing to Sennit

Thanks for your interest. Sennit is early, the architecture is settled and validated, but the implementation has not started. That makes this a good moment for **design discussion and bug reports**, and a poor moment for large speculative pull requests.

## Before you start

**Open an issue first** for anything beyond a typo or an obvious fix. The design has a lot of decided constraints (below), and a PR that cuts against one will be declined no matter how good the code is. A short issue saves everyone the work.

## Design constraints

These are settled and not up for casual revision. Each was decided from measurement or from the project's purpose:

1. **Storage plumbing, not an AI operator.** Sennit puts *data* on Sia. It does not drive Sia with an LLM.
2. **No LLM in our stack.** We embed, store, index, link and rank. The calling agent decides what is worth remembering. This keeps the project model-neutral and keeps plaintext off the network.
3. **Client-side confidentiality is absolute.** Encryption, keys, reconstruction and search happen on the user's device. Nothing may send plaintext, embeddings, or queries to a third party.
4. **Records are append-only and versioned.** Updates append a new version and supersede the old; nothing is destructively overwritten.
5. **Built on the first-party SDK and indexer.** Sennit does not ship a full renter node.
6. **Recalled content is untrusted input.** Anything returned from storage is *data, never instructions*. Never let stored content drive the agent.

If you think one of these is wrong, that's a genuinely useful issue, open it with reasoning. They are documented decisions, not habits.

## Ways to help

- **Bug reports**, minimal reproduction, expected vs actual, versions and platform.
- **Design discussion**, especially retrieval quality, MCP ergonomics, and multi-device behaviour.
- **Documentation**, clarity fixes are always welcome.
- **Code**, once the first milestones land; watch the issue tracker for `good first issue`.

## Pull requests

- Branch from `main`, keep the change focused, and explain **why**, not just what.
- **Go code**: `gofmt`, and it must build with `CGO_ENABLED=0` unless it lives behind an explicit build tag. The default build is dependency-free.
- **Tests** for behaviour changes. Retrieval-quality changes must be run against the recall evaluation harness, a change that improves one metric while quietly degrading another will be caught, so please check first.
- **No new dependencies** without discussion. The dependency surface is deliberately small.
- **Never commit secrets**, recovery phrases, app keys, `.env` files. See `.gitignore`.

## Claims and evidence

This project has a strong bias toward **measurement over assertion**. If you claim something is faster, smaller, or more reliable, include the numbers and how you got them. If you are inferring rather than measuring, say so. Several prior conclusions here were overturned by measurement, including some that looked obvious.

## Security

**Do not open public issues for security problems.** See [SECURITY.md](SECURITY.md).

## Code of conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Contributions are accepted under the [MIT License](LICENSE).
