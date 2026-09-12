# What it costs

**Short answer: for almost everyone, nothing.** The free tier holds roughly **57 million** packed
records, and if you outgrow it, storing 100,000 memories for a year costs about **a third of a
cent**.

That sounds like marketing, so the rest of this page shows the arithmetic and says plainly which
numbers are measured and which are quoted.

---

## 1. The free tier

`sia.storage` grants every approved app **50 GB (46.57 GiB) of pinned data**, up to 3 apps, on
approval. There is no wallet, no payment, no contract negotiation, and no funding step. The indexer
funds host accounts on your behalf; your app's entire economic interface is one call reporting how
much you have pinned and how much remains.

**How far 50 GB goes depends entirely on whether storage gets reclaimed:**

| | Records that fit in the free tier |
|---|---|
| Sennit, packed and reclaimed | **~57,000,000** |
| The same data written one record per object | **1,250** |

That 45,000× gap is the whole reason this project has a packer and a repack command. See §4.

## 2. If you outgrow it, the hosted tiers

`sia.storage`'s own tiers, read from their pricing page:

| Tier | Price | Storage |
|---|---|---|
| Free | No charge | 50 GB |
| Plus | $7.99 / month | 500 GB |
| Pro | $14.99 / month | 5 TB |
| Ultimate | $19.99 / month | 10 TB |

Pro works out at about **$3.00 per TB per month**, which sits on top of a raw network cost of
$3.3–3.7 per TiB per month (§3). They are reselling close to cost.

## 3. If you run your own indexer

The Sia SDK takes the indexer URL as a parameter, and Sia's documentation states the SDK works with
any Sia indexer and that you can run your own. Doing so replaces the hosted tier with the raw network
price, which we measured from two independent sources:

| Per month, 3× redundancy (10-of-30) | Network-wide<br>(444 hosts, siascan) | The indexer's own host set<br>(198 hosts priced) |
|---|---|---|
| Storage, SC / GiB | 2.3646 | 2.1402 |
| **Logical GiB** | **$0.0036** | **$0.0033** |
| **Logical TiB** | **$3.68** | **$3.33** |
| One 40 MiB slab | $0.00014 | $0.000127 |

Two independent measurements agreeing within 10% is the strongest form this claim takes.
**$3.3–3.7 per TiB per month** is the honest range.

⚠️ **What this does not include:** running an indexer is a server you operate, and its behaviour
under real load is something we have not measured. The *prices* above are network prices; the
*operational cost* of self-hosting is yours to estimate.

### Per memory

At the measured mean sealed record size of 857 bytes:

| | Cost |
|---|---|
| One memory / month | $0.0000000029 |
| 10,000 memories / year | $0.00034 |
| **100,000 memories / year** | **$0.0034** |

## 4. The number that actually matters

**Per-record cost is not a real quantity.** Storage is billed in **slabs** of 40 MiB, charged whole,
and a slab can never be extended after it is written. One slab holds roughly **48,900 records**.

> **Until you exceed ~48,900 memories, your storage bill is one slab: about $0.0017 per year.**

So the cost driver is not how much you remember. It is **how many partially-filled slabs are left
lying around.** Every time the packer flushes, whatever it flushes occupies a full slab forever.
A hundred eager flushes is a hundred slabs, which is about **$0.014 per month** for data that would
fit in one.

That is still trivially cheap in absolute terms, but it is the only cost lever in the system, and it
is why Sennit batches writes rather than sending them one at a time, and why `sennit reclaim
-repack` exists. On the free tier it is the difference between 57 million records and 1,250.

## 5. Stopping costs nothing, and nothing is locked in

Storage is metered, not prepaid. Sia's RHP4 protocol has an explicit release mechanism, and the
indexer runs it automatically on a maintenance loop. Freeing a 30-sector slab costs **$0.000000058**
against **$0.000133 per month** to keep it, so it pays for itself in about thirty seconds.

**Your cost at any moment is what you currently have pinned.** There is no reusable storage
allocation to hand back and no term to run out. Delete records and the meter stops.

---

## ⚠️ What these numbers are, and what they are not

**They are arithmetic over live price sheets, not invoices. We have never paid for storage.** The
free tier has covered every byte this project has written. Read them as *quoted rates*, the same way
you would read any published price, and not as a bill anyone has received.

Specifically:

- Host prices and the SC/USD exchange rate were read live on **2026-08-02** from the Sia Foundation's
  own explorer API. **Both are spot readings and both move.**
- The hosted tier prices were read from `sia.storage` the same day.
- The record sizes, the slab quantum and the packing density are **measured** from real writes to
  the live network, not estimated.
- Self-hosted indexer *behaviour* is unmeasured. Its *prices* are the network prices above.

If a number here matters to a decision you are making, re-check it. The measurement scripts that
produced these figures are in the repository.
