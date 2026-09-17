# What's changing

> Part of the [Sennit documentation](README.md).

**The [quickstart](quickstart.md) is correct for [`v0.1.0-beta-mvp`](https://github.com/steven3002/sennit/releases/tag/v0.1.0-beta-mvp).**
These are planned changes, not shipped ones. Each page of this documentation is rewritten in the release that
changes it, not before, so the quickstart never describes a command your binary does not have.

- **Credentials move into named profiles.** Today the phrase and the app key travel in environment
  variables and sit in plain text inside your MCP host's config. A profile binds the phrase, the app
  key, the indexer and the vault directory under one name, keeps the secrets in your operating
  system's keychain (a locked file where there is none), and lets an MCP host refer to the profile by
  name instead of holding the phrase. `SENNIT_PHRASE`, `SENNIT_APP_KEY`, `SENNIT_HOME` and
  `SENNIT_INDEXER` keep working for the release that introduces profiles, with a warning each time
  one is used, and are removed in the release after it. **This replaces quickstart steps 2, 3 and 7.**
- **Signed and notarized macOS binaries**, so the Gatekeeper step goes away.
- **Long memories** stop failing at the embedding model's limit.
