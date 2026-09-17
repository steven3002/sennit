package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
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
func runHydrate(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("hydrate").withVault().withVerbose()
	depth, quiet := hydrateFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}

	v, err := cmd.vault.open(ctx, out, restorePhases(out, "Restoring records", "restored", *quiet))
	if err != nil {
		return err
	}
	defer closing(out, v)
	if !v.Online() {
		return needsIndexer("hydrate", v, "")
	}
	out.leaves("Records recovered before the interrupt stay on this device.")

	report, err := v.Hydrate(ctx, vault.HydrateRequest{Depth: vault.HydrateDepth(*depth)})
	if err != nil {
		return err
	}

	out.done(ui.MarkSuccess, fmt.Sprintf("Hydrated %s to depth %s", plural(report.Records, "record"), *depth))
	out.report(hydrateRows(report, v.Indexer()))
	out.detail(fmt.Sprintf("fetch %s · rebuild %s · index %s · total %s",
		report.WalkFor.Round(time.Millisecond), report.RebuildFor.Round(time.Millisecond),
		report.EmbedFor.Round(time.Millisecond), report.Elapsed.Round(time.Millisecond)))

	if report.Damaged > 0 || report.Unreadable > 0 {
		out.note(ui.Message{Kind: ui.KindWarning, Text: fmt.Sprintf(
			"%s stopped parsing part way, %d could not be opened at all",
			plural(report.Damaged, "object"), report.Unreadable)})
	}
	if note, ok := rebuiltHeads(report); ok {
		out.note(note)
	}
	if report.Rebuild.Gaps > 0 {
		out.note(ui.Message{Kind: ui.KindWarning, Text: fmt.Sprintf(
			"%s %s missing part of the transcript in the middle",
			plural(report.Rebuild.Gaps, "conversation"), verb(report.Rebuild.Gaps, "is", "are"))})
	}
	if hint, ok := deeperHint(vault.HydrateDepth(*depth)); ok {
		out.note(hint)
	}
	return nil
}

// hydrateFlags is what sennit hydrate takes.
func hydrateFlags(cmd *invocation) (depth *string, quiet *bool) {
	return cmd.set.String("depth", string(vault.HydrateMetadata),
			"how far to go: catalog (locate records), metadata (hold and file them), index (search by meaning)"),
		cmd.set.Bool("quiet", false, "report only the summary, not each record")
}

// hydrateRows is what came back and what it cost, in the labels this command
// has always used.
func hydrateRows(report vault.HydrateReport, indexer string) [][2]string {
	rows := [][2]string{
		{"Indexer", indexer},
		{"Records", fmt.Sprintf("%s from %s, %s of ciphertext",
			ui.Commas(report.Records), plural(report.Objects, "object"), humanBytes(uint64(report.Bytes)))},
		{"Held on this device", ui.Commas(report.Bodies)},
		{"Conversations rebuilt", ui.Commas(report.Sessions)},
		{"Searchable by meaning", ui.Commas(report.Embedded)},
		{"Slabs tracked", ui.Commas(report.Slabs)},
	}
	if report.Foreign > 0 {
		rows = append(rows, [2]string{"Skipped", plural(report.Foreign, "frame") + " this phrase does not open"})
	}
	return rows
}

// rebuiltHeads says plainly what a rebuilt conversation does not carry.
//
// A session head is the one record that never reaches the network, so what
// comes back is assembled from the transcript. Most of it is exact and some of
// it is invented, and a device that presented the two identically would be
// claiming something it has not restored. It stays in the default output for
// that reason: it is the honest half of a successful hydrate.
func rebuiltHeads(report vault.HydrateReport) (ui.Message, bool) {
	if report.Sessions == 0 {
		return ui.Message{}, false
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
	explanation := "The messages are exact. A conversation's own description is not on Sia"
	if len(invented) > 0 {
		explanation += ", so these were reconstructed here: " + strings.Join(invented, ", ")
	}
	explanation += "."
	if len(lost) > 0 {
		explanation += " These are not recoverable: " + strings.Join(lost, ", ") + "."
	}
	return ui.Message{
		Kind: ui.KindWarning,
		Text: fmt.Sprintf("%s %s rebuilt from their transcripts",
			plural(report.Sessions, "conversation"), verb(report.Sessions, "was", "were")),
		Explanation: []string{explanation},
	}, true
}

// deeperHint says what this depth left out and what the next one would add.
func deeperHint(depth vault.HydrateDepth) (ui.Message, bool) {
	switch depth {
	case vault.HydrateCatalog:
		return ui.Message{Kind: ui.KindHint, Text: "records are locatable and none is held here, so a read " +
			"fetches its body from Sia; run again with `--depth index` to search by meaning"}, true
	case vault.HydrateMetadata:
		return ui.Message{Kind: ui.KindHint, Text: "everything is here except the search vectors; " +
			"run again with `--depth index` to search by meaning"}, true
	}
	return ui.Message{}, false
}
