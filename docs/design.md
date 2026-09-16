# Design principles

> Part of the [Sennit documentation](README.md).

1. **Storage consumer, not AI operator**, Sennit puts data *on* Sia; it does not drive Sia with an LLM.
2. **Client-side confidentiality**, encryption, keys and search stay on the device.
3. **User-owned and portable**, memory follows the user across apps, models and devices.
4. **With the grain of Sia**, built on the first-party SDK and indexer.
5. **No LLM in our stack**, Sennit embeds, stores, indexes and ranks. The calling agent decides what is worth remembering.

## Built on

- [Sia](https://sia.tech), decentralized storage with client-side encryption and user-held keys
- [`go.sia.tech/siastorage`](https://pkg.go.dev/go.sia.tech/siastorage), the first-party Go SDK
- [Model Context Protocol](https://modelcontextprotocol.io), the open standard for connecting data to AI applications
