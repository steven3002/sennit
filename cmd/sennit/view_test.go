package main

import (
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/reclaim"
	"github.com/steven3002/sennit/vault"
)

// ended is a screen whose command has finished, with the time it took.
func ended(t *testing.T, columns int, elapsed time.Duration) *screen {
	t.Helper()
	s := terminal(t, columns)
	start := time.Now()
	first := true
	s.out.clock = func() time.Time {
		if first {
			first = false
			return start
		}
		return start.Add(elapsed)
	}
	return s
}

func account(used, maximum uint64) sia.Account {
	return sia.Account{Ready: true, PinnedData: used, MaxPinnedData: maximum}
}

const (
	mib = uint64(1) << 20
	gib = uint64(1) << 30
)

// The three states a write ends in, and the advice that follows one.
func TestWhatRememberSays(t *testing.T) {
	recordID := id(t, "4083dbdd9e9f0f816c797080eed61fba")
	stored := vault.RememberResult{ID: recordID, OnNetwork: true, Flushed: &store.Flush{}}
	queued := vault.RememberResult{ID: recordID}

	for _, c := range []struct {
		name, file string
		elapsed    time.Duration
		columns    int
		outcome    writeOutcome
		warning    bool
	}{
		{name: "stored on Sia", file: "remember-stored.txt", elapsed: 24900 * time.Millisecond, columns: 80,
			outcome: writeOutcome{result: stored}},
		{name: "queued", file: "remember-queued.txt", elapsed: 600 * time.Millisecond, columns: 80,
			outcome: writeOutcome{result: queued}},
		{name: "the indexer did not answer", file: "remember-degraded.txt", elapsed: 600 * time.Millisecond, columns: 100,
			outcome: writeOutcome{result: vault.RememberResult{ID: id(t, "cdbfb0d3eefb5babd4779487b8aac9e7")}, degraded: true},
			warning: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := ended(t, c.columns, c.elapsed)
			if c.warning {
				s.out.warn(degraded(nil, "https://sia.storage"))
			}
			reportRemembered(s.out, c.outcome)
			s.assert(t, c.name, fixture(t, c.file))
		})
	}
}

func TestTheAdviceAWriteLeaves(t *testing.T) {
	for _, c := range []struct {
		name, file string
		id         string
		elapsed    time.Duration
		result     vault.RememberResult
	}{
		{name: "a near duplicate", file: "remember-near-duplicate.txt",
			id: "3bd23285606a44e6110cb83771d91ed1", elapsed: 500 * time.Millisecond,
			result: vault.RememberResult{
				ID: id(t, "3bd23285606a44e6110cb83771d91ed1"),
				Conflicts: []vault.Neighbour{{
					ID:         id(t, "fc680f96678d090b2e4aee3a82cfa677"),
					Statement:  "Prefers one live status line over scrolling logs",
					Similarity: 0.9835, Conflict: true,
				}},
			}},
		{name: "no tag narrows anything", file: "remember-broad-tags.txt",
			id: "6e8e20608d584ca9a619d542bc459480", elapsed: 500 * time.Millisecond,
			result: vault.RememberResult{
				ID: id(t, "6e8e20608d584ca9a619d542bc459480"),
				Tags: vault.TagAdvice{Records: 35, Tags: []vault.TagSpecificity{
					{Tag: "sennit", Records: 31, Share: 31.0 / 35.0, TooCommon: true},
				}},
			}},
		{name: "one tag is too common", file: "remember-common-tag.txt",
			id: "8761ec70b03978e892ea6a26ec715589", elapsed: 500 * time.Millisecond,
			result: vault.RememberResult{
				ID: id(t, "8761ec70b03978e892ea6a26ec715589"),
				Tags: vault.TagAdvice{Records: 36, Discriminating: 1, Tags: []vault.TagSpecificity{
					{Tag: "sennit", Records: 32, Share: 32.0 / 36.0, TooCommon: true},
				}},
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := ended(t, 80, c.elapsed)
			reportRemembered(s.out, writeOutcome{result: c.result})
			s.assert(t, c.name, fixture(t, c.file))
		})
	}
}

