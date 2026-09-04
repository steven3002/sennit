package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

func runRemember(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("remember", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	context_ := fs.String("context", "", "what makes the statement resolvable on its own")
	memType := fs.String("type", string(record.TypeFact),
		"one of "+strings.Join(record.TypeNames(), ", "))
	tags := fs.String("tags", "", "comma-separated tags; prefer specific ones, and reuse the vault's existing vocabulary")
	supersedes := fs.String("supersedes", "", "the id of a record this one replaces")
	flush := fs.Bool("flush", true, "write to Sia before returning instead of leaving the record queued")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkFlagOrder(fs.Args()); err != nil {
		return err
	}
	statement := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if statement == "" {
		return fmt.Errorf("nothing to remember: pass the statement as an argument")
	}
	// Named here rather than left to the record validator, which explains why
	// the field matters but not what to type. A first run meets this before it
	// has met anything else.
	if strings.TrimSpace(*context_) == "" {
		return fmt.Errorf(
			"-context is required, and it is what makes the statement findable once it is separated "+
				"from the conversation it came from. Try:\n"+
				"  sennit remember -context \"why this matters and when it applies\" \"%s\"", statement)
	}

	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)

	req := vault.RememberRequest{
		Statement: statement,
		Context:   *context_,
		Type:      record.Type(*memType),
		Tags:      splitTags(*tags),
		Source:    record.Source{Origin: "cli", Client: "sennit"},
	}
	if *supersedes != "" {
		replaced, err := record.ParseID(*supersedes)
		if err != nil {
			return err
		}
		req.Supersedes = &replaced
	}

	result, err := v.Remember(ctx, req)
	if err != nil {
		return err
	}

	if *flush && !result.OnNetwork && v.Online() {
		flushed, err := v.Flush(ctx)
		if err != nil {
			return err
		}
		if flushed != nil {
			result.OnNetwork = true
			result.Flushed = flushed
		}
	}

	fmt.Println(result.ID)
	fmt.Fprintf(stderr, "  cid       %s\n", result.CID)
	reportAdvice(result)
	fmt.Fprintf(stderr, "  embed     %s\n", took(result.EmbedFor))
	fmt.Fprintf(stderr, "  seal      %s\n", took(result.SealFor))
	if result.OnNetwork && result.Flushed != nil {
		fmt.Fprintf(stderr, "  on Sia    %s in %d object(s), %d slab(s)\n",
			humanBytes(uint64(result.Flushed.Bytes())), len(result.Flushed.Written), len(result.Flushed.Slabs))
		fmt.Fprintf(stderr, "            upload %s · pin slabs %s · pin objects %s\n",
			took(result.Flushed.UploadFor), took(result.Flushed.PinSlabsFor), took(result.Flushed.PinObjectFor))
		fmt.Fprintf(stderr, "  object    %s\n", result.Flushed.Written[0].ObjectRef)
	} else {
		// Saying "saved" without this distinction would be the one dishonest
		// thing this interface could do: until a flush completes the record
		// exists on this device only.
		fmt.Fprintf(stderr, "  on Sia    not yet, held on this device, %d record(s) queued\n", v.Pending())
	}
	return nil
}

func splitTags(raw string) []string {
	var out []string
	for _, tag := range strings.Split(raw, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// reportAdvice prints what the vault noticed about the record just written.
//
// It is on stderr with the rest of the diagnostics because it is advice, not
// output: the caller decides whether a near-duplicate is a duplicate, and
// whether a tag is worth narrowing. The vault runs no model and does not decide
// either.
func reportAdvice(result vault.RememberResult) {
	for _, tag := range result.Tags.Tags {
		switch {
		case tag.New:
			fmt.Fprintf(stderr, "  tag       %-20s new to this vault\n", tag.Tag)
		case tag.TooCommon:
			fmt.Fprintf(stderr, "  tag       %-20s on %d of %d records (%.0f%%), too common to narrow a search\n",
				tag.Tag, tag.Records, result.Tags.Records, 100*tag.Share)
		default:
			fmt.Fprintf(stderr, "  tag       %-20s on %d of %d records\n",
				tag.Tag, tag.Records, result.Tags.Records)
		}
	}
	if result.Tags.NeedsNarrowerTags() {
		fmt.Fprintln(stderr, "  ⚠ none of these tags narrows a search of this vault; a more specific one would")
	}
	for _, conflict := range result.Conflicts {
		fmt.Fprintf(stderr, "  ⚠ near-duplicate [%.4f] %s\n", conflict.Similarity, conflict.ID)
		if conflict.Statement != "" {
			fmt.Fprintf(stderr, "              %s\n", conflict.Statement)
		}
	}
	if len(result.Conflicts) > 0 {
		fmt.Fprintln(stderr, "  decide whether this adds to the vault or replaces the record above (-supersedes)")
	}
}

// splitTypes parses a comma-separated type list for the recall filter.
func splitTypes(raw string) []record.Type {
	var out []record.Type
	for _, name := range splitTags(raw) {
		out = append(out, record.Type(name))
	}
	return out
}
