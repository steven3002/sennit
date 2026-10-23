package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/vault"
)

// refusedUploadPhrase is the recovery phrase of the vaults here. It is the
// well-known test phrase, so nothing written with it is private.
const refusedUploadPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

// vaultWithoutHosts opens a vault as remember does, with its progress named on
// the screen, over an indexer that refuses every upload for want of hosts.
// policy decides when a write sets off a flush of its own, and the zero policy
// is the one a command runs with. uploads reports how many uploads were begun.
func vaultWithoutHosts(t *testing.T, s *screen, policy packer.Policy) (v *vault.Vault, uploads func() int64) {
	t.Helper()
	indexer, key := siatest.IndexerWithoutHosts(t)
	var begun atomic.Int64
	watch := s.out.watch(rememberPhases(s.out))
	v, err := vault.Open(context.Background(), vault.Options{
		Home:     t.TempDir(),
		Phrase:   refusedUploadPhrase,
		Embedder: embedtest.NewStub(),
		Indexer:  indexer,
		AppKey:   key,
		Flush:    policy,
		OnProgress: func(p vault.Progress) {
			// An upload reports its total before its first shard, and only
			// then with nothing done.
			if p.Phase == vault.PhaseUpload && p.Done == 0 {
				begun.Add(1)
			}
			watch(p)
		},
	})
	if err != nil {
		t.Fatalf("open a vault over an indexer with no hosts: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v, begun.Load
}

// refusedUploadMemory is the memory each run here writes while the network
// refuses its upload.
func refusedUploadMemory() vault.RememberRequest {
	return vault.RememberRequest{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the fictional Tidepool field guide, written while the network refused every upload.",
		Type:      record.TypeFact,
		Tags:      []string{"harbour", "gauge"},
		Source:    record.Source{Origin: "cli", Client: "sennit"},
	}
}

// onlyMemory is the id of the one memory a vault holds, failing the test when
// it holds any other number.
func onlyMemory(t *testing.T, v *vault.Vault) string {
	t.Helper()
	page, err := v.Browse(vault.BrowseRequest{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("the vault holds %d record(s), want the one memory the run stored", len(page.Rows))
	}
	return page.Rows[0].ID.String()
}

// flat is a screen's text with every run of spaces and line breaks made one
// space, so a sentence can be looked for however the terminal wrapped it.
func flat(text string) string { return strings.Join(strings.Fields(text), " ") }

// A remember whose upload failed succeeds and warns: the memory is saved and
// stays queued, the error says why the upload failed, and the hint names the
// command that retries it.
func TestWhatARememberWhoseUploadFailedSays(t *testing.T) {
	s := ended(t, 80, 12300*time.Millisecond)
	reportRemembered(s.out, writeOutcome{queued: 2, result: vault.RememberResult{
		ID: id(t, "9c2e41d07a5b38f6e1d4c0b7a2958e13"),
		FlushFailed: errors.New("upload batch of 2: finalize packed upload: " +
			"not enough hosts available to upload slab 0: 22 < 30"),
	}})
	s.assert(t, "an upload that failed", fixture(t, "remember-upload-failed-queued.txt"))
}

// A flush that failed only after its upload had put the memory on Sia leaves
// nothing about the memory to retry, so the write says the memory is on Sia,
// and the warning names what failed after it.
func TestAFlushThatFailedAfterStoringTheMemoryOnSiaSaysItIsOnSia(t *testing.T) {
	s := ended(t, 80, 24900*time.Millisecond)
	reportRemembered(s.out, writeOutcome{result: vault.RememberResult{
		ID:          id(t, "9c2e41d07a5b38f6e1d4c0b7a2958e13"),
		OnNetwork:   true,
		Flushed:     &store.Flush{},
		FlushFailed: errors.New("catalog 9c2e41d07a5b38f6e1d4c0b7a2958e13: no space left on device"),
	}})
	lines := strings.Split(s.text(), "\n")
	if lines[0] != "✓ Remembered, and stored on Sia (24.9s)" {
		t.Errorf("the write ended with %q", lines[0])
	}
	said := flat(s.text())
	for _, want := range []string{
		"WARNING the flush that stored it on Sia failed afterwards, on this device: " +
			"catalog 9c2e41d07a5b38f6e1d4c0b7a2958e13: no space left on device",
		"The memory is on Sia all the same, " + secondCopy,
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the screen does not say %q:\n%s", want, s.text())
		}
	}
	for _, never := range []string{"queued", "retries"} {
		if strings.Contains(said, never) {
			t.Errorf("the screen says %q of a memory already on Sia:\n%s", never, s.text())
		}
	}
}

// remember --flush whose upload the network refuses exits with an error, and
// says what the run left: the memory is saved, its id, and that it is not to be
// remembered again, because `sennit flush` retries the upload.
func TestRememberFlushWhoseUploadIsRefusedSaysTheMemoryIsStored(t *testing.T) {
	s := ended(t, 80, 12300*time.Millisecond)
	v, _ := vaultWithoutHosts(t, s, packer.Policy{})

	err := rememberIn(t.Context(), s.out, v, refusedUploadMemory(), true)
	if code := report(t.Context(), s.out, err); code != 1 {
		t.Errorf("exit %d, want 1: the upload the run was asked to wait for did not happen", code)
	}
	stored := onlyMemory(t, v)
	lines := strings.Split(s.text(), "\n")
	for i, want := range []string{"✗ Failed while uploading to Sia (12.3s)", "  " + onDevice, stored} {
		if i >= len(lines) || lines[i] != want {
			t.Fatalf("line %d is not %q:\n%s", i+1, want, s.text())
		}
	}
	said := flat(s.text())
	for _, want := range []string{
		"ERROR", "not enough hosts", "The memory stays queued, " + secondCopy, "HINT " + retryUpload,
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the screen does not say %q:\n%s", want, s.text())
		}
	}
	if pending := v.Pending(); pending != 1 {
		t.Errorf("%d record(s) queued for Sia, want the memory", pending)
	}
}

// A plain remember whose own upload the network refuses still succeeds, because
// the memory was stored before the upload began, and warns that the upload
// failed, why, and what retries it.
func TestARememberWhoseOwnUploadIsRefusedSucceedsAndWarns(t *testing.T) {
	s := ended(t, 80, 12300*time.Millisecond)
	v, uploads := vaultWithoutHosts(t, s, packer.Immediate(store.DefaultSlabPayloadSize))

	err := rememberIn(t.Context(), s.out, v, refusedUploadMemory(), false)
	if code := report(t.Context(), s.out, err); code != 0 {
		t.Errorf("exit %d, want 0: the memory was stored", code)
	}
	if n := uploads(); n != 1 {
		t.Errorf("%d upload(s) begun, want the one the write set off", n)
	}
	stored := onlyMemory(t, v)
	lines := strings.Split(s.text(), "\n")
	for i, want := range []string{
		"! Remembered on this device only: the upload failed, the next flush retries it (12.3s)", stored,
	} {
		if i >= len(lines) || lines[i] != want {
			t.Fatalf("line %d is not %q:\n%s", i+1, want, s.text())
		}
	}
	said := flat(s.text())
	for _, want := range []string{
		"WARNING the upload failed: ", "not enough hosts",
		"The memory is saved on this device and stays queued, " + secondCopy, "HINT " + retryUpload,
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the screen does not say %q:\n%s", want, s.text())
		}
	}
	if pending := v.Pending(); pending != 1 {
		t.Errorf("%d record(s) queued for Sia, want the memory", pending)
	}
}

