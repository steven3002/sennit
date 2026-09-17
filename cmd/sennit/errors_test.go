package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// shown is what a failure puts on screen.
func shown(t *testing.T, columns int, err error) string {
	t.Helper()
	s := terminal(t, columns)
	report(t.Context(), s.out, err)
	return s.text()
}

// The three failures a first run actually hits are answered with instructions,
// and each of them keeps every sentence the old wording carried.
func TestTheFirstRunFailures(t *testing.T) {
	if got, want := shown(t, 80, keys.ErrNoAppKey), fixture(t, "error-no-app-key.txt"); got != want {
		t.Errorf("no app key:\n got %q\nwant %q", got, want)
	}
	if got, want := shown(t, 80, keys.AppKey(make([]byte, 4)).Check()), fixture(t, "error-app-key-length.txt"); got != want {
		t.Errorf("a damaged key:\n got %q\nwant %q", got, want)
	}
	got := shown(t, 80, keys.ErrNoPhrase)
	want := uitest.Lines(
		" ERROR  no recovery phrase",
		"  Sennit derives the vault's keys from the phrase on every run",
		"  and never stores it.",
		"",
		" HINT   set SENNIT_PHRASE, or pipe the phrase in on stdin",
		" HINT   no phrase yet? `sennit init --new-phrase` prints one and",
		"        stores nothing",
	)
	if got != want {
		t.Errorf("no phrase:\n got %q\nwant %q", got, want)
	}
}

// An indexer failure separates what failed from what to do about it, and never
// carries the signed request URL or the response body.
func TestAnIndexerFailure(t *testing.T) {
	err := &sia.IndexerError{
		Op: "read the account", Indexer: "https://sia.storage", Status: 502,
		Advice: "The indexer or the CDN in front of it is having trouble. This is transient and " +
			"retryable; nothing was lost, because a failed write leaves the records queued on this device",
	}
	if got, want := shown(t, 80, err), fixture(t, "error-indexer.txt"); got != want {
		t.Errorf("an indexer failure:\n got %q\nwant %q", got, want)
	}
}

// An error nothing rewrites is shown as the code stated it.
func TestAnErrorWithoutARewrite(t *testing.T) {
	got := shown(t, 80, errors.New("decode SENNIT_APP_KEY: encoding/hex: invalid byte: U+007A 'z'"))
	want := uitest.Lines(
		" ERROR  decode SENNIT_APP_KEY: encoding/hex: invalid byte:",
		"        U+007A 'z'")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := shown(t, 80, vault.ErrWrongPhrase); got != " ERROR  recovery phrase does not match this vault\n" {
		t.Errorf("a wrong phrase: %q", got)
	}
}

// A command that cannot work without the indexer says which of the two
// situations it is in, because they need opposite responses.
func TestACommandThatNeedsTheIndexer(t *testing.T) {
	offline := needsIndexer("reclaim", &vault.Vault{}, "")
	if got, want := offline.message.Text, "reclaim needs the indexer, and this run is offline"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(offline.message.Hints) != 1 || offline.message.Hints[0] != "run it without `--offline`" {
		t.Errorf("hints %v", offline.message.Hints)
	}
	queued := needsIndexer("flush", &vault.Vault{}, "The 37 records stay queued on this device.")
	if got, want := shown(t, 80, queued), fixture(t, "flush-offline.txt"); got != want {
		t.Errorf("an offline flush:\n got %q\nwant %q", got, want)
	}
}

// The refusals reclaim makes rather than deleting something it cannot account
// for.
func TestTheReclaimRefusals(t *testing.T) {
	s := terminal(t, 80)
	for i, p := range []*problem{
		refuse("3 records are queued and not yet on the network").try("flush before reclaiming: `sennit flush`"),
		refuse("repack needs 120.00 MiB free to hold the old and new slabs at once, and there is less than that"),
		refuse("the catalog holds no records while 1 slab is pinned").
			because("That is what an emptied vault looks like and also what a lost or unreadable catalog looks like.").
			try("check the vault is opened against the right home directory",
				"if the vault really is empty, `sennit reclaim --release-all` releases its storage"),
	} {
		if i > 0 {
			s.out.stderr.Write([]byte("\n"))
		}
		for _, line := range s.out.messageLines(p.message) {
			fmt.Fprintln(s.out.stderr, line)
		}
	}
	if got, want := s.text(), fixture(t, "reclaim-refusals.txt"); got != want {
		t.Errorf("the refusals:\n got %q\nwant %q", got, want)
	}
}

