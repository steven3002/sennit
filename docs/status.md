# Status

> Part of the [Sennit documentation](README.md).

**Working, and measured against live Sia:**

- End-to-end round-trip, byte-exact, with client-side encryption
- A thousand records written into one storage slab, and any one of them read back on its own
- A vault rebuilt from the recovery phrase and the indexer with its catalog deleted
- A vault **hydrated onto a machine that has never held it**, and read there
- Reclamation that returns exactly the space it should and nothing that is still in use
- Repack that rewrites every storage location without disturbing a single record identity, including
  **while a second process is writing to the same vault**
- An interrupted repack loses no data
- Three-tier reads, from the device's own copy through a cached location to a cold fetch
- A conversation saved on one machine and replayed **byte for byte** on another
- An MCP server two different clients, on two different protocol revisions, drive at once

**Not yet built:** the `skill` record type, the local viewer and its graph map, session forking.

**Stated plainly, because it would be easy to imply otherwise:**

- **A conversation comes back exactly. The label on it does not.** Replayed on a second machine, every
  message is byte for byte what was saved, including each tool call and the result it was correlated
  with. What is *not* on Sia is the head that described the conversation. Of its 26 fields, **11 come
  back from the transcript itself**, and where the rest land depends on the rebuild rather than on
  the schema: a rebuild at index depth with no memory linking back to the conversation reconstructs
  **4** and loses **11**, the summary, tags, project and lineage among them. A linked memory restores
  `links.memories` through the other record's edge, and a catalog-depth rebuild leaves the embedding
  unset, so **do not read those two numbers as fixed.** Measured on a live two-machine run,
  2026-09-04. **The vault reports the origin of every field it returns**, rather than handing back a
  rebuilt head as though it were the original.
- **Opening a vault on a second machine needs the phrase *and* one browser approval.** The phrase
  alone is not sufficient, and we do not claim seed-only recovery.
- **Cost figures we quote are advertised rates, not invoices.** The free tier has covered everything
  so far; we have never paid a bill. The arithmetic, its sources and its limits are in
  [`docs/cost.md`](cost.md).
- **Recall quality has been measured on synthetic corpora and on LongMemEval**, a public
  conversational-memory benchmark, **but never on a real personal vault.** A public benchmark is
  checkable; it is still not your data.
- **Very long memories are not supported yet.** The embedding model reads about 512 tokens, roughly
  370 English words, and a statement plus its context longer than that fails with an error at
  `remember` instead of being stored. Memories are meant to be one sentence with a short context, so
  ordinary use does not reach it; a fix is planned.
