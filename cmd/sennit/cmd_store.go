package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// runFlush writes whatever is queued to Sia.
//
// A record is durable on this device the moment it is remembered and on the
// network only after a flush. The standing cadence closes that gap on its own;
// this is for closing it now.
func runFlush(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("flush", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	pending := v.Pending()
	if pending == 0 {
		fmt.Fprint(stderr, "nothing queued: everything on this device is on Sia\n")
		return nil
	}
	fmt.Fprintf(stderr, "flushing %d record(s)\n", pending)

	flushed, err := v.Flush(ctx)
	if err != nil {
		return err
	}
	if flushed == nil {
		fmt.Fprint(stderr, "the flush wrote nothing\n")
		return nil
	}
	fmt.Fprintf(stderr, "  wrote     %s in %d object(s) over %d slab(s)\n",
		humanBytes(uint64(flushed.Bytes())), len(flushed.Written), len(flushed.Slabs))
	fmt.Fprintf(stderr, "  upload    %s · pin slabs %s · pin objects %s\n",
		took(flushed.UploadFor), took(flushed.PinSlabsFor), took(flushed.PinObjectFor))
	if n := len(flushed.Written); n > 0 {
		fmt.Fprintf(stderr, "  per pin   %s across %d object(s)\n",
			took(flushed.PinObjectFor/time.Duration(n)), n)
	}
	return nil
}

// runStatus reports what the vault holds, what it owes the network, and what
// it is being billed for.
func runStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	entries := v.Entries()
	fmt.Fprintf(stderr, "records   %d catalogued, %d queued for Sia\n", len(entries), v.Pending())

	stats := v.ManifestStats()
	fmt.Fprintf(stderr, "catalog   %s snapshot + %s log, %d compaction(s), %s written\n",
		humanBytes(uint64(stats.SnapshotBytes)), humanBytes(uint64(stats.LogBytes)),
		stats.Compactions, humanBytes(uint64(stats.Written)))

	health := v.IndexHealth()
	vectors := v.VectorStats()
	fmt.Fprintf(stderr, "index     %d vector(s) of %s, %s base + %s delta, %d compaction(s)\n",
		health.Indexed, health.Model,
		humanBytes(uint64(vectors.BaseBytes)), humanBytes(uint64(vectors.DeltaBytes)), vectors.Compactions)
	// A mixed index is reported rather than passed over. Vectors from two models
	// cannot be compared, so the ones from the other model are simply not
	// searched, which looks exactly like the vault having become worse at
	// recall unless it is said out loud.
	if health.Mixed() {
		fmt.Fprintf(stderr, "  ⚠ %d vector(s) are from another model and are NOT searchable:\n", health.Stale())
		for model, count := range health.Foreign {
			fmt.Fprintf(stderr, "      %-32s %d\n", model, count)
		}
		fmt.Fprintln(stderr, "      re-embed those records to make them findable again")
	}

	if cache, err := v.CacheSize(); err == nil && cache.Objects > 0 {
		fmt.Fprintf(stderr, "locations %s for %d object(s) over %d slab(s), %.0f B/object\n",
			humanBytes(uint64(cache.Total())), cache.Objects, cache.Slabs, cache.PerObject())
	}

	if tiers, err := v.ReadStats(); err == nil && len(tiers) > 0 {
		fmt.Fprint(stderr, "reads\n")
		for _, tier := range tiers {
			fmt.Fprintf(stderr, "  %-8s %6d served, mean %s, %d miss(es)\n",
				tier.Tier, tier.Reads, took(tier.Mean()), tier.Misses)
		}
	}

	if !v.Online() {
		fmt.Fprint(stderr, "offline: queued records stay on this device until a connected run\n")
		return nil
	}

	account, err := v.Account(ctx)
	if err != nil {
		return err
	}
	slabs, err := v.TrackedSlabs()
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "quota     %s of %s used, %s free\n",
		humanBytes(account.PinnedData), humanBytes(account.MaxPinnedData), humanBytes(account.Free()))
	var hydrated int
	for _, slab := range slabs {
		if !slab.Releasable() {
			hydrated++
		}
	}
	fmt.Fprintf(stderr, "slabs     %d pinned by this device, %d hydrated from another\n",
		len(slabs)-hydrated, hydrated)

	mark, err := v.Watermark(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "repack    %.1f%% of quota used; %s\n", 100*mark.Used, repackAdvice(mark.Due, mark.Affordable))
	if mark.Due {
		fmt.Fprintf(stderr, "  ⚠ reclaimable storage has built up. Run `sennit reclaim -repack` to return it;\n"+
			"    left alone it keeps accumulating until a write fails for want of room.\n")
	}

	// Storage the account pays for that this device has no record of. It is
	// reported here because it is otherwise invisible: an ordinary sweep is
	// bounded by the ledger and cannot see past it, so quota that will not come
	// back looks like quota that was never freed.
	unledgered, err := v.Unledgered(ctx)
	if err != nil {
		return err
	}
	if len(unledgered) > 0 {
		var empty int
		for _, slab := range unledgered {
			if slab.Empty {
				empty++
			}
		}
		fmt.Fprintf(stderr, "unknown   %d slab(s) are billed to this account and not in this device's ledger\n",
			len(unledgered))
		if empty > 0 {
			fmt.Fprintf(stderr, "          %d of them hold nothing, an interrupted write leaves exactly this; "+
				"`sennit reclaim -orphans` releases them\n", empty)
		}
		if held := len(unledgered) - empty; held > 0 {
			fmt.Fprintf(stderr, "          %d hold records and belong to another installation of this vault; "+
				"reclaim them from the device that wrote them\n", held)
		}
	}
	return nil
}

