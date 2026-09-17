package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
	"github.com/steven3002/sennit/vault"
)

// runFlush writes whatever is queued to Sia.
//
// A record is durable on this device the moment it is remembered and on the
// network only after a flush. The standing cadence closes that gap on its own;
// this is for closing it now.
func runFlush(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("flush").withVault().withVerbose()
	if err := cmd.parse(out, args); err != nil {
		return err
	}

	v, err := cmd.vault.open(ctx, out, openPhases)
	if err != nil {
		return err
	}
	defer closing(out, v)

	pending := v.Pending()
	if pending == 0 {
		out.done(ui.MarkSuccess, "Nothing queued: everything on this device is on Sia")
		return nil
	}
	if !v.Online() {
		return needsIndexer("flush", v, queuedStay(pending))
	}
	out.watch(flushPhases(out, pending))
	out.leaves(queuedLeaves(pending)...)

	flushed, err := v.Flush(ctx)
	if err != nil {
		return err
	}
	if flushed == nil {
		out.done(ui.MarkWarning, "The flush wrote nothing")
		return nil
	}
	out.done(ui.MarkSuccess, "Flushed "+plural(len(flushed.Written), "record")+" to Sia")
	detail := []string{
		fmt.Sprintf("  wrote     %s in %d object(s) over %d slab(s)",
			humanBytes(uint64(flushed.Bytes())), len(flushed.Written), len(flushed.Slabs)),
		fmt.Sprintf("  upload    %s · pin slabs %s · pin objects %s",
			took(flushed.UploadFor), took(flushed.PinSlabsFor), took(flushed.PinObjectFor)),
	}
	if n := len(flushed.Written); n > 0 {
		detail = append(detail, fmt.Sprintf("  per pin   %s across %d object(s)",
			took(flushed.PinObjectFor/time.Duration(n)), n))
	}
	out.detail(detail...)
	return nil
}

// queuedLeaves is what an interrupted flush leaves behind, in the two sentences
// it is said in.
//
// They are built together because they have to agree with each other about how
// many records there are, and a fixed second sentence beside a counted first one
// is exactly how they came to disagree.
func queuedLeaves(records int) []string {
	return []string{queuedStay(records), pick(records, "It has", "They have") + " not reached Sia yet."}
}

// queuedStay says what is still owed to the network, which is the one thing a
// reader must not have to work out for themselves.
func queuedStay(records int) string {
	if records == 1 {
		return "The record stays queued on this device."
	}
	return "The " + plural(records, "record") + " stay queued on this device."
}

