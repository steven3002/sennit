# Reporting a problem in the beta

> Part of the [Sennit documentation](README.md).

This is the point of a beta, so a report of something being confusing is as useful as a report of
something crashing. **Include the output of `sennit version`**, because a bug report that cannot name
a build cannot be reproduced:

```
sennit 0.1.0-beta-mvp (3615850, 2026-09-04)
```

⚠️ **Never paste your recovery phrase into an issue.** It is the key to every memory in the vault.
Nothing in this project ever needs it in order to help you, and no output here prints it.

**Two checks are worth more than any bug report**, because they cover the two things automated tests
cannot reach. Both are scripted in [`docs/host-checks.md`](host-checks.md) and take about ten
minutes: whether the `/resume` prompt shows up as a slash command in your MCP host, and **whether an
agent reading only the tool descriptions supplies usable tags.** The second one decides whether this
project's retrieval quality claim survives contact with a real agent, and it has never been run by
anyone who did not write the code.