func repackAdvice(due, affordable bool) string {
	switch {
	case due && affordable:
		return "worth running, and there is room for it"
	case due:
		return "worth running, but there is no longer room for the slabs it would hold at once"
	default:
		return "not needed yet"
	}
}

// runReclaim releases storage nothing points at any more.
func runReclaim(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reclaim", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	repack := fs.Bool("repack", false,
		"rewrite every live record into as few slabs as it fits in before releasing the rest")
	orphans := fs.Bool("orphans", false,
		"also release slabs the account is billed for that hold nothing, including any stranded by an installation that is gone")
	unreadable := fs.Bool("unreadable", false,
		"also delete objects the indexer holds but cannot open")
	takeOwnership := fs.Bool("take-ownership", false,
		"release storage this device hydrated rather than pinned; only when the installation that wrote it is gone for good, never when it is merely switched off")
	releaseAll := fs.Bool("release-all", false,
		"release this vault's storage even though the catalog is empty; only for a vault that really has been emptied, never to work around a catalog that will not load")
	if err := fs.Parse(args); err != nil {
		return err
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	opts := vault.ReclaimOptions{ReleaseAll: *releaseAll, TakeOwnership: *takeOwnership}

	if *takeOwnership {
		taken, err := v.TakeOwnership()
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "owned     %d slab(s) hydrated from another installation are now this device's to release\n", taken)
	}

	if *repack {
		if err := reportRepack(ctx, v); err != nil {
			return err
		}
	}

	sweep, err := v.Reclaim(ctx, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "swept     %d object(s), deleted %d, released %d slab(s) in %s\n",
		sweep.ObjectsSeen, sweep.ObjectsDeleted, sweep.SlabsReleased, took(sweep.Elapsed))
	if sweep.Unreadable > 0 && !*unreadable {
		fmt.Fprintf(stderr, "          %d object(s) cannot be opened; -unreadable removes them\n", sweep.Unreadable)
	}
	if sweep.SlabsHeld > 0 {
		fmt.Fprintf(stderr, "          %d slab(s) left alone: another device pinned them and this one hydrated them\n",
			sweep.SlabsHeld)
	}
	// A sweep that reported only what it released would say nothing about the
	// storage it cannot reach, which is exactly the storage a user is looking
	// for when a reclaim returns less than expected.
	if unledgered, err := v.Unledgered(ctx); err == nil && len(unledgered) > 0 && !*orphans {
		var empty int
		for _, slab := range unledgered {
			if slab.Empty {
				empty++
			}
		}
		fmt.Fprintf(stderr, "unknown   %d slab(s) billed to this account are not in this device's ledger "+
			"and were not swept; %d hold nothing and -orphans would release them\n", len(unledgered), empty)
	}

	if *unreadable {
		dropped, err := v.DropUnreadable(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "dropped   %d object(s) the indexer could not open\n", len(dropped))
	}

	freed := sweep.Freed()
	before, after := sweep.Before, sweep.After
	if *orphans {
		found, err := v.Orphans(ctx, opts)
		if err != nil {
			return err
		}
		var stranded int
		for _, orphan := range found {
			if !orphan.Tracked {
				stranded++
			}
		}
		fmt.Fprintf(stderr, "orphans   %d slab(s) hold nothing, %d of them unknown to this device\n",
			len(found), stranded)

		released, err := v.ReleaseOrphans(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "          released %d slab(s) in %s\n", released.SlabsReleased, took(released.Elapsed))
		freed += released.Freed()
		after = released.After
	}

	fmt.Fprintf(stderr, "quota     %s used before, %s after, freed %s\n",
		humanBytes(before.PinnedData), humanBytes(after.PinnedData), humanBytes(freed))
	return nil
}

func reportRepack(ctx context.Context, v *vault.Vault) error {
	mark, err := v.Watermark(ctx)
	if err != nil {
		return err
	}
	if !mark.Affordable {
		return fmt.Errorf("repack needs %s free to hold the old and new slabs at once, and there is less than that",
			humanBytes(mark.Headroom))
	}

	packed, err := v.Repack(ctx)
	if err != nil {
		return err
	}
	if len(packed.Records) == 0 {
		fmt.Fprint(stderr, "repack    nothing to move\n")
		return nil
	}
	fmt.Fprintf(stderr, "repack    %d record(s), %d slab(s) into %d, peak %d, in %s\n",
		len(packed.Records), packed.SlabsBefore, packed.SlabsAfter, packed.Peak, took(packed.Elapsed))
	fmt.Fprintf(stderr, "          read %s · write %s · retire %s\n",
		took(packed.ReadFor), took(packed.WriteFor), took(packed.RetireFor))
	return nil
}

// runRecover rebuilds the vault from the recovery phrase and the indexer.
func runRecover(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	embed := fs.Bool("embed", true,
		"regenerate search vectors as records are recovered, so they are findable by meaning and not only by id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	fmt.Fprint(stderr, "rebuilding from the recovery phrase and the indexer\n")
	report, err := v.Recover(ctx, vault.RecoveryRequest{
		Embed: *embed,
		OnRecord: func(_ record.ID, n int) {
			if n%100 == 0 {
				fmt.Fprintf(stderr, "  %d records\n", n)
			}
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "recovered %d record(s) from %d object(s) in %s\n",
		report.Recovered, report.Objects, took(report.Elapsed))
	if report.Foreign > 0 {
		fmt.Fprintf(stderr, "  skipped  %d frame(s) this phrase does not open\n", report.Foreign)
	}
	if report.Damaged > 0 || report.Unreadable > 0 {
		fmt.Fprintf(stderr, "  damaged  %d object(s) stopped parsing part way, %d could not be opened at all\n",
			report.Damaged, report.Unreadable)
	}
	return nil
}
