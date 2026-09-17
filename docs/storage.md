# Storage: saved, flushed and reclaimed

> Part of the [Sennit documentation](README.md).

## "Saved" is not "on Sia"

Sennit distinguishes the two everywhere, because the gap between them is real and can be up to an
hour under the standing flush cadence:

- **On this device**, the record is sealed and durable locally the moment `remember` returns.
- **On Sia**, the record is on the network, and only a completed flush puts it there.

`remember` says which it achieved on the line it ends with, in one of three wordings:

```
✓ Remembered, and stored on Sia (24.4s)
✓ Remembered on this device, queued for Sia (0.4s)
! Remembered on this device only: the indexer did not answer, the next connected flush uploads it (0.6s)
```

Only the first says the record is on Sia. `sennit status` reports what is still queued, and
`sennit flush` closes the gap on demand. **No output claims durability on Sia before a flush has
completed.**

## Storage, quota and reclaiming

Sia bills a **slab** whole, 40 MiB, whether it holds one record or a thousand, and a partly filled
slab can never be extended. So every flush strands a slab, and an account fills up over months no
matter how little you actually store.

```sh
sennit status           # what is held, what is queued, what is billed
sennit reclaim          # release storage nothing points at any more
sennit reclaim --repack # rewrite live records into fewer slabs first
```

`status` reports what the account holds and warns you once reclaimable storage has built up, rather
than leaving you to find out when a write fails:

```
✓ Checked this vault and its Sia account (9.8s)

Vault    ~/.sennit
Records  1,281 stored on Sia, 3 queued on this device
Search   1,284 indexed with bge-small-en-v1.5-fp32
Quota    80.00 MiB of 46.57 GiB used, 46.49 GiB free
Slabs    2 pinned by this device, 0 hydrated from another
Repack   not needed yet (0.2% of quota used)
```
 Reclamation is two-phase and refuses to run on a keep-set that does not account for
everything the vault holds, an empty keep-set means the computation is wrong, not that the data is
dead.

**This is also the only thing that costs money.** Reclaimed, the free tier holds around **57 million
records**; written one record per object it holds **1,250**. Beyond the free tier, storing 100,000
memories for a year is about a third of a cent. The arithmetic, both price sources, and a plain
statement of what has and has not actually been paid for are in [`docs/cost.md`](cost.md).
