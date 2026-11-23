package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/vault"
)

// vaultFlags are the options every command shares.
type vaultFlags struct {
	home    string
	indexer string
	offline bool
	// phraseStdin is --phrase-stdin: the phrase is to be read from stdin, which
	// is otherwise left alone.
	phraseStdin bool
	// set is the command's flags, kept to tell an indexer this run named from
	// the default one.
	set *flag.FlagSet
}

// checkFlagOrder rejects a flag written after the positional arguments.
//
// Go's flag package stops parsing at the first non-flag word, so
// `remember "..." -offline` leaves -offline as a second piece of the statement
// and the command goes on to fail for an unrelated reason, asking for an app
// key the user had just said they did not want to use. Silently taking a flag
// as prose is worse than refusing it: the run appears to be about something
// else entirely.
func checkFlagOrder(command string, rest []string) error {
	for _, arg := range rest {
		if len(arg) > 1 && strings.HasPrefix(arg, "-") {
			return refuse(fmt.Sprintf(
				"%q looks like a flag but comes after the text, where it is read as part of it", arg)).
				try(fmt.Sprintf("flags go first: `sennit %s %s \"<text>\"`", command, flagAsShown(command, arg)))
		}
	}
	return nil
}

// flagAsShown names a flag the way the help does, so the hint shows the form to
// type rather than echoing what was typed.
func flagAsShown(command, typed string) string {
	name, _, _ := strings.Cut(strings.TrimLeft(typed, "-"), "=")
	for _, group := range [][]flagHelp{commandHelps[command].flags, vaultFlagHelp, {colorFlagHelp, verboseFlagHelp}} {
		for _, f := range group {
			if f.flag == name {
				return f.shown
			}
		}
	}
	return typed
}

func (f *vaultFlags) bind(fs *flag.FlagSet) {
	f.set = fs
	fs.StringVar(&f.home, "home", vault.DefaultHome(), "vault directory")
	fs.StringVar(&f.indexer, "indexer", vault.DefaultIndexer(), "indexer URL")
	fs.BoolVar(&f.offline, "offline", false, "work without contacting the indexer; a missing embedding model is still downloaded")
	bindPhraseStdin(fs, &f.phraseStdin)
}

// open prepares a vault from the flags and the environment.
//
// Neither secret is ever the value of a flag. A recovery phrase or an app key
// passed as an argument is visible in the process table and lands in shell
// history, which would make every other precaution in the design decorative.
// Each comes from the environment, or from the profile sennit connect stored,
// and the phrase can also be typed when asked for, or piped in on stdin when
// --phrase-stdin says to read it there.
func (f *vaultFlags) open(ctx context.Context, out *session, phases phaseNamer) (*vault.Vault, error) {
	phrase, err := out.readPhrase(f.phraseStdin)
	if err != nil {
		return nil, err
	}
	var appKey keys.AppKey
	if !f.offline {
		// Before the options are built, because a stored key brings the indexer
		// that issued it.
		if appKey, err = f.appKey(out); err != nil {
			return nil, err
		}
	}
	opts := vault.Options{
		Home:       f.home,
		Phrase:     phrase,
		Indexer:    f.indexer,
		AppKey:     appKey,
		Offline:    f.offline,
		OnProgress: out.watch(out.rebuilding(phases)),
	}
	v, err := vault.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	// A search index found damaged was rebuilt as the vault opened, and nothing
	// later in the run would say so. Damage in it is never passed over in
	// silence, so it is printed now, whatever the command.
	if repair := v.IndexRepair(); repair.Ran {
		out.warn(rebuiltIndex(repair))
	}
	// A change a crash cut off was dropped as the catalog was read, and nothing
	// later in the run would say so. It is what explains a record that seems to
	// have lost its latest change, so it is printed now, whatever the command.
	if dropped := v.DroppedCatalogChanges(); dropped > 0 {
		out.warn(cutCatalog(dropped))
	}
	// A listing the vault could not date by its records as it opened stays as
	// it was until an open manages it, and every open tries. Nothing later in
	// the run says so, and it is what explains a listing still dated by a
	// restore, so it is printed now, whatever the command.
	if reason := v.ListingNotDatedBecause(); reason != nil {
		out.warn(undatedListing(reason))
	}
	// A folder others can read was already there, and is left as it was set,
	// so it is said at every open until somebody narrows it.
	if who := v.FolderOpenTo(); who != "" {
		out.warn(folderOpenTo(f.home, who, out.probe.GOOS))
	}
	// A vault that wanted the network and did not get it still works, and the
	// user has to be told which of the two happened. Reads are answered from
	// this device; writes are queued and owed. It is printed the moment it
	// happens, above the live line, rather than saved for the end.
	if reason := v.OfflineBecause(); reason != nil {
		out.warn(degraded(reason, f.indexer))
	}
	return v, nil
}

// closing releases the vault and hands any failure to the session.
//
// The command's result is already decided by the time this runs and a close
// failure does not undo work that succeeded, so it changes no exit code. It is
// still the only notice that the next run may have something to recover: a
// record queued but not flushed stays claimed, and the device store was left to
// the operating system rather than closed.
//
// It is a warning, and it is printed last, after the command's own outcome and
// after any error. A defer runs before the command's failure is reported, so
// printing it here and then would put it where a reader takes it for the reason
// the command failed.
func closing(out *session, v *vault.Vault) {
	if err := v.Close(); err != nil {
		out.closeWarning = err
	}
}

// finish prints what was held back until the command's own outcome was on
// screen.
func (s *session) finish() {
	if s.closeWarning == nil {
		return
	}
	s.note(ui.Message{Kind: ui.KindWarning, Text: "the vault did not close cleanly: " + s.closeWarning.Error()})
	s.closeWarning = nil
}