// A write whose upload failed still says where the memory is, and prints the id
// that reaches it.
func TestAFailedUploadStillNamesTheRecord(t *testing.T) {
	s := ended(t, 80, 12300*time.Millisecond)
	s.out.phase("Uploading to Sia")
	s.out.failed(onDevice)
	s.out.result("9c2e41d07a5b38f6e1d4c0b7a2958e13")
	report(t.Context(), s.out, refuse("upload batch of 1: finalize packed upload: "+
		"not enough hosts available to upload slab 0: 22 < 30"))
	s.assert(t, "a failed upload", fixture(t, "remember-upload-failed.txt"))
}

// What --verbose keeps of the block this command used to print always.
func TestTheWriteDetail(t *testing.T) {
	cid, err := seal.ParseCID("241502f96360bc8c0456658cecc050277afb80bd26bd131890e33deed5a90d77")
	if err != nil {
		t.Fatalf("parse cid: %v", err)
	}
	outcome := writeOutcome{queued: 3, result: vault.RememberResult{
		CID:      cid,
		EmbedFor: 269 * time.Millisecond, SealFor: time.Millisecond,
		Tags: vault.TagAdvice{Records: 1, Tags: []vault.TagSpecificity{
			{Tag: "cli", Records: 1}, {Tag: "ui", Records: 1},
		}},
	}}
	want := []string{
		"  cid       241502f96360bc8c0456658cecc050277afb80bd26bd131890e33deed5a90d77",
		"  tag       cli                  on 1 of 1 records",
		"  tag       ui                   on 1 of 1 records",
		"  embed     269 ms",
		"  seal      1 ms",
		"  on Sia    not yet, held on this device, 3 record(s) queued",
	}
	got := writeDetail(outcome)
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, got, want)
		}
	}
}

// Everything status reports, online and offline, with each caution it can add.
func TestWhatStatusSays(t *testing.T) {
	base := heldView{path: "~/.sennit", stored: 1281, queued: 3, indexed: 1284, model: "bge-small-en-v1.5-fp32"}
	online := base
	online.online, online.account, online.pinned = true, account(80*mib, 46*gib+583*mib+269228), 2
	online.repack = "not needed yet (0.2% of quota used)"

	offline := heldView{path: "~/.sennit", stored: 0, queued: 37, indexed: 37, model: "bge-small-en-v1.5-fp32"}

	for _, c := range []struct {
		name, file string
		view       heldView
		final      string
		elapsed    time.Duration
		notes      []ui.Message
	}{
		{name: "online", file: "status-online.txt", view: online,
			final: "Checked this vault and its Sia account", elapsed: 9800 * time.Millisecond},
		{name: "offline", file: "status-offline.txt", view: offline,
			final: "Checked this vault", elapsed: 300 * time.Millisecond},
		{name: "vectors from another model", file: "status-other-model.txt",
			view: heldView{path: "~/.sennit", stored: 1296, indexed: 1284, model: "bge-small-en-v1.5-fp32"},
			notes: []ui.Message{foreignVectors(vault.IndexHealth{
				Model: "bge-small-en-v1.5-fp32", Indexed: 1284, Foreign: map[string]int{"all-MiniLM-L6-v2": 12},
			})},
			final: "Checked this vault", elapsed: 300 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := ended(t, 80, c.elapsed)
			s.out.done(ui.MarkSuccess, c.final)
			s.out.report(statusRows(c.view))
			for _, note := range c.notes {
				s.out.note(note)
			}
			s.assert(t, c.name, fixture(t, c.file))
		})
	}
}

