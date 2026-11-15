# What's changing

> Part of the [Sennit documentation](README.md).

**The [quickstart](quickstart.md) is correct for [`v0.2.0-beta-mvp`](https://github.com/steven3002/sennit/releases/tag/v0.2.0-beta-mvp).**
These are planned changes, not shipped ones. Each page of this documentation is rewritten in the release that
changes it, not before, so the quickstart never describes a command your binary does not have.

- **Credentials move into named profiles.** The first step shipped in `v0.2.0-beta-mvp`: one
  profile, `default`, which `sennit connect` stores in your operating system's credential store (a
  file only your account can open where there is none), so a shell needs nothing set and an MCP
  host's config holds no secret. Named profiles follow: several under names of your choosing, each
  binding the phrase, the app key, the indexer and the vault directory, with commands to list,
  switch and remove them, and an MCP host that refers to one by name. `SENNIT_PHRASE`,
  `SENNIT_APP_KEY`, `SENNIT_HOME` and `SENNIT_INDEXER` keep working until then, and are retired in
  the releases after named profiles arrive, with a warning first.
- **Signed and notarized macOS binaries**, so the Gatekeeper step goes away.
- **Long memories** stop failing at the embedding model's limit.
