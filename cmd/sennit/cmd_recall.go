package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

func runRecall(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("recall").withVault().withVerbose()
	options := recallFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}
	if err := checkFlagOrder("recall", cmd.set.Args()); err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(cmd.set.Args(), " "))
	if query == "" {
		return refuse("nothing to recall").try("pass the query as an argument")
	}

	v, err := cmd.vault.open(ctx, out, recallPhases(out))
	if err != nil {
		return err
	}
	defer closing(out, v)

	result, err := v.Recall(ctx, recall.Request{
		Query:             query,
		Limit:             *options.limit,
		Filter:            recall.Filter{Tags: splitTags(*options.tags), Types: splitTypes(*options.types)},
		IncludeSuperseded: *options.history,
	})
	if err != nil {
		return err
	}
	hits := result.Hits
	if *options.fromNetwork {
		if !v.Online() {
			return needsIndexer("--from-network", v, "")
		}
		if hits, err = refetch(ctx, out, v, hits); err != nil {
			return err
		}
	}
	// What the search could reach is this device, so how much of the vault the
	// device has restored decides what the answer can be said to show.
	restoration, err := v.Restoration()
	if err != nil {
		return err
	}

	reportRecall(out, result, hits, restoration, *options.memoryText, *options.asJSON)
	return nil
}

// recallOptions are the flags one search was given.
type recallOptions struct {
	limit                                    *int
	tags, types                              *string
	history, fromNetwork, memoryText, asJSON *bool
}

// recallFlags is what sennit recall takes.
func recallFlags(cmd *invocation) recallOptions {
	return recallOptions{
		limit:       cmd.set.Int("limit", recall.DefaultLimit, "how many records to return"),
		tags:        cmd.set.String("tags", "", "comma-separated tags the answer is likely to carry; these prefer, never exclude"),
		types:       cmd.set.String("types", "", "comma-separated record types that could answer this"),
		history:     cmd.set.Bool("history", false, "include versions that have since been superseded"),
		fromNetwork: cmd.set.Bool("from-network", false, "read bodies from Sia even when this device holds a copy, to check the stored data"),
		memoryText:  cmd.set.Bool("memory-text", false, "show each result's full text, its context and its id"),
		asJSON:      cmd.set.Bool("json", false, "print the results as JSON"),
	}
}

// refetch reads each memory back from Sia, which is how a reader checks what is
// actually stored rather than what this device remembers storing.
func refetch(ctx context.Context, out *session, v *vault.Vault, hits []recall.Hit) ([]recall.Hit, error) {
	memories := 0
	for _, h := range hits {
		if h.Memory != nil {
			memories++
		}
	}
	if memories == 0 {
		return hits, nil
	}
	out.phase("Reading " + plural(memories, "memory") + " from Sia")
	read := 0
	for i := range hits {
		if hits[i].Memory == nil {
			continue
		}
		start := time.Now()
		fetched, err := v.FetchMemoryFromNetwork(ctx, hits[i].Memory.ID)
		if err != nil {
			return nil, err
		}
		read++
		hits[i].Memory, hits[i].Tier, hits[i].FetchedIn = fetched, recall.TierNetwork, time.Since(start)
		out.line().Detail(fmt.Sprintf("%d of %d", read, memories))
	}
	return hits, nil
}

// reportRecall prints the answer: the table on a terminal, tab separated
// through a pipe, and JSON when it was asked for.
func reportRecall(out *session, result recall.Result, hits []recall.Hit, restoration vault.Restoration,
	memoryText, asJSON bool) {
	memories, conversations := 0, 0
	rows := make([]hit, 0, len(hits))
	for _, h := range hits {
		if h.Session != nil {
			conversations++
		} else {
			memories++
		}
		rows = append(rows, viewHit(h, out.verbose))
	}
	out.done(ui.MarkSuccess, found(memories, conversations))

	switch {
	case asJSON:
		encoded, err := recallJSON(result, hits)
		if err == nil {
			out.result(string(encoded))
		}
	case len(rows) == 0:
	case !out.policy.StdoutTerminal:
		out.result(recallTSV(rows)...)
	default:
		out.block(recallTable(rows, out.outWidth(), memoryText, out.verbose))
	}

	out.detail(fmt.Sprintf("  embed %s · search %s over %d vector(s) and %d term match(es) · fetch %s",
		took(result.EmbedFor), took(result.SearchFor), result.Searched,
		result.LexicalHits, took(result.FetchFor)))

	// Returning nothing is an answer, not a failure. A memory store that
	// returns its least-bad guess when it holds nothing relevant is worse than
	// one that says so, and a vault that holds nothing searchable is a
	// different situation that looks identical from the outside.
	if hint, ok := searchedHint(len(rows) == 0 && result.Searched == 0, restoration); ok {
		out.note(hint)
	}
	if result.SupersededHidden > 0 {
		out.note(ui.Message{Kind: ui.KindHint,
			Text: fmt.Sprintf("%s held back; `--history` returns %s",
				plural(result.SupersededHidden, "superseded version"), them(result.SupersededHidden))})
	}
}