// runStatus reports what the vault holds, what it owes the network, and what
// it is being billed for.
func runStatus(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("status").withVault().withVerbose()
	if err := cmd.parse(out, args); err != nil {
		return err
	}

	v, err := cmd.vault.open(ctx, out, openPhases)
	if err != nil {
		return err
	}
	defer closing(out, v)

	entries := v.Entries()
	health := v.IndexHealth()
	held := heldView{
		path:    vaultPath(cmd.vault.home),
		stored:  len(entries),
		queued:  v.Pending(),
		indexed: health.Indexed,
		model:   health.Model,
	}
	var notes []ui.Message
	// A mixed index is reported rather than passed over. Vectors from two
	// models cannot be compared, so the ones from the other model are simply
	// not searched, which looks exactly like the vault having become worse at
	// recall unless it is said out loud.
	if health.Mixed() {
		notes = append(notes, foreignVectors(health))
	}

	// The detail costs a few reads of the device's own accounting, so it is
	// gathered only when it will be shown.
	var detail []string
	if out.verbose {
		detail = statusDetail(v, len(entries), health)
	}
	if !v.Online() {
		out.done(ui.MarkSuccess, "Checked this vault")
		out.report(statusRows(held))
		out.detail(detail...)
		for _, note := range notes {
			out.note(note)
		}
		return nil
	}

	out.phase(accountPhase)
	account, err := v.Account(ctx)
	if err != nil {
		return err
	}
	slabs, err := v.TrackedSlabs()
	if err != nil {
		return err
	}
	mark, err := v.Watermark(ctx)
	if err != nil {
		return err
	}
	for _, slab := range slabs {
		if !slab.Releasable() {
			held.hydrated++
		}
	}
	held.online, held.account, held.pinned = true, account, len(slabs)-held.hydrated
	held.repack = fmt.Sprintf("%s (%.1f%% of quota used)", repackAdvice(mark.Due, mark.Affordable), 100*mark.Used)
	if mark.Due {
		notes = append(notes, ui.Message{
			Kind:        ui.KindWarning,
			Text:        "reclaimable storage has built up",
			Explanation: []string{"Left alone it keeps accumulating until a write fails for want of room."},
			Hints:       []string{"`sennit reclaim --repack` returns it"},
		})
	}

	// Storage the account pays for that this device has no record of. It is
	// reported here because it is otherwise invisible: an ordinary sweep is
	// bounded by the ledger and cannot see past it, so quota that will not come
	// back looks like quota that was never freed.
	unledgered, err := v.Unledgered(ctx)
	if err != nil {
		return err
	}
	if note, ok := unknownSlabs(unledgered); ok {
		notes = append(notes, note)
	}

	out.done(ui.MarkSuccess, "Checked this vault and its Sia account")
	out.report(statusRows(held))
	out.detail(detail...)
	for _, note := range notes {
		out.note(note)
	}
	return nil
}

// A heldView is what status says the vault holds, in the terms it prints.
type heldView struct {
	path             string
	stored, queued   int
	indexed          int
	model            string
	online           bool
	account          sia.Account
	pinned, hydrated int
	repack           string
}

// statusRows is the report: what is here, what is owed, and what is billed.
func statusRows(v heldView) [][2]string {
	rows := [][2]string{
		{"Vault", v.path},
		{"Records", fmt.Sprintf("%s stored on Sia, %s queued on this device", ui.Commas(v.stored), ui.Commas(v.queued))},
		{"Search", fmt.Sprintf("%s indexed with %s", ui.Commas(v.indexed), v.model)},
	}
	if !v.online {
		return append(rows, [2]string{"Sia", "offline: queued records stay on this device until a connected run"})
	}
	return append(rows,
		[2]string{"Quota", fmt.Sprintf("%s of %s used, %s free",
			humanBytes(v.account.PinnedData), humanBytes(v.account.MaxPinnedData), humanBytes(v.account.Free()))},
		[2]string{"Slabs", fmt.Sprintf("%d pinned by this device, %d hydrated from another", v.pinned, v.hydrated)},
		[2]string{"Repack", v.repack})
}

func foreignVectors(health vault.IndexHealth) ui.Message {
	note := ui.Message{
		Kind: ui.KindWarning,
		Text: fmt.Sprintf("%s %s from another model and %s not searchable",
			plural(health.Stale(), "vector"), verb(health.Stale(), "is", "are"), verb(health.Stale(), "is", "are")),
		Hints: []string{"re-embed those records to make them findable again"},
	}
	labels := 0
	for model := range health.Foreign {
		labels = max(labels, width.String(model))
	}
	for model, count := range health.Foreign {
		note.Data = append(note.Data, fmt.Sprintf("%s    %d", width.Pad(model, labels), count))
	}
	return note
}

