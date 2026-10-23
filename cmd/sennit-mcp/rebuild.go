package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/steven3002/sennit/vault"
)

// rebuildEvery is how long the server goes without a line while it rebuilds its
// search index. A host's log that says nothing for minutes reads as a server
// that has hung.
const rebuildEvery = 10 * time.Second

// rebuildProgress is the server's progress callback while its vault opens.
//
// A server opens its vault before it serves, and a vault that has found its
// search index damaged rebuilds it before it opens. That holds the start for as
// long as the embedding takes, hundreds of milliseconds a record, so the server
// says on stderr, which a host keeps as the server's log, that it is rebuilding
// and how far it has got, every ten seconds until it is done. Nothing else a
// vault reports as it opens is worth a line in that log.
//
// It touches nothing but for the rebuild, whose reports come one at a time,
// because the same callback hears every later upload, from several goroutines
// at once.
func rebuildProgress(w io.Writer, now func() time.Time) func(vault.Progress) {
	var started, said time.Time
	return func(p vault.Progress) {
		if p.Phase != vault.PhaseIndexRebuild || p.Total == 0 {
			return
		}
		at := now()
		switch {
		case p.Done == 0:
			started, said = at, at
			fmt.Fprintf(w, "sennit-mcp: rebuilding the search index before serving: %s to embed\n", records(p.Total))
		case p.Done < p.Total && at.Sub(said) >= rebuildEvery:
			said = at
			fmt.Fprintf(w, "sennit-mcp: still rebuilding the search index: %d of %s embedded, %s so far\n",
				p.Done, records(p.Total), at.Sub(started).Round(time.Second))
		}
	}
}

// notOpened says on stderr why the vault did not open.
//
// A host that stops waiting for the server to start stops the process, and so
// does a signal, and either cancels the open. A rebuild of the search index makes
// the open long enough for that to be ordinary. The server is on its way out
// then rather than serving, so it says it stopped. The explanation, and the
// promise to serve anyway, are for a vault that could not open for a reason
// someone has to fix.
func notOpened(w io.Writer, ctx context.Context, err error) {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		fmt.Fprintln(w, "sennit-mcp: stopped before serving")
		return
	}
	fmt.Fprintf(w, "sennit-mcp: %v\n", err)
	explain(w, err)
}

// rebuiltIndex says what the vault found wrong with its search index as it
// opened, and what it rebuilt before the server began serving.
func rebuiltIndex(r vault.IndexRepair) string {
	what := "the search index on this device was damaged, and has been rebuilt before serving"
	if r.Unfinished {
		what = "the search index on this device was damaged, and has been partly rebuilt before serving"
	}
	said := []string{damageFound(r)}
	switch {
	case r.Embedded > 0:
		said = append(said, fmt.Sprintf("every memory and conversation this device holds that had no vector "+
			"has been given one, %d in all", r.Embedded))
	case r.Failed == 0 && !r.Unfinished:
		said = append(said, "every memory and conversation this device holds still had its vector")
	}
	if r.Failed > 0 {
		said = append(said, fmt.Sprintf("%d could not be embedded, and %s found by %s words alone",
			r.Failed, pick(r.Failed, "is", "are"), pick(r.Failed, "its", "their")))
	}
	if r.Dropped > 0 {
		said = append(said, fmt.Sprintf("%d %s of %s this device no longer holds %s taken out", r.Dropped,
			pick(r.Dropped, "vector", "vectors"), pick(r.Dropped, "a record", "records"), pick(r.Dropped, "was", "were")))
	}
	if r.Unfinished {
		said = append(said, "records without a vector were still arriving from another process, and the next "+
			"start goes on with them")
	}
	said = append(said, "a conversation saved recently may be found by an earlier summary until it is next saved")
	return what + ": " + strings.Join(said, "; ")
}

// damageFound says what this start found wrong with the search index, or that
// an earlier run found it.
func damageFound(r vault.IndexRepair) string {
	skipped, lost := r.Found.Skipped, r.Found.Lost > 0
	switch {
	case skipped > 0 && lost:
		return records(int64(skipped)) + " in it could not be read, nor anything after a damaged record length"
	case skipped > 0:
		return records(int64(skipped)) + " in it could not be read"
	case lost:
		return "nothing in it after a damaged record length could be read"
	}
	return "an earlier run found it damaged"
}

func records(n int64) string {
	if n == 1 {
		return "1 record"
	}
	return fmt.Sprintf("%d records", n)
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
