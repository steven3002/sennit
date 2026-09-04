package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/vault"
)

// vaultFlags are the options every command shares.
type vaultFlags struct {
	home    string
	indexer string
	offline bool
}

// checkFlagOrder rejects a flag written after the positional arguments.
//
// Go's flag package stops parsing at the first non-flag word, so
// `remember "..." -offline` leaves -offline as a second piece of the statement
// and the command goes on to fail for an unrelated reason, asking for an app
// key the user had just said they did not want to use. Silently taking a flag
// as prose is worse than refusing it: the run appears to be about something
// else entirely.
func checkFlagOrder(rest []string) error {
	for _, arg := range rest {
		if len(arg) > 1 && strings.HasPrefix(arg, "-") {
			return fmt.Errorf(
				"%q looks like a flag but comes after the text, where it is read as part of it. "+
					"Flags go first: sennit <command> %s \"<text>\"", arg, arg)
		}
	}
	return nil
}

func (f *vaultFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.home, "home", vault.DefaultHome(), "vault directory")
	fs.StringVar(&f.indexer, "indexer", vault.DefaultIndexer(), "indexer URL")
	fs.BoolVar(&f.offline, "offline", false, "work from this device only, without contacting the indexer")
}

// open prepares a vault from the flags and the environment.
//
// Neither secret is a flag. A recovery phrase or an app key passed as an
// argument is visible in the process table and lands in shell history, which
// would make every other precaution in the design decorative.
func (f *vaultFlags) open(ctx context.Context) (*vault.Vault, error) {
	phrase, err := keys.ReadPhrase(os.Stdin)
	if err != nil {
		return nil, err
	}
	opts := vault.Options{
		Home:    f.home,
		Phrase:  phrase,
		Indexer: f.indexer,
		Offline: f.offline,
	}
	if !f.offline {
		appKey, err := keys.AppKeyFromEnv()
		if err != nil {
			return nil, err
		}
		opts.AppKey = appKey
	}
	v, err := vault.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	// A vault that wanted the network and did not get it still works, and the
	// user has to be told which of the two happened. Reads are answered from
	// this device; writes are queued and owed.
	if reason := v.OfflineBecause(); reason != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n"+
			"         Working from this device only. Reads are answered locally; "+
			"anything written is queued until a run that reaches the indexer.\n", reason)
	}
	return v, nil
}

// closing releases the vault and says so when the shutdown did not complete.
//
// The command's result is already decided by the time this runs and a close
// failure does not undo work that succeeded, so it changes no exit code. It is
// still the only notice that the next run may have something to recover: a
// record queued but not flushed stays claimed, and the device store was left to
// the operating system rather than closed.
//
// It is a warning rather than an error line because a defer runs before the
// command's own failure is reported, and two "sennit:" lines in that order
// would make this one look like the reason the command failed.
func closing(v *vault.Vault) {
	if err := v.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: the vault did not close cleanly: %v\n", err)
	}
}
