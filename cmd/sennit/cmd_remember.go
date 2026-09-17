package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// onDevice is what an interrupted or failed write leaves behind, and it is the
// sentence this interface exists to be honest about: the memory is here, and it
// is not on Sia.
const onDevice = "The memory is saved on this device. It has not reached Sia yet."

func runRemember(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("remember").withVault().withVerbose()
	statementContext, memType, tags, supersedes, flush := rememberFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}
	if err := checkFlagOrder("remember", cmd.set.Args()); err != nil {
		return err
	}
	statement := strings.TrimSpace(strings.Join(cmd.set.Args(), " "))
	if statement == "" {
		return refuse("nothing to remember").try("pass the statement as an argument")
	}
	// Named here rather than left to the record validator, which explains why
	// the field matters but not what to type. A first run meets this before it
	// has met anything else.
	if strings.TrimSpace(*statementContext) == "" {
		return refuse("--context is required").
			because("It is what makes the statement findable once it is separated from the conversation it came from.").
			try(fmt.Sprintf("`sennit remember --context \"why this matters and when it applies\" %q`", statement))
	}

	v, err := cmd.vault.open(ctx, out, rememberPhases(out))
	if err != nil {
		return err
	}
	defer closing(out, v)

	req := vault.RememberRequest{
		Statement: statement,
		Context:   *statementContext,
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
	// Neither the connection to Sia nor the embedder stops for an interrupt, so
	// from here the memory is on this device however the run ends.
	out.leaves(onDevice)

	if *flush && !result.OnNetwork && v.Online() {
		flushed, err := v.Flush(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				// The memory is stored and the id is how it is reached, so it
				// is printed even though the upload failed.
				out.failed(onDevice)
				out.result(result.ID.String())
			}
			return err
		}
		if flushed != nil {
			result.OnNetwork = true
			result.Flushed = flushed
		}
	}

	reportRemembered(out, writeOutcome{
		result:   result,
		degraded: v.OfflineBecause() != nil,
		queued:   v.Pending(),
	})
	return nil
}

// rememberFlags is what sennit remember takes.
//
// --flush defaults to false because Sia bills a slab whole and a flush mints a
// new one, so waiting for the upload buys one 40 MiB slab for one memory of a
// few hundred bytes, and costs the whole upload before the command returns. The
// queue on the device is what the packer exists for: the record is sealed and
// durable here when this returns, and a later flush carries it, either the one
// a later write makes due or an explicit `sennit flush`. Nothing uploads on a
// timer at the command line. The flag stays for a caller that wants the wait.
func rememberFlags(cmd *invocation) (statementContext, memType, tags, supersedes *string, flush *bool) {
	return cmd.set.String("context", "", "what makes the statement resolvable on its own"),
		cmd.set.String("type", string(record.TypeFact), "one of "+strings.Join(record.TypeNames(), ", ")),
		cmd.set.String("tags", "", "comma-separated tags; prefer specific ones, and reuse the vault's existing vocabulary"),
		cmd.set.String("supersedes", "", "the id of a record this one replaces"),
		cmd.set.Bool("flush", false, "wait for the record to reach Sia instead of leaving it queued")
}

// A writeOutcome is what one remember achieved, in the terms the output needs:
// the record, whether the indexer answered at all, and what is still owed to
// the network.
type writeOutcome struct {
	result   vault.RememberResult
	degraded bool
	queued   int
}

// rememberPhases names what a write is doing, and keeps track of what an
// interrupt would leave: the model download is the one phase that stops before
// anything has been stored.
func rememberPhases(out *session) phaseNamer {
	write := out.writePhases("Uploading to Sia")
	return func(p vault.Progress) (string, string, bool) {
		switch p.Phase {
		case vault.PhaseModelFetch:
			out.leaves("The memory was not stored, and the model download did not finish.")
			return "Downloading embedding model", "", true
		case vault.PhaseEmbed:
			return "Embedding and encrypting", "", true
		}
		return write(p)
	}
}

// reportRemembered says which of the three things happened, and only the first
// of them may say the memory is on Sia.
//
// Saying "saved" without that distinction would be the one dishonest thing this
// interface could do: until a flush completes the record exists on this device
// alone.
func reportRemembered(out *session, outcome writeOutcome) {
	switch {
	case outcome.result.OnNetwork && outcome.result.Flushed != nil:
		out.done(ui.MarkSuccess, "Remembered, and stored on Sia")
	case outcome.degraded:
		out.done(ui.MarkWarning, "Remembered on this device only: the indexer did not answer, "+
			"the next connected flush uploads it")
	default:
		out.done(ui.MarkSuccess, "Remembered on this device, queued for Sia")
	}
	out.result(outcome.result.ID.String())
	out.detail(writeDetail(outcome)...)
	for _, note := range writeAdvice(outcome.result) {
		out.note(note)
	}
}

