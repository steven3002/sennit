package main

import (
	"context"
	"fmt"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// runInit prepares a vault: derive keys, fetch the embedding model, open the
// device store, and confirm the indexer will accept writes.
func runInit(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("init").withVault().withVerbose()
	newPhrase, wait := initFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}

	if *newPhrase {
		// The phrase is the only thing on stdout, so it can be read by a script
		// without anything else coming with it.
		out.result(keys.NewPhrase())
		out.note(ui.Message{
			Kind:        ui.KindWarning,
			Text:        "this phrase is the vault: anyone holding it can read every memory, and losing it loses the data",
			Explanation: []string{"It is not stored anywhere."},
		})
		return nil
	}

	start := time.Now()
	v, err := cmd.vault.open(ctx, out, openPhases)
	if err != nil {
		return err
	}
	defer closing(out, v)
	prepared := fmt.Sprintf("  keys derived, model loaded, device store ready in %s", took(time.Since(start)))

	if !v.Online() {
		out.done(ui.MarkSuccess, "Vault ready on this device")
		out.report(preparedRows(cmd.vault.home, "", sia.Account{}))
		out.detail(prepared)
		return nil
	}

	account, err := awaitAccount(ctx, out, v, *wait)
	if err != nil {
		return err
	}
	out.done(ui.MarkSuccess, "Vault ready")
	out.report(preparedRows(cmd.vault.home, v.Indexer(), account))
	out.detail(prepared)
	return nil
}

// initFlags is what sennit init takes.
func initFlags(cmd *invocation) (newPhrase *bool, wait *time.Duration) {
	return cmd.set.Bool("new-phrase", false, "print a fresh recovery phrase and exit without touching the vault"),
		cmd.set.Duration("wait", 60*time.Second, "how long to wait for the indexer to finish funding host accounts")
}

// preparedRows is where the vault is and what it may write. An offline vault
// says so in place of the account, because there is no account to report and
// the difference is the whole point of the flag.
func preparedRows(home, indexer string, account sia.Account) [][2]string {
	if indexer == "" {
		return [][2]string{
			{"Vault", home},
			{"Sia", "offline: the vault works from this device but writes nothing to Sia"},
		}
	}
	return [][2]string{
		{"Vault", home},
		{"Indexer", indexer},
		{"Quota", fmt.Sprintf("%s used of %s (%s free)",
			humanBytes(account.PinnedData), humanBytes(account.MaxPinnedData), humanBytes(account.Free()))},
	}
}

// awaitAccount waits for the indexer to be able to accept a write.
//
// Approval and readiness are separate events: the indexer funds host accounts
// after a connection is approved, and a write attempted before that finishes
// fails for a reason that has nothing to do with the write. The wait is a phase
// of its own, carrying the budget, because it is the one wait here that can run
// into minutes.
func awaitAccount(ctx context.Context, out *session, v *vault.Vault, wait time.Duration) (sia.Account, error) {
	out.phase(accountPhase)
	account, err := v.Account(ctx)
	if err != nil || account.Ready {
		return account, err
	}
	out.phase(fmt.Sprintf("Waiting up to %s for the indexer to fund host accounts", budget(wait)))
	return v.WaitReady(ctx, wait)
}

// budget writes a waiting time the way the flag that sets it is written.
func budget(d time.Duration) string { return fmt.Sprintf("%ds", int(d.Seconds())) }