// searchedHint says what a search on this device could reach, from the check
// the server makes for the same answer: how much of what its catalog names this
// device has restored.
//
// With nothing to search, that check is what tells an empty vault from a
// device holding nothing of it. One that never restored the vault holds
// nothing whatever the vault holds, and one that knows of records it has not
// restored holds part of it. The second says so with its results too, because
// they come from the part it holds and look no different from a whole vault's.
func searchedHint(nothingSearched bool, restoration vault.Restoration) (ui.Message, bool) {
	hint := func(text string) (ui.Message, bool) { return ui.Message{Kind: ui.KindHint, Text: text}, true }
	switch {
	case nothingSearched && restoration.Untouched:
		return hint("nothing on this device is searchable yet, and it has not restored any record of this " +
			"vault from Sia, so this does not show the vault is empty; `sennit hydrate --depth index` " +
			"restores its records unless it was just created here, and `sennit remember` stores new ones")
	case nothingSearched && !restoration.Complete():
		return hint(fmt.Sprintf("nothing on this device is searchable yet, though it knows of %s on Sia "+
			"that it has not restored; `sennit hydrate --depth index` restores this vault's records and "+
			"makes them searchable", unrestored(restoration)))
	case nothingSearched:
		return hint("this vault holds nothing searchable yet; `sennit remember` stores the first record")
	case !restoration.Complete():
		return hint(fmt.Sprintf("this device knows of %s on Sia that it has not restored, so the search "+
			"could not reach them; `sennit hydrate --depth index` restores this vault's records and makes "+
			"them searchable", unrestored(restoration)))
	}
	return ui.Message{}, false
}

// unrestored names what this device knows of on Sia and has not restored, as
// the server's answers name it. Memories are counted and conversations are not:
// a conversation is catalogued as the chunks of its transcript, and which one a
// chunk belongs to is written inside the chunk, which this device has not read.
func unrestored(restoration vault.Restoration) string {
	memories := plural(restoration.Memories, "memory")
	switch {
	case restoration.Memories > 0 && restoration.Chunks > 0:
		return memories + " and at least one conversation"
	case restoration.Chunks > 0:
		return "at least one conversation"
	}
	return memories
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// viewHit is one result in the form the output shows it.
func viewHit(h recall.Hit, verbose bool) hit {
	row := hit{
		// The number shown is similarity, not the score the record was ranked
		// on. Ranking fuses two passes and the fused score is in units of rank,
		// so it is meaningful only against the other hits in this list and
		// would read as a uniformly terrible match if printed. Similarity is
		// the one number here that means the same thing every time it is shown.
		similarity: fmt.Sprintf("%.2f", h.Similarity),
		full:       fmt.Sprintf("%.4f", h.Similarity),
		kind:       string(h.Kind()),
		note:       ranked(h),
	}
	if h.Similarity == 0 {
		// A record the vector pass never scored has no similarity, and a 0.00
		// would read as unrelated rather than as unscored.
		row.similarity = "-"
	}
	if verbose {
		row.read = fmt.Sprintf("%s %s", h.Tier, took(h.FetchedIn))
	}
	if session := h.Session; session != nil {
		row.typ, row.text, row.context = string(record.KindSession), session.Title, session.Summary
		row.tags, row.id = session.Tags, session.ID.String()
		row.holds = plural(session.Counts.Messages, "message") + " in " + plural(len(session.Chunks), "chunk")
		return row
	}
	memory := h.Memory
	row.typ, row.text, row.context = string(memory.Type), memory.Statement, memory.Context
	row.tags, row.id = memory.Tags, memory.ID.String()
	return row
}

// ranked names the signals beyond similarity that put a record where it is, so
// a caller can tell a record found by meaning from one found by its words or
// promoted by the filter it supplied.
func ranked(hit recall.Hit) string {
	var why []string
	if hit.Lexical > 0 {
		why = append(why, "words it shares with the query")
	}
	if hit.Boost > 0 {
		why = append(why, "the tags and types asked for")
	}
	if len(why) == 0 {
		return ""
	}
	prefix := "also ranked up by "
	if hit.Similarity == 0 {
		prefix = "found only by "
	}
	return prefix + strings.Join(why, " and ")
}
