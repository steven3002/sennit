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

	reportRecall(out, result, hits, *options.memoryText, *options.asJSON)
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
func reportRecall(out *session, result recall.Result, hits []recall.Hit, memoryText, asJSON bool) {
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
	if len(rows) == 0 && result.Searched == 0 {
		out.note(ui.Message{Kind: ui.KindHint,
			Text: "this vault holds nothing searchable yet; `sennit remember` stores the first record"})
	}
	if result.SupersededHidden > 0 {
		out.note(ui.Message{Kind: ui.KindHint,
			Text: fmt.Sprintf("%s held back; `--history` returns %s",
				plural(result.SupersededHidden, "superseded version"), them(result.SupersededHidden))})
	}
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