// unknownSlabs is the caution about storage this account pays for that this
// device cannot account for: some of it is another installation's, and the rest
// is this device's own interrupted writes.
func unknownSlabs(unledgered []reclaim.Unledgered) (ui.Message, bool) {
	if len(unledgered) == 0 {
		return ui.Message{}, false
	}
	var empty int
	for _, slab := range unledgered {
		if slab.Empty {
			empty++
		}
	}
	note := ui.Message{
		Kind: ui.KindWarning,
		Text: fmt.Sprintf("%s %s billed to this account and not in this device's ledger",
			plural(len(unledgered), "slab"), verb(len(unledgered), "is", "are")),
	}
	if empty > 0 {
		note.Data = append(note.Data, fmt.Sprintf("%d %s nothing, an interrupted write leaves exactly this",
			empty, verb(empty, "holds", "hold")))
		note.Hints = append(note.Hints, fmt.Sprintf("`sennit reclaim --orphans` releases the %d that %s nothing",
			empty, verb(empty, "holds", "hold")))
	}
	if held := len(unledgered) - empty; held > 0 {
		note.Data = append(note.Data, fmt.Sprintf("%d %s records and %s to another installation of this vault",
			held, verb(held, "holds", "hold"), verb(held, "belongs", "belong")))
		note.Hints = append(note.Hints, fmt.Sprintf("reclaim the %s from the device that wrote %s",
			pick(held, "other", "others"), pick(held, "it", "them")))
	}
	return note, true
}

// statusDetail is the internal accounting --verbose keeps: what the catalog,
// the index and the location cache cost on this device, and how reads were
// served.
func statusDetail(v *vault.Vault, records int, health vault.IndexHealth) []string {
	stats := v.ManifestStats()
	vectors := v.VectorStats()
	lines := []string{
		fmt.Sprintf("records   %d catalogued, %d queued for Sia", records, v.Pending()),
		fmt.Sprintf("catalog   %s snapshot + %s log, %d compaction(s), %s written",
			humanBytes(uint64(stats.SnapshotBytes)), humanBytes(uint64(stats.LogBytes)),
			stats.Compactions, humanBytes(uint64(stats.Written))),
		fmt.Sprintf("index     %d vector(s) of %s, %s base + %s delta, %d compaction(s)",
			health.Indexed, health.Model,
			humanBytes(uint64(vectors.BaseBytes)), humanBytes(uint64(vectors.DeltaBytes)), vectors.Compactions),
	}
	if cache, err := v.CacheSize(); err == nil && cache.Objects > 0 {
		lines = append(lines, fmt.Sprintf("locations %s for %d object(s) over %d slab(s), %.0f B/object",
			humanBytes(uint64(cache.Total())), cache.Objects, cache.Slabs, cache.PerObject()))
	}
	if tiers, err := v.ReadStats(); err == nil && len(tiers) > 0 {
		lines = append(lines, "reads")
		for _, tier := range tiers {
			lines = append(lines, fmt.Sprintf("  %-8s %6d served, mean %s, %d miss(es)",
				tier.Tier, tier.Reads, took(tier.Mean()), tier.Misses))
		}
	}
	return lines
}

func repackAdvice(due, affordable bool) string {
	switch {
	case due && affordable:
		return "worth running, and there is room for it"
	case due:
		return "worth running, but there is no longer room for the slabs it would hold at once"
	default:
		return "not needed yet"
	}
}

