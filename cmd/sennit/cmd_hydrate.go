package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// runHydrate rebuilds a vault on a machine that has never held it.
//
// It is `recover` with the cost made visible and the order made choosable. What
// a second device gets from the network is the records; the catalog that locates
// them and the index that searches them are device-local by decision, so both
// are rebuilt here. The catalog is cheap and the index is not, hundreds of
// milliseconds a record, which is why this can stop after any stage and why it
// says what each one cost.
func runHydrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hydrate", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	depth := fs.String("depth", string(vault.HydrateMetadata),
		"how far to go: catalog (locate records), metadata (hold and file them), index (search by meaning)")
	quiet := fs.Bool("quiet", false, "report only the summary, not each record")
	if err := fs.Parse(args); err != nil {
		return err
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	fmt.Fprintf(stderr, "hydrating from %s to depth %q\n", v.Indexer(), *depth)
	tick := time.Now()
	report, err := v.Hydrate(ctx, vault.HydrateRequest{
		Depth: vault.HydrateDepth(*depth),
		OnRecord: func(_ record.ID, n int) {
			if *quiet || time.Since(tick) < time.Second {
				return
			}
			tick = time.Now()
			fmt.Fprintf(stderr, "  %d record(s)...\n", n)
		},
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stderr, "\n%d record(s) from %d object(s), %s of ciphertext\n",
		report.Records, report.Objects, humanBytes(uint64(report.Bytes)))
	fmt.Fprintf(stderr, "  held on this device   %d\n", report.Bodies)
	fmt.Fprintf(stderr, "  conversations rebuilt %d\n", report.Sessions)
	fmt.Fprintf(stderr, "  searchable by meaning %d\n", report.Embedded)
	fmt.Fprintf(stderr, "  slabs tracked         %d\n", report.Slabs)
	if report.Foreign+report.Damaged+report.Unreadable > 0 {
		fmt.Fprintf(stderr, "  skipped               %d frame(s) this phrase does not open, "+
			"%d damaged object(s), %d unreadable\n",
			report.Foreign, report.Damaged, report.Unreadable)
	}
	fmt.Fprintf(stderr, "\nfetch %s · rebuild %s · index %s · total %s\n",
		report.WalkFor.Round(time.Millisecond), report.RebuildFor.Round(time.Millisecond),
		report.EmbedFor.Round(time.Millisecond), report.Elapsed.Round(time.Millisecond))

	reportRebuiltHeads(report)

	switch vault.HydrateDepth(*depth) {
	case vault.HydrateCatalog:
		fmt.Fprintf(stderr, "\nRecords are locatable and none is held here: a read fetches its body "+
			"from Sia.\nRun again with -depth index to search by meaning.\n")
	case vault.HydrateMetadata:
		fmt.Fprintf(stderr, "\nEverything is here except the search vectors. Run again with "+
			"-depth index to search by meaning.\n")
	}
	return nil
}

// reportRebuiltHeads says plainly what a rebuilt conversation does not carry.
//
// A session head is the one record that never reaches the network, so what comes
// back is assembled from the transcript. Most of it is exact and some of it is
// invented, and a device that presented the two identically would be claiming
// something it has not restored.
func reportRebuiltHeads(report vault.HydrateReport) {
	if report.Sessions == 0 {
		return
	}
	var invented, lost []string
	for _, field := range vault.HeadFields {
		switch report.Rebuild.Origins[field] {
		case vault.OriginSynthesised:
			invented = append(invented, field)
		case vault.OriginLost:
			lost = append(lost, field)
		}
	}
	fmt.Fprintf(stderr, "\n%d conversation(s) were rebuilt from their transcripts.\n", report.Sessions)
	fmt.Fprintf(stderr, "  The messages are exact. A conversation's own description is not on Sia,\n")
	if len(invented) > 0 {
		fmt.Fprintf(stderr, "  so these were reconstructed here: %s\n", strings.Join(invented, ", "))
	}
	if len(lost) > 0 {
		fmt.Fprintf(stderr, "  and these are not recoverable: %s\n", strings.Join(lost, ", "))
	}
	if report.Rebuild.Gaps > 0 {
		fmt.Fprintf(stderr, "  ⚠ %d conversation(s) are missing part of the transcript in the middle.\n",
			report.Rebuild.Gaps)
	}
}
