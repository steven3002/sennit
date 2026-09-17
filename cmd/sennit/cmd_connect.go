package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// runConnect walks the whole onboarding path: approve, register, wait for the
// account to be usable, and write down the credential that makes every later run
// headless.
//
// It exists because the honest path has three pieces of friction and a
// documentation page is a poor place to meet them. The approval request expires
// in about ten minutes, so a request issued while the user goes to find a browser
// is often already dead; approval is not readiness, because the indexer funds
// host accounts afterwards and a write before that fails with a message about
// hosts; and the app key is a secret that must not be typed on a command line.
func runConnect(ctx context.Context, out *session, args []string) error {
	cmd := newInvocation("connect")
	keyFile, indexer, budget, ready := connectFlags(cmd)
	if err := cmd.parse(out, args); err != nil {
		return err
	}
	if *keyFile == "" {
		return refuse("--out is required").
			because("The app key is a secret and is written to a file rather than printed, " +
				"so it does not land in a terminal's scrollback or a recording.").
			try("`sennit connect --out sennit.key`")
	}

	// The phrase is read once, used twice, to register and to derive the vault
	// keys, and never written anywhere.
	phrase, err := keys.ReadPhrase(os.Stdin)
	if err != nil {
		return err
	}

	// Everything this command prints is for the person at the terminal, and
	// none of it is a result another program would read, so all of it goes to
	// stderr. The link is printed above the live line and never cut.
	out.above(
		"Connecting to "+*indexer,
		"This needs one approval in a browser. The link below expires after about ten",
		"minutes; a fresh one is issued automatically until you approve or the budget runs out.")
	out.leaves("No app key was issued. Run `sennit connect --out " + *keyFile + "` again for a fresh link.")
	out.phase("Requesting an approval link")

	result, err := sia.Approve(ctx, phrase, sia.ApprovalRequest{
		Indexer: *indexer,
		Budget:  *budget,
		OnURL: func(url string, attempt int) {
			lines := []string{""}
			if attempt > 1 {
				lines = append(lines,
					fmt.Sprintf("  the previous link expired unapproved, here is a fresh one (#%d)", attempt), "")
			}
			out.above(append(lines, "  approve this: "+url)...)
			out.phase("Waiting for approval")
		},
		OnApproved: func() { out.phase("Registering this installation") },
	})
	if err != nil {
		return err
	}
	if err := writeAppKey(*keyFile, result.AppKey); err != nil {
		return err
	}
	out.leaves("The app key is already written to " + *keyFile +
		". The indexer may need a little longer before the account can write.")

	// Approval is not readiness. The indexer funds host accounts after the
	// connection is approved, and a write before that completes fails with an
	// error about hosts that says nothing about waiting.
	out.phase("Waiting for the indexer to fund host accounts")
	client, err := sia.Connect(sia.Config{Indexer: *indexer, AppKey: result.AppKey})
	if err != nil {
		return err
	}
	defer client.Close()

	waited := time.Now()
	account, err := client.WaitReady(ctx, *ready)
	if err != nil {
		return err
	}

	out.done(ui.MarkSuccess, fmt.Sprintf("Connected: app key %s… written to %s",
		result.AppKey.Fingerprint(), *keyFile))
	out.above(append([]string{""}, ui.KeyValues(
		connectRows(result.Attempts, result.WaitedFor, time.Since(waited), account))...)...)
	out.note(ui.Message{Kind: ui.KindHint, Text: fmt.Sprintf(
		"load the key into this shell without putting it in your history: `export %s=$(cat %s)`",
		keys.AppKeyEnv, *keyFile)})
	return nil
}

// connectFlags is what sennit connect takes. It opens no vault, so it has none
// of the vault flags.
func connectFlags(cmd *invocation) (keyFile, indexer *string, budget, ready *time.Duration) {
	return cmd.set.String("out", "", "file to write the issued app key to, created 0600 (required)"),
		cmd.set.String("indexer", vault.DefaultIndexer(), "indexer URL"),
		cmd.set.Duration("wait", sia.ApprovalBudget, "how long to keep a live approval link available"),
		cmd.set.Duration("ready", 90*time.Second, "how long to wait for the account to become usable")
}

// connectRows is what the approval cost and where the account stands after it.
func connectRows(attempts int, approval, ready time.Duration, account sia.Account) [][2]string {
	return [][2]string{
		{"Approved", fmt.Sprintf("after %s, over %s", approval.Round(time.Second), plural(attempts, "request"))},
		{"Ready", fmt.Sprintf("after %s, %s of %s quota in use", ready.Round(time.Second),
			humanBytes(account.PinnedData), humanBytes(account.MaxPinnedData))},
	}
}

// writeAppKey stores the credential at 0600 and refuses to widen an existing
// file's permissions by writing through it.
func writeAppKey(path string, key keys.AppKey) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write the app key: %w", err)
	}
	if _, err := fmt.Fprintln(file, hex.EncodeToString(key)); err != nil {
		file.Close()
		return fmt.Errorf("write the app key: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("write the app key: %w", err)
	}
	// Closed before it is read back, so the check sees the file rather than a
	// buffer that has not reached it.
	if err := file.Close(); err != nil {
		return fmt.Errorf("write the app key: %w", err)
	}
	return verifyAppKey(path, key)
}

// verifyAppKey reads back what was written and confirms it decodes to the key
// that was issued.
//
// The approval happens once and cannot be repeated cheaply: the link expires in
// about ten minutes and a second round needs the person and the browser again.
// So the expensive thing to get wrong is a key file that exists, looks
// plausible, and does not carry the key, a truncated write, a full disk, an
// encoding slip. That failure surfaces much later as an authorization error
// against the indexer, by which time nothing connects it to this step.
func verifyAppKey(path string, want keys.AppKey) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read back the app key just written to %s: %w", path, err)
	}
	got, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("the app key written to %s cannot be decoded: %w", path, err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf(
			"the app key written to %s is not the key the indexer issued (%s on disk, %s issued): "+
				"the approval succeeded but the file did not, so remove it and run connect again",
			path, keys.AppKey(got).Fingerprint(), want.Fingerprint())
	}
	return nil
}
