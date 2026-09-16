# Storage: saved, flushed and reclaimed

> Part of the [Sennit documentation](README.md).

## "Saved" is not "on Sia"

Sennit distinguishes the two everywhere, because the gap between them is real and can be up to an
hour under the standing flush cadence:

- **On this device**, the record is sealed and durable locally the moment `remember` returns.
- **On Sia**, the record is on the network, and only a completed flush puts it there.

`remember` says which it achieved (`on Sia 420 B in 1 object(s)` versus `on Sia not yet, held on
this device, 1 record(s) queued`), `sennit status` reports what is still queued, and `sennit
flush` closes the gap on demand. **No output claims durability on Sia before a flush has
completed.**

## Storage, quota and reclaiming

Sia bills a **slab** whole, 40 MiB, whether it holds one record or a thousand, and a partly filled
slab can never be extended. So every flush strands a slab, and an account fills up over months no
matter how little you actually store.

```sh
sennit status          # what is held, what is queued, what is billed
sennit reclaim         # release storage nothing points at any more
sennit reclaim -repack # rewrite live records into fewer slabs first
```

`status` warns you once reclaimable storage has built up, rather than leaving you to find out when a
write fails. Reclamation is two-phase and refuses to run on a keep-set that does not account for
everything the vault holds, an empty keep-set means the computation is wrong, not that the data is
dead.

**This is also the only thing that costs money.** Reclaimed, the free tier holds around **57 million
records**; written one record per object it holds **1,250**. Beyond the free tier, storing 100,000
memories for a year is about a third of a cent. The arithmetic, both price sources, and a plain
statement of what has and has not actually been paid for are in [`docs/cost.md`](cost.md).
