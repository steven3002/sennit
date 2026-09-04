package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/steven3002/sennit/recall"
)

func runRecall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recall", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	limit := fs.Int("limit", recall.DefaultLimit, "how many records to return")
	tags := fs.String("tags", "", "comma-separated tags the answer is likely to carry; these prefer, never exclude")
	types := fs.String("types", "", "comma-separated record types that could answer this")
	history := fs.Bool("history", false, "include versions that have since been superseded")
	fromNetwork := fs.Bool("from-network", false,
		"read bodies from Sia even when this device holds a copy, to check the stored data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkFlagOrder(fs.Args()); err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("nothing to recall: pass the query as an argument")
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	result, err := v.Recall(ctx, recall.Request{
		Query:             query,
		Limit:             *limit,
		Filter:            recall.Filter{Tags: splitTags(*tags), Types: splitTypes(*types)},
		IncludeSuperseded: *history,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stderr, "  embed %s · search %s over %d vector(s) and %d term match(es) · fetch %s\n",
		took(result.EmbedFor), took(result.SearchFor), result.Searched,
		result.LexicalHits, took(result.FetchFor))
	if result.SupersededHidden > 0 {
		fmt.Fprintf(stderr, "  %d superseded version(s) held back; -history returns them\n",
			result.SupersededHidden)
	}

	// No results is an answer, not a failure. A memory store that returns its
	// least-bad guess when it holds nothing relevant is worse than one that
	// says so.
	if len(result.Hits) == 0 {
		fmt.Fprintln(stderr, "  no records matched")
		return nil
	}

	for n, hit := range result.Hits {
		source, elapsed := string(hit.Tier), hit.FetchedIn
		if session := hit.Session; session != nil {
			// A session ranks beside memories and is rendered differently,
			// because what a caller wants from one is different: a memory is an
			// answer, a session is somewhere to go and look.
			fmt.Printf("%d. [%.4f] %s\n", n+1, hit.Similarity, session.Title)
			if why := ranked(hit); why != "" {
				fmt.Printf("   %s\n", why)
			}
			if session.Summary != "" {
				fmt.Printf("   summary: %s\n", session.Summary)
			}
			fmt.Printf("   %s · session · %d message(s) in %d chunk(s)",
				session.ID, session.Counts.Messages, len(session.Chunks))
			if len(session.Tags) > 0 {
				fmt.Printf(" · %s", strings.Join(session.Tags, ", "))
			}
			fmt.Printf(" · from %s in %s\n", source, took(elapsed))
			continue
		}

		memory := hit.Memory
		if *fromNetwork {
			start := time.Now()
			fetched, err := v.FetchMemoryFromNetwork(ctx, memory.ID)
			if err != nil {
				return err
			}
			memory, source, elapsed = fetched, "network", time.Since(start)
		}
		// The leading number is similarity, not the score the record was ranked
		// on. Ranking fuses two passes and the fused score is in units of rank,
		// so it is meaningful only against the other hits in this list and would
		// read as a uniformly terrible match if printed. Similarity is the one
		// number here that means the same thing every time it is shown.
		fmt.Printf("%d. [%.4f] %s\n", n+1, hit.Similarity, memory.Statement)
		if why := ranked(hit); why != "" {
			fmt.Printf("   %s\n", why)
		}
		if memory.Context != "" {
			fmt.Printf("   context: %s\n", memory.Context)
		}
		fmt.Printf("   %s · %s", memory.ID, memory.Type)
		if len(memory.Tags) > 0 {
			fmt.Printf(" · %s", strings.Join(memory.Tags, ", "))
		}
		fmt.Printf(" · from %s in %s\n", source, took(elapsed))
	}
	return nil
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
	// A record the vector pass never scored has no similarity to report, and
	// printing the zero it carries would read as "no relation to the query".
	prefix := "also ranked up by "
	if hit.Similarity == 0 {
		prefix = "found only by "
	}
	return prefix + strings.Join(why, " and ")
}