// vaultPath is the vault's directory as a person recognises it. A home-relative
// path is shorter and is what the documentation shows; on Windows the full path
// is the one a reader can act on.
func vaultPath(home string) string {
	if runtime.GOOS == "windows" {
		return home
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		return home
	}
	if home == dir {
		return "~"
	}
	if rest, ok := strings.CutPrefix(home, dir+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return home
}

// runReclaim releases storage nothing points at any more.
func runReclaim(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("reclaim").withVault().withVerbose()
	repack, orphans, unreadable, takeOwnership, releaseAll := reclaimFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}

	v, err := cmd.vault.open(ctx, out, reclaimPhases(out))
	if err != nil {
		return err
	}
	defer closing(out, v)
	if !v.Online() {
		return needsIndexer("reclaim", v, "")
	}
	// The same refusal the vault makes, made before any phase starts so that
	// it reads as a precondition rather than as a step that failed.
	if pending := v.Pending(); pending > 0 {
		return refuse(fmt.Sprintf("%s %s queued and not yet on the network",
			plural(pending, "record"), verb(pending, "is", "are"))).
			try("flush before reclaiming: `sennit flush`")
	}
	out.leaves("It stopped part way. Run `sennit reclaim` again to finish.")

	opts := vault.ReclaimOptions{ReleaseAll: *releaseAll, TakeOwnership: *takeOwnership}
	var rows [][2]string
	var detail []string
	if *takeOwnership {
		taken, err := v.TakeOwnership()
		if err != nil {
			return err
		}
		rows = append(rows, ownedRow(taken))
	}
	var packed reclaim.Repack
	if *repack {
		if packed, err = repackNow(ctx, v); err != nil {
			return err
		}
		rows = append(rows, repackRow(packed))
		if len(packed.Records) > 0 {
			detail = append(detail,
				fmt.Sprintf("repack    %d record(s), %d slab(s) into %d, peak %d, in %s",
					len(packed.Records), packed.SlabsBefore, packed.SlabsAfter, packed.Peak, took(packed.Elapsed)),
				fmt.Sprintf("          read %s · write %s · retire %s",
					took(packed.ReadFor), took(packed.WriteFor), took(packed.RetireFor)))
		}
	}

	sweep, err := v.Reclaim(ctx, opts)
	if err != nil {
		return err
	}
	rows = append(rows, sweptRow(sweep, len(packed.Records) > 0))
	detail = append(detail, fmt.Sprintf("swept     %d object(s), deleted %d, released %d slab(s) in %s",
		sweep.ObjectsSeen, sweep.ObjectsDeleted, sweep.SlabsReleased, took(sweep.Elapsed)))
	if sweep.SlabsHeld > 0 {
		rows = append(rows, heldRow(sweep.SlabsHeld))
	}

	var notes []ui.Message
	if sweep.Unreadable > 0 && !*unreadable {
		notes = append(notes, ui.Message{Kind: ui.KindHint, Text: fmt.Sprintf(
			"%s cannot be opened; `--unreadable` removes %s",
			plural(sweep.Unreadable, "object"), pick(sweep.Unreadable, "it", "them"))})
	}
	// A sweep that reported only what it released would say nothing about the
	// storage it cannot reach, which is exactly the storage a user is looking
	// for when a reclaim returns less than expected.
	if !*orphans {
		if note, ok := unsweptSlabs(ctx, v); ok {
			notes = append(notes, note)
		}
	}
	if *unreadable {
		dropped, err := v.DropUnreadable(ctx)
		if err != nil {
			return err
		}
		rows = append(rows, droppedRow(len(dropped)))
	}

	var orphaned *reclaim.Sweep
	if *orphans {
		found, err := v.Orphans(ctx, opts)
		if err != nil {
			return err
		}
		var stranded int
		for _, orphan := range found {
			if !orphan.Tracked {
				stranded++
			}
		}
		released, err := v.ReleaseOrphans(ctx, opts)
		if err != nil {
			return err
		}
		rows = append(rows, orphansRow(len(found), stranded, released.SlabsReleased))
		detail = append(detail, fmt.Sprintf("          released %d slab(s) in %s", released.SlabsReleased, took(released.Elapsed)))
		orphaned = &released
	}

	quota := reclaimQuota(packed, sweep, orphaned)
	rows = append(rows, quotaRow(quota.before, quota.after, quota.freed))

	final := "Nothing to release"
	if quota.freed > 0 {
		final = "Released " + humanBytes(quota.freed)
	}
	out.done(ui.MarkSuccess, final)
	out.report(rows)
	out.detail(detail...)
	for _, note := range notes {
		out.note(note)
	}
	return nil
}

// A quotaWindow is what a whole reclaim returned, and the span it is measured
// over.
//
// Every stage reads the account either side of itself, so the readings chain:
// a repack's after-reading is what the sweep behind it sees before it starts.
// Reporting only the sweep's end of that chain, which is what this command used
// to do, leaves everything the repack released outside the window, and on a run
// whose whole purpose was the repack that is all of it.
//
// The total is accumulated from what each stage returned rather than taken as
// the difference between the ends of the window, so a record another process
// wrote part way through is not reported as space this command gave back.
type quotaWindow struct {
	before, after sia.Account
	freed         uint64
	// measured records whether any stage has reported yet, so the first one to
	// do so sets where the window opens.
	measured bool
}

