# Security Policy

Sennit handles **encryption keys, recovery phrases, and personal data**. Security reports are taken seriously.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report privately via [GitHub Security Advisories](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) ("Report a vulnerability" on the Security tab), which keeps the report confidential until a fix is available.

Please include: what you found, how to reproduce it, and what an attacker could achieve. We will acknowledge receipt, keep you updated, and credit you when the fix ships, unless you prefer otherwise.

## Scope

Especially interested in:

- **Key handling**, anything that could expose a recovery phrase or derived key, in memory, on disk, or in logs
- **Plaintext leakage**, any path where unencrypted record content or embeddings could reach the network, an indexer, or a storage provider
- **Encryption misuse**, nonce reuse, weak derivation, unauthenticated data accepted as authentic
- **Metadata leakage**, inferring content from object sizes, counts, or timing beyond what is documented
- **Untrusted input**, recalled records are **data, never instructions**; report anything that lets stored content drive an agent or the host
- **Bundle unpacking**, path traversal, symlink escape, or decompression abuse when materialising files to disk

## Out of scope

- Vulnerabilities in Sia, `indexd`, or storage providers themselves, please report those to the [Sia Foundation](https://github.com/SiaFoundation). We will happily help route a report.
- Anything requiring an already-compromised device. Sennit assumes the local machine is trusted; that is a stated design boundary, not a defect.

## Design boundaries (not vulnerabilities)

Stated plainly so reports can be triaged fairly:

- **Access is key possession.** Anyone with the recovery phrase has full access. There is no server-side revocation, and no delegated or programmable sharing.
- **The phrase is stored on the device once you connect.** `sennit connect` keeps the phrase and the app key in the operating system's credential store, or, where there is none, in a file only your account can open. Anyone who can act as your account on that device can read them, which is the same trust the local working copy already rests on.
- **Local plaintext.** The local working copy is readable on a trusted device, this is deliberate, as in local-first note tools. We considered encrypting it and decided against it: the process that searches your memory is the process that holds your key, and an MCP host starts that process unattended, so on a machine where an attacker can read the vault they can read the key beside it. Full-disk encryption is the control that works here. `SENNIT_HOME` can point inside an encrypted volume if you want a second one.
- **Context an agent keeps.** Sennit's protection covers the vault: what is stored on the network, and the working copy on your device. A record an agent reads becomes part of that agent's context. Many agents keep their context, or a summary of it, in their own local data, such as a session log or a working folder. That data is managed by the agent, under its settings, and `forget` in Sennit does not reach it. For example, Claude Code keeps session logs under `~/.claude/projects/`, and Antigravity CLI keeps its working history under `~/.gemini/antigravity-cli/brain/`.
- **A forget is not shared between devices.** `forget` takes a record off your device at once, and its copy on Sia stays there until a flush deletes it. Until then, `sennit hydrate` or `sennit recover` on another device can restore it. A device that already holds the record keeps it.
- **Durability is Sia's.** Redundancy and repair are the network's guarantees, not ours.

## Supported versions

Sennit is in beta. **Security fixes go into the latest release and into `main`.** Earlier releases are not patched: the fix for them is to update.

| Version | Security fixes |
|---|---|
| The latest release, the first one on the [releases page](https://github.com/steven3002/sennit/releases) | Yes |
| `main` | Yes |
| Any earlier release | No, update to the latest |

**A report about an earlier release is still welcome.** We check it against the latest release. If the problem is still there, it is fixed there and you are credited as for any other report. If it is already gone, we tell you which release removed it.