func TestStatusWarnsAboutStorageItCannotAccountFor(t *testing.T) {
	view := heldView{path: "~/.sennit", stored: 1281, indexed: 1281, model: "bge-small-en-v1.5-fp32",
		online: true, account: account(200*mib, 46*gib+583*mib+269228), pinned: 2,
		repack: "not needed yet (0.4% of quota used)"}
	note, ok := unknownSlabs([]reclaim.Unledgered{{Empty: true}, {Empty: true}, {Empty: false}})
	if !ok {
		t.Fatal("three slabs the ledger does not know were reported as nothing to say")
	}
	s := ended(t, 80, 10400*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Checked this vault and its Sia account")
	s.out.report(statusRows(view))
	s.out.note(note)
	s.assert(t, "unknown slabs", fixture(t, "status-unknown-slabs.txt"))
}

func TestStatusSaysWhenRepackIsWorthRunning(t *testing.T) {
	view := heldView{path: "~/.sennit", stored: 12480, indexed: 12480, model: "bge-small-en-v1.5-fp32",
		online: true, account: account(36*gib+532*mib+293601, 46*gib+583*mib+269228), pinned: 935,
		repack: "worth running, and there is room for it (78.4% of quota used)"}
	s := ended(t, 80, 9900*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Checked this vault and its Sia account")
	s.out.report(statusRows(view))
	s.out.note(ui.Message{
		Kind:        ui.KindWarning,
		Text:        "reclaimable storage has built up",
		Explanation: []string{"Left alone it keeps accumulating until a write fails for want of room."},
		Hints:       []string{"`sennit reclaim --repack` returns it"},
	})
	s.assert(t, "repack due", fixture(t, "status-repack-due.txt"))
}

// What a flush says in each of the states it can end in.
func TestWhatFlushSays(t *testing.T) {
	for _, c := range []struct {
		name, file string
		mark       ui.Mark
		final      string
		elapsed    time.Duration
	}{
		{"flushed", "flush.txt", ui.MarkSuccess, "Flushed 3 records to Sia", 22800 * time.Millisecond},
		{"nothing queued", "flush-nothing-queued.txt", ui.MarkSuccess,
			"Nothing queued: everything on this device is on Sia", 9100 * time.Millisecond},
		{"wrote nothing", "flush-wrote-nothing.txt", ui.MarkWarning, "The flush wrote nothing", 9400 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := ended(t, 80, c.elapsed)
			s.out.done(c.mark, c.final)
			s.assert(t, c.name, fixture(t, c.file))
		})
	}
}

// The report a reclaim leaves, in each shape it can take.
func TestWhatReclaimSays(t *testing.T) {
	before, after := account(80*mib, 46*gib), account(80*mib, 46*gib)
	swept := reclaim.Sweep{ObjectsSeen: 4, Before: before, After: after}

	t.Run("nothing to release", func(t *testing.T) {
		s := ended(t, 80, 21800*time.Millisecond)
		s.out.done(ui.MarkSuccess, "Nothing to release")
		s.out.report([][2]string{sweptRow(swept, false), quotaRow(before, after, 0)})
		s.assert(t, "nothing", fixture(t, "reclaim-nothing.txt"))
	})

	// Driven through the accounting rather than with the figures written out,
	// because the figures are what the accounting used to get wrong.
	t.Run("after a repack", func(t *testing.T) {
		s := ended(t, 80, 35700*time.Millisecond)
		packed := reclaim.Repack{
			Records: make([]reclaim.Moved, 60), SlabsBefore: 4, SlabsAfter: 1, Peak: 5,
			Before: account(200*mib, 46*gib), After: account(80*mib, 46*gib),
		}
		sweep := reclaim.Sweep{ObjectsSeen: 60, Before: packed.After, After: packed.After}
		quota := reclaimQuota(packed, sweep, nil)
		s.out.done(ui.MarkSuccess, "Released "+humanBytes(quota.freed))
		s.out.report([][2]string{
			repackRow(packed),
			sweptRow(sweep, true),
			quotaRow(quota.before, quota.after, quota.freed),
		})
		s.assert(t, "repack", fixture(t, "reclaim-repack.txt"))
	})

	t.Run("every optional release at once", func(t *testing.T) {
		s := ended(t, 80, 31200*time.Millisecond)
		sweep := reclaim.Sweep{
			ObjectsSeen: 12, ObjectsDeleted: 4, SlabsReleased: 1,
			Before: account(240*mib, 46*gib), After: account(160*mib, 46*gib),
		}
		released := reclaim.Sweep{SlabsReleased: 2, Before: sweep.After, After: account(120*mib, 46*gib)}
		quota := reclaimQuota(reclaim.Repack{}, sweep, &released)
		s.out.done(ui.MarkSuccess, "Released "+humanBytes(quota.freed))
		s.out.report([][2]string{
			ownedRow(3),
			sweptRow(sweep, false),
			droppedRow(1),
			orphansRow(2, 1, released.SlabsReleased),
			quotaRow(quota.before, quota.after, quota.freed),
		})
		s.assert(t, "everything", fixture(t, "reclaim-everything.txt"))
	})

	t.Run("what it did not touch", func(t *testing.T) {
		s := ended(t, 80, 22100*time.Millisecond)
		s.out.done(ui.MarkSuccess, "Nothing to release")
		s.out.report([][2]string{sweptRow(swept, false), heldRow(1), quotaRow(before, after, 0)})
		s.out.note(ui.Message{Kind: ui.KindHint, Text: "2 objects cannot be opened; `--unreadable` removes them"})
		s.out.note(ui.Message{Kind: ui.KindHint, Text: "3 slabs billed to this account are not in this device's " +
			"ledger and were not swept; `--orphans` releases the 2 that hold nothing"})
		s.assert(t, "hints", fixture(t, "reclaim-hints.txt"))
	})
}

// What a reclaim reports having freed covers the repack as well as the sweep.
//
// The repack runs first and releases as it goes, so by the time the sweep reads
// the account for itself the repack's release has already happened and sits
// outside anything measured from there. Reclaiming space is the only operation
// here that costs money to get wrong, and a figure that understates it invites
// running the whole thing again.
func TestTheQuotaAReclaimReportsCoversEveryStageThatRan(t *testing.T) {
	var (
		start    = account(240*mib, 46*gib)
		repacked = account(120*mib, 46*gib)
		swept    = account(80*mib, 46*gib)
		orphaned = account(40*mib, 46*gib)
	)
	moved := make([]reclaim.Moved, 60)

	for _, c := range []struct {
		name          string
		packed        reclaim.Repack
		sweep         reclaim.Sweep
		orphans       *reclaim.Sweep
		before, after sia.Account
		freed         uint64
	}{
		{
			name:   "a repack frees and the sweep finds nothing left",
			packed: reclaim.Repack{Records: moved, Before: start, After: repacked},
			sweep:  reclaim.Sweep{Before: repacked, After: repacked},
			before: start, after: repacked, freed: 120 * mib,
		},
		{
			name:   "both of them free something",
			packed: reclaim.Repack{Records: moved, Before: start, After: repacked},
			sweep:  reclaim.Sweep{Before: repacked, After: swept},
			before: start, after: swept, freed: 160 * mib,
		},
		{
			name:    "a repack, a sweep and an orphan release",
			packed:  reclaim.Repack{Records: moved, Before: start, After: repacked},
			sweep:   reclaim.Sweep{Before: repacked, After: swept},
			orphans: &reclaim.Sweep{Before: swept, After: orphaned},
			before:  start, after: orphaned, freed: 200 * mib,
		},
		{
			name:   "a repack with nothing to move took no reading of its own",
			packed: reclaim.Repack{},
			sweep:  reclaim.Sweep{Before: start, After: swept},
			before: start, after: swept, freed: 160 * mib,
		},
		{
			name:   "no repack at all reports exactly the sweep",
			sweep:  reclaim.Sweep{Before: start, After: swept},
			before: start, after: swept, freed: 160 * mib,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			quota := reclaimQuota(c.packed, c.sweep, c.orphans)
			if quota.freed != c.freed {
				t.Errorf("freed %s, want %s", humanBytes(quota.freed), humanBytes(c.freed))
			}
			if quota.before != c.before || quota.after != c.after {
				t.Errorf("window %s to %s, want %s to %s",
					humanBytes(quota.before.PinnedData), humanBytes(quota.after.PinnedData),
					humanBytes(c.before.PinnedData), humanBytes(c.after.PinnedData))
			}
		})
	}
}