// remember --flush does not try a second time an upload its own write has just
// tried and failed. It ends with that upload's failure, and says what the run
// left, as it does when the upload it asked for fails.
func TestRememberFlushDoesNotTryAFailedUploadTwice(t *testing.T) {
	s := ended(t, 80, 12300*time.Millisecond)
	v, uploads := vaultWithoutHosts(t, s, packer.Immediate(store.DefaultSlabPayloadSize))

	err := rememberIn(t.Context(), s.out, v, refusedUploadMemory(), true)
	if code := report(t.Context(), s.out, err); code != 1 {
		t.Errorf("exit %d, want 1: the upload the run was asked to wait for did not happen", code)
	}
	if n := uploads(); n != 1 {
		t.Errorf("%d upload(s) begun, want only the one the write set off", n)
	}
	stored := onlyMemory(t, v)
	lines := strings.Split(s.text(), "\n")
	for i, want := range []string{"✗ Failed while uploading to Sia (12.3s)", "  " + onDevice, stored} {
		if i >= len(lines) || lines[i] != want {
			t.Fatalf("line %d is not %q:\n%s", i+1, want, s.text())
		}
	}
	if said := flat(s.text()); !strings.Contains(said, "The memory stays queued, "+secondCopy) ||
		!strings.Contains(said, "HINT "+retryUpload) {
		t.Errorf("the screen does not say the memory stays queued and what retries it:\n%s", s.text())
	}
}
