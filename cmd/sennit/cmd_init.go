package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/vault"
)

// runInit prepares a vault: derive keys, fetch the embedding model, open the
// device store, and confirm the indexer will accept writes.
func runInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	var flags vaultFlags
	flags.bind(fs)
	newPhrase := fs.Bool("new-phrase", false, "print a fresh recovery phrase and exit without touching the vault")
	wait := fs.Duration("wait", 60*time.Second, "how long to wait for the indexer to finish funding host accounts")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *newPhrase {
		fmt.Println(keys.NewPhrase())
		fmt.Fprint(stderr, "\nThis phrase is the vault. Anyone holding it can read every memory,\n"+
			"and losing it loses the data. It is not stored anywhere.\n")
		return nil
	}

	fmt.Fprintf(stderr, "preparing vault in %s\n", flags.home)
	start := time.Now()
	v, err := flags.open(ctx)
	if err != nil {
		return err
	}
	defer closing(v)
	fmt.Fprintf(stderr, "  keys derived, model loaded, device store ready in %s\n", took(time.Since(start)))

	if !v.Online() {
		fmt.Fprint(stderr, "  offline: the vault works from this device but writes nothing to Sia\n")
		return nil
	}
	return reportAccount(ctx, v, *wait)
}

// reportAccount waits for the indexer to be able to accept a write.
//
// Approval and readiness are separate events: the indexer funds host accounts
// after a connection is approved, and a write attempted before that finishes
// fails for a reason that has nothing to do with the write.
func reportAccount(ctx context.Context, v *vault.Vault, wait time.Duration) error {
	account, err := v.Account(ctx)
	if err != nil {
		return err
	}
	if !account.Ready {
		fmt.Fprintf(stderr, "  indexer is still funding host accounts, waiting up to %s\n", wait)
		if account, err = v.WaitReady(ctx, wait); err != nil {
			return err
		}
	}
	fmt.Fprintf(stderr, "  connected: %s\n", v.Indexer())
	fmt.Fprintf(stderr, "  quota:     %s used of %s (%s free)\n",
		humanBytes(account.PinnedData), humanBytes(account.MaxPinnedData), humanBytes(account.Free()))
	return nil
}