// add folds one stage's readings in. Stages are added in the order they ran,
// and a stage that did nothing and took no reading is not added at all.
func (w *quotaWindow) add(before, after sia.Account, freed uint64) {
	if !w.measured {
		w.before, w.measured = before, true
	}
	w.after = after
	w.freed += freed
}

// reclaimQuota adds up what one run of reclaim returned, in the order its
// stages ran.
//
// It is a function of the stage reports rather than a few lines inside
// runReclaim because the order is the whole content of the answer, and nothing
// about that order is observable from a report only a live indexer can produce.
// A repack with nothing to move never read the account and so contributes
// neither a reading nor a release; orphans is nil unless the run asked for it.
func reclaimQuota(packed reclaim.Repack, sweep reclaim.Sweep, orphans *reclaim.Sweep) quotaWindow {
	var window quotaWindow
	if len(packed.Records) > 0 {
		window.add(packed.Before, packed.After, packed.Freed())
	}
	window.add(sweep.Before, sweep.After, sweep.Freed())
	if orphans != nil {
		window.add(orphans.Before, orphans.After, orphans.Freed())
	}
	return window
}

// The lines of a reclaim's report. Each says what was released and what was
// left alone, because quota that did not come back is the thing a reader is
// looking for when a reclaim returns less than they expected.
func ownedRow(slabs int) [2]string {
	return [2]string{"Owned", fmt.Sprintf("%s hydrated from another installation %s now this device's to release",
		plural(slabs, "slab"), verb(slabs, "is", "are"))}
}

func repackRow(packed reclaim.Repack) [2]string {
	if len(packed.Records) == 0 {
		return [2]string{"Repack", "nothing to move"}
	}
	return [2]string{"Repack", fmt.Sprintf("%s, %s into %d, peak %d, freed %s",
		plural(len(packed.Records), "record"), plural(packed.SlabsBefore, "slab"),
		packed.SlabsAfter, packed.Peak, humanBytes(packed.Freed()))}
}

// sweptRow names the sweep's own share of the quota when a repack ran first,
// because the two releases are otherwise one figure on the quota row and a
// reader cannot tell which operation returned what. On a run with no repack
// there is nothing to tell apart, and the row is the one it has always been.
func sweptRow(sweep reclaim.Sweep, afterRepack bool) [2]string {
	swept := fmt.Sprintf("%s, deleted %d, released %s",
		plural(sweep.ObjectsSeen, "object"), sweep.ObjectsDeleted, plural(sweep.SlabsReleased, "slab"))
	if afterRepack {
		swept += ", freed " + humanBytes(sweep.Freed())
	}
	return [2]string{"Swept", swept}
}

func heldRow(slabs int) [2]string {
	return [2]string{"Held", fmt.Sprintf("%s left alone: another device pinned %s and this one hydrated %s",
		plural(slabs, "slab"), pick(slabs, "it", "them"), pick(slabs, "it", "them"))}
}

func droppedRow(objects int) [2]string {
	return [2]string{"Dropped", plural(objects, "object") + " the indexer could not open"}
}

// orphansRow takes its verb from the count, and counts the slabs this device's
// ledger does not name rather than saying "of them", which reads as a plural of
// one when a single slab was found. The hint this row follows is built the same
// way.
func orphansRow(found, stranded, released int) [2]string {
	return [2]string{"Orphans", fmt.Sprintf("%s %s nothing, %d not in this device's ledger; released %d",
		plural(found, "slab"), verb(found, "holds", "hold"), stranded, released)}
}

func quotaRow(before, after sia.Account, freed uint64) [2]string {
	return [2]string{"Quota", fmt.Sprintf("%s used before, %s after, freed %s",
		humanBytes(before.PinnedData), humanBytes(after.PinnedData), humanBytes(freed))}
}

