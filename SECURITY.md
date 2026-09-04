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
- **Local plaintext.** The local working copy is readable on a trusted device, this is deliberate, as in local-first note tools.
- **Durability is Sia's.** Redundancy and repair are the network's guarantees, not ours.

## Supported versions

Pre-alpha; there is no released version yet. Once releases begin, this section will state which are supported.