// writeDetail is the diagnostic block --verbose keeps, in the words it had
// before there was anywhere else to put it.
func writeDetail(outcome writeOutcome) []string {
	result := outcome.result
	lines := []string{fmt.Sprintf("  cid       %s", result.CID)}
	for _, tag := range result.Tags.Tags {
		switch {
		case tag.New:
			lines = append(lines, fmt.Sprintf("  tag       %-20s new to this vault", tag.Tag))
		case tag.TooCommon:
			lines = append(lines, fmt.Sprintf("  tag       %-20s on %d of %d records (%.0f%%), too common to narrow a search",
				tag.Tag, tag.Records, result.Tags.Records, 100*tag.Share))
		default:
			lines = append(lines, fmt.Sprintf("  tag       %-20s on %d of %d records", tag.Tag, tag.Records, result.Tags.Records))
		}
	}
	lines = append(lines,
		fmt.Sprintf("  embed     %s", took(result.EmbedFor)),
		fmt.Sprintf("  seal      %s", took(result.SealFor)))
	if result.OnNetwork && result.Flushed != nil {
		lines = append(lines,
			fmt.Sprintf("  on Sia    %s in %d object(s), %d slab(s)",
				humanBytes(uint64(result.Flushed.Bytes())), len(result.Flushed.Written), len(result.Flushed.Slabs)),
			fmt.Sprintf("            upload %s · pin slabs %s · pin objects %s",
				took(result.Flushed.UploadFor), took(result.Flushed.PinSlabsFor), took(result.Flushed.PinObjectFor)))
		if len(result.Flushed.Written) > 0 {
			lines = append(lines, fmt.Sprintf("  object    %s", result.Flushed.Written[0].ObjectRef))
		}
		return lines
	}
	return append(lines, fmt.Sprintf("  on Sia    not yet, held on this device, %d record(s) queued", outcome.queued))
}

// writeAdvice is what the vault noticed about the record just written.
//
// It is advice rather than output: the caller decides whether a near-duplicate
// is a duplicate and whether a tag is worth narrowing. The vault runs no model
// and does not decide either.
func writeAdvice(result vault.RememberResult) []ui.Message {
	var out []ui.Message
	for i, conflict := range result.Conflicts {
		note := ui.Message{
			Kind: ui.KindWarning,
			Text: "a very similar memory is already in this vault",
			Data: []string{fmt.Sprintf("%s  match %.2f", conflict.ID, conflict.Similarity)},
		}
		if conflict.Statement != "" {
			note.Data = append(note.Data, conflict.Statement)
		}
		if i == len(result.Conflicts)-1 {
			note.Hints = []string{"decide whether this adds to the vault or replaces the record above (`--supersedes`)"}
		}
		out = append(out, note)
	}

	if result.Tags.NeedsNarrowerTags() {
		note := ui.Message{
			Kind: ui.KindHint,
			Text: "none of these tags narrows a search of this vault; a more specific one would",
		}
		labels := 0
		for _, tag := range result.Tags.Tags {
			labels = max(labels, width.String(tag.Tag))
		}
		for _, tag := range result.Tags.Tags {
			note.Data = append(note.Data, fmt.Sprintf("%s  on %d of %d records (%.0f%%)",
				width.Pad(tag.Tag, labels), tag.Records, result.Tags.Records, 100*tag.Share))
		}
		return append(out, note)
	}
	for _, tag := range result.Tags.Tags {
		if !tag.TooCommon {
			continue
		}
		out = append(out, ui.Message{
			Kind: ui.KindHint,
			Text: fmt.Sprintf("%s is on %d of %d records (%.0f%%), too common to narrow a search",
				tag.Tag, tag.Records, result.Tags.Records, 100*tag.Share),
		})
	}
	return out
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

// splitTypes parses a comma-separated type list for the recall filter.
func splitTypes(raw string) []record.Type {
	var out []record.Type
	for _, name := range splitTags(raw) {
		out = append(out, record.Type(name))
	}
	return out
}