// unsweptSlabs is the hint about storage a ledger-bounded sweep cannot reach.
func unsweptSlabs(ctx context.Context, v *vault.Vault) (ui.Message, bool) {
	unledgered, err := v.Unledgered(ctx)
	if err != nil || len(unledgered) == 0 {
		return ui.Message{}, false
	}
	var empty int
	for _, slab := range unledgered {
		if slab.Empty {
			empty++
		}
	}
	return ui.Message{Kind: ui.KindHint, Text: fmt.Sprintf(
		"%s billed to this account %s not in this device's ledger and were not swept; "+
			"`--orphans` releases the %d that %s nothing",
		plural(len(unledgered), "slab"), verb(len(unledgered), "is", "are"), empty, verb(empty, "holds", "hold"))}, true
}

// repackNow rewrites the live records into as few slabs as they fit in.
func repackNow(ctx context.Context, v *vault.Vault) (reclaim.Repack, error) {
	mark, err := v.Watermark(ctx)
	if err != nil {
		return reclaim.Repack{}, err
	}
	if !mark.Affordable {
		return reclaim.Repack{}, refuse(fmt.Sprintf(
			"repack needs %s free to hold the old and new slabs at once, and there is less than that",
			humanBytes(mark.Headroom)))
	}
	return v.Repack(ctx)
}

// runRecover rebuilds the vault from the recovery phrase and the indexer.
func runRecover(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("recover").withVault().withVerbose()
	embed := recoverFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}

	v, err := cmd.vault.open(ctx, out, restorePhases(out, "Recovering records", "recovered", false))
	if err != nil {
		return err
	}
	defer closing(out, v)
	if !v.Online() {
		return needsIndexer("recover", v, "")
	}
	// Each record is written whole before the next is read, so what was
	// restored stays. Whether running it again is safe has not been
	// established, so nothing here suggests it.
	out.leaves("Records recovered before the interrupt stay on this device.")

	report, err := v.Recover(ctx, vault.RecoveryRequest{Embed: *embed})
	if err != nil {
		return err
	}
	out.done(ui.MarkSuccess, fmt.Sprintf("Recovered %s from %s",
		plural(report.Recovered, "record"), plural(report.Objects, "object")))
	if report.Foreign > 0 {
		out.report([][2]string{{"Skipped", plural(report.Foreign, "frame") + " this phrase does not open"}})
	}
	if report.Damaged > 0 || report.Unreadable > 0 {
		out.note(ui.Message{Kind: ui.KindWarning, Text: fmt.Sprintf(
			"%s stopped parsing part way, %d could not be opened at all",
			plural(report.Damaged, "object"), report.Unreadable)})
	}
	return nil
}

// reclaimFlags is what sennit reclaim takes. Each of them releases something
// the plain command deliberately leaves alone.
func reclaimFlags(cmd *invocation) (repack, orphans, unreadable, takeOwnership, releaseAll *bool) {
	return cmd.set.Bool("repack", false,
			"rewrite every live record into as few slabs as it fits in before releasing the rest"),
		cmd.set.Bool("orphans", false,
			"also release slabs the account is billed for that hold nothing, including any stranded by an installation that is gone"),
		cmd.set.Bool("unreadable", false,
			"also delete objects the indexer holds but cannot open"),
		cmd.set.Bool("take-ownership", false,
			"release storage this device hydrated rather than pinned; only when the installation that wrote it is gone for good, never when it is merely switched off"),
		cmd.set.Bool("release-all", false,
			"release this vault's storage even though the catalog is empty; only for a vault that really has been emptied, never to work around a catalog that will not load")
}

// recoverFlags is what sennit recover takes.
func recoverFlags(cmd *invocation) *bool {
	return cmd.set.Bool("embed", true,
		"regenerate search vectors as records are recovered, so they are findable by meaning and not only by id")
}

// verb picks the form of a verb that agrees with a count.
func verb(n int, singular, plural string) string { return pick(n, singular, plural) }

func pick(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