func TestWhatRecoverSays(t *testing.T) {
	s := ended(t, 80, 56500*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Recovered 1,008 records from 14 objects")
	s.out.report([][2]string{{"Skipped", "12 frames this phrase does not open"}})
	s.out.note(ui.Message{Kind: ui.KindWarning, Text: "1 object stopped parsing part way, 2 could not be opened at all"})
	s.assert(t, "recover", fixture(t, "recover.txt"))
}

func TestWhatHydrateSays(t *testing.T) {
	report := vault.HydrateReport{
		Records: 1008, Objects: 14, Bytes: 2013265, Bodies: 1008, Sessions: 4, Slabs: 14,
		Rebuild: vault.RebuildReport{Origins: map[string]vault.FieldOrigin{}},
	}
	for _, field := range []string{"version", "kind", "title", "created", "updated"} {
		report.Rebuild.Origins[field] = vault.OriginSynthesised
	}
	for _, field := range []string{"summary", "tags", "project", "archived", "agent", "agentRef", "models",
		"counts.tokens", "counts.durationMs", "lineage", "preservedTail"} {
		report.Rebuild.Origins[field] = vault.OriginLost
	}

	s := ended(t, 80, 25100*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Hydrated 1,008 records to depth metadata")
	s.out.report(hydrateRows(report, "https://sia.storage"))
	note, ok := rebuiltHeads(report)
	if !ok {
		t.Fatal("four rebuilt conversations were reported as nothing to say")
	}
	s.out.note(note)
	hint, _ := deeperHint(vault.HydrateMetadata)
	s.out.note(hint)
	s.assert(t, "hydrate", fixture(t, "hydrate-metadata.txt"))

	shallow := vault.HydrateReport{Records: 1008, Objects: 14, Bytes: 2013265, Slabs: 14,
		Rebuild: vault.RebuildReport{Gaps: 1}}
	s = ended(t, 80, 18400*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Hydrated 1,008 records to depth catalog")
	s.out.report(hydrateRows(shallow, "https://sia.storage"))
	s.out.note(ui.Message{Kind: ui.KindWarning, Text: "1 conversation is missing part of the transcript in the middle"})
	hint, _ = deeperHint(vault.HydrateCatalog)
	s.out.note(hint)
	s.assert(t, "hydrate at catalog depth", fixture(t, "hydrate-catalog.txt"))
}

func TestWhatInitSays(t *testing.T) {
	t.Run("a new phrase", func(t *testing.T) {
		s := ended(t, 80, 0)
		s.out.result("<word1> <word2> <word3> ... <word12>")
		s.out.note(ui.Message{
			Kind:        ui.KindWarning,
			Text:        "this phrase is the vault: anyone holding it can read every memory, and losing it loses the data",
			Explanation: []string{"It is not stored anywhere."},
		})
		s.assert(t, "new phrase", fixture(t, "init-new-phrase.txt"))
	})

	t.Run("online", func(t *testing.T) {
		s := ended(t, 80, 9400*time.Millisecond)
		s.out.done(ui.MarkSuccess, "Vault ready")
		s.out.report(preparedRows("/home/you/.sennit", "https://sia.storage", account(40*mib, 46*gib+583*mib+269228)))
		s.assert(t, "init online", fixture(t, "init-online.txt"))
	})

	t.Run("offline", func(t *testing.T) {
		s := ended(t, 80, 300*time.Millisecond)
		s.out.done(ui.MarkSuccess, "Vault ready on this device")
		s.out.report(preparedRows("/home/you/.sennit", "", sia.Account{}))
		s.assert(t, "init offline", fixture(t, "init-offline.txt"))
	})
}

func TestWhatConnectSays(t *testing.T) {
	s := ended(t, 80, 102*time.Second)
	s.out.done(ui.MarkSuccess, "Connected: app key 3f9a1c2b… written to sennit.key")
	s.out.above(append([]string{""}, ui.KeyValues(
		connectRows(1, 72*time.Second, 16*time.Second, account(40*mib, 46*gib+583*mib+269228)))...)...)
	s.out.note(ui.Message{Kind: ui.KindHint,
		Text: "load the key into this shell without putting it in your history: `export SENNIT_APP_KEY=$(cat sennit.key)`"})
	s.assert(t, "connect", fixture(t, "connect.txt"))
}

// The vault path a reader recognises.
func TestTheVaultPathIsShownFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, c := range []struct{ path, want string }{
		{home + "/.sennit", "~/.sennit"},
		{home, "~"},
		{"/var/tmp/other", "/var/tmp/other"},
	} {
		if got := vaultPath(c.path); got != c.want {
			t.Errorf("vaultPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// A conversation among the results reads differently from a memory, and the
// difference is what a caller does with it.
func TestAConversationIsShownAsOne(t *testing.T) {
	session := &record.Session{
		ID: id(t, "9c41e07b2d5a4f86a1e3b0c7d8f25e61"), Title: "Deciding the flush cadence",
		Summary: "Worked out why the one-hour cap exists.", Tags: []string{"storage", "flush"},
		Counts: record.Counts{Messages: 4}, Chunks: []record.ChunkRef{{}},
	}
	row := viewHit(recall.Hit{Found: recall.Found{Session: session}, Similarity: 0.4811}, false)
	if row.typ != "session" || row.holds != "4 messages in 1 chunk" || row.text != session.Title {
		t.Errorf("a conversation renders as %+v", row)
	}
}