func TestTheConnectRefusals(t *testing.T) {
	s := terminal(t, 80)
	for i, p := range []*problem{
		refuse("--out is required").
			because("The app key is a secret and is written to a file rather than printed, " +
				"so it does not land in a terminal's scrollback or a recording.").
			try("`sennit connect --out sennit.key`"),
		refuse("the connection was declined at the indexer"),
		refuse("no approval within 30m0s: 3 request(s) were issued at https://sia.storage and each expired unapproved"),
	} {
		if i > 0 {
			s.out.stderr.Write([]byte("\n"))
		}
		for _, line := range s.out.messageLines(p.message) {
			fmt.Fprintln(s.out.stderr, line)
		}
	}
	if got, want := s.text(), fixture(t, "connect-refusals.txt"); got != want {
		t.Errorf("the refusals:\n got %q\nwant %q", got, want)
	}
}

// A flag written after the text is still refused, and the hint shows the form
// to type rather than echoing what was typed.
func TestAFlagAfterTheText(t *testing.T) {
	err := checkFlagOrder("recall", []string{"the query", "-limit", "3"})
	if err == nil {
		t.Fatal("a flag after the text was accepted")
	}
	got := shown(t, 80, err)
	want := uitest.Lines(
		` ERROR  "-limit" looks like a flag but comes after the text,`,
		"        where it is read as part of it",
		"",
		"[[hint]] flags go first: `sennit recall --limit n \"<text>\"`")
	want = strings.Replace(want, "[[hint]]", " HINT  ", 1)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if err := checkFlagOrder("recall", []string{"the query"}); err != nil {
		t.Errorf("an ordinary query was refused: %v", err)
	}
	if err := checkFlagOrder("remember", []string{"a statement", "-"}); err != nil {
		t.Errorf("a lone dash was taken for a flag: %v", err)
	}
}

// A flag the command does not have, and a value it cannot read, keep the flag
// package's own words and gain a hint. The exit code stays 2.
func TestFlagFailures(t *testing.T) {
	for _, c := range []struct{ args, text string }{
		{"-x", "flag provided but not defined: -x"},
		{"--limit=abc", `invalid value "abc" for flag -limit: parse error`},
		{"--color=sometimes", `invalid value "sometimes" for flag -color: parse error`},
	} {
		s := terminal(t, 80)
		cmd := newInvocation("recall").withVault().withVerbose()
		recallFlags(cmd)
		err := cmd.parse(s.out, []string{c.args, "a query"})
		if err == nil {
			t.Fatalf("%s was accepted", c.args)
		}
		if got := shown(t, 80, err); !strings.Contains(got, c.text) ||
			!strings.Contains(got, "`sennit recall --help` lists its flags") {
			t.Errorf("%s:\n%s", c.args, got)
		}
		if statusOf(err) != 2 {
			t.Errorf("%s: exit %d, want 2", c.args, statusOf(err))
		}
	}
}

// An interrupt says what it stopped and what that left, and never reports the
// cancellation as an error of its own.
func TestAnInterruptSaysWhatItLeft(t *testing.T) {
	interrupted, cancel := context.WithCancel(t.Context())
	cancel()
	s := ended(t, 80, 86*time.Second)
	s.out.phase("Uploading to Sia")
	s.out.leaves(onDevice)
	code := report(interrupted, s.out, fmt.Errorf("upload batch of 1: %w", context.Canceled))
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	want := uitest.Lines(
		"✗ Cancelled while uploading to Sia (1m26s)",
		"  "+onDevice)
	if got := s.text(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Nothing a reader relies on changes when a search is interrupted, so there is
// no second line.
func TestAnInterruptedSearchSaysOnlyThat(t *testing.T) {
	interrupted, cancel := context.WithCancel(t.Context())
	cancel()
	s := ended(t, 80, 6200*time.Millisecond)
	s.out.phase("Connecting to Sia")
	report(interrupted, s.out, context.Canceled)
	if got, want := s.text(), "✗ Cancelled while connecting to Sia (6.2s)\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A vault that did not close cleanly is the only notice that the next run may
// have something to recover, and it comes after the command's own outcome so it
// never reads as the reason one failed.
func TestACloseFailureIsReportedLast(t *testing.T) {
	s := ended(t, 80, time.Second)
	s.out.done(ui.MarkSuccess, "Flushed 3 records to Sia")
	s.out.closeWarning = errors.New("close the device store: database is locked")
	report(t.Context(), s.out, nil)
	want := uitest.Lines(
		"✓ Flushed 3 records to Sia (1.0s)",
		"",
		" WARNING  the vault did not close cleanly: close the device",
		"          store: database is locked")
	if got := s.text(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
