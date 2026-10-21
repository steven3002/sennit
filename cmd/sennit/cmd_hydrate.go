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
	for _, note := range lostBySome(report) {
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
//
// The fields it calls not recoverable are the ones every rebuilt conversation
// lost. A field only some of them lost is said of those alone, by lostBySome.
func rebuiltHeads(report vault.HydrateReport) (ui.Message, bool) {
	if report.Sessions == 0 {
		return ui.Message{}, false
	}
	verdict := verdictOn(report)
	explanation := "The messages are exact. A conversation's own description is not on Sia"
	if len(verdict.invented) > 0 {
		explanation += ", so these were reconstructed here: " + strings.Join(verdict.invented, ", ")
	}
	explanation += "."
	if len(verdict.lost) > 0 {
		explanation += " These are not recoverable: " + strings.Join(verdict.lost, ", ") + "."
	}
	return ui.Message{
		Kind: ui.KindWarning,
		Text: fmt.Sprintf("%s %s rebuilt from %s",
			plural(report.Sessions, "conversation"), verb(report.Sessions, "was", "were"),
			pick(report.Sessions, "its transcript", "their transcripts")),
		Explanation: []string{explanation},
	}, true
}

// lostBySome names the rebuilt conversations that lost a field the others got
// back, one warning for each set of fields the same conversations lost.
//
// Saying it of every conversation would be false of most of them. Each is named
// by its title, the one it was rebuilt with and is listed under on this device,
// and a long list stops at listedConversations and says how many more there are.
func lostBySome(report vault.HydrateReport) []ui.Message {
	verdict := verdictOn(report)
	out := make([]ui.Message, 0, len(verdict.some))
	for _, group := range verdict.some {
		note := ui.Message{
			Kind: ui.KindWarning,
			Text: fmt.Sprintf("%s %s not recoverable for %s of the %s",
				andList(group.fields), verb(len(group.fields), "is", "are"),
				ui.Commas(len(group.titles)), plural(verdict.rebuilt, "conversation")),
		}
		for i, title := range group.titles {
			if i == listedConversations {
				note.Data = append(note.Data, fmt.Sprintf("and %s more", ui.Commas(len(group.titles)-i)))
				break
			}
			note.Data = append(note.Data, title)
		}
		out = append(out, note)
	}
	return out
}

// listedConversations is how many conversations a warning names before it
// counts the rest. A hydrate can rebuild hundreds, and a list that long hides
// the warning it belongs to.
const listedConversations = 5

// A rebuiltVerdict is what the conversations a hydrate rebuilt came back
// without.
type rebuiltVerdict struct {
	// rebuilt is how many conversations it covers.
	rebuilt int
	// invented are the fields made up here, and lost the ones none of the
	// rebuilt conversations got back.
	invented, lost []string
	// some are the fields only some of them lost, with the conversations that
	// lost them.
	some []partlyLost
}

// partlyLost is a set of fields the same conversations lost and the others got
// back, and the titles of the ones that lost them.
type partlyLost struct {
	fields, titles []string
}

// verdictOn reads what was lost off each head the hydrate rebuilt.
//
// It is read head by head and not from the rebuild's verdict across all of them.
// That verdict keeps the weakest origin any head reported, which is what a claim
// about every rebuilt conversation has to be written against, and the wrong
// thing to tell a user about each one: a single conversation whose turns record
// no time made created and updated not recoverable for every conversation
// rebuilt beside it. It also took in the heads this device already held. A
// rebuild reconstructs those to report on and does not store them, so such a
// conversation keeps the head that was written and lost nothing here.
func verdictOn(report vault.HydrateReport) rebuiltVerdict {
	var heads []vault.RebuiltHead
	for _, head := range report.Rebuild.Heads {
		if !head.Existing && head.Session != nil {
			heads = append(heads, head)
		}
	}
	verdict := rebuiltVerdict{rebuilt: len(heads)}
	if len(heads) == 0 {
		return verdict
	}
	// Fields lost by the same conversations are said together, so a set of
	// them is found again by which conversations lost it.
	groups := make(map[string]int)
	for _, field := range vault.HeadFields {
		var lostBy []int
		invented := false
		for i, head := range heads {
			switch head.Origins[field] {
			case vault.OriginSynthesised:
				invented = true
			case vault.OriginLost:
				lostBy = append(lostBy, i)
			}
		}
		if invented {
			verdict.invented = append(verdict.invented, field)
		}
		switch {
		case len(lostBy) == len(heads):
			verdict.lost = append(verdict.lost, field)
		case len(lostBy) > 0:
			key := fmt.Sprint(lostBy)
			if at, ok := groups[key]; ok {
				verdict.some[at].fields = append(verdict.some[at].fields, field)
				continue
			}
			groups[key] = len(verdict.some)
			group := partlyLost{fields: []string{field}}
			for _, i := range lostBy {
				group.titles = append(group.titles, heads[i].Session.Title)
			}
			verdict.some = append(verdict.some, group)
		}
	}
	return verdict
}

// andList joins names the way a sentence lists them: "a", "a and b", "a, b and
// c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
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
