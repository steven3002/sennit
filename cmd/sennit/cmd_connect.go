package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

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
func runConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	out := fs.String("out", "", "file to write the issued app key to, created 0600 (required)")
	indexer := fs.String("indexer", vault.DefaultIndexer(), "indexer URL")
	budget := fs.Duration("wait", sia.ApprovalBudget, "how long to keep a live approval link available")
	ready := fs.Duration("ready", 90*time.Second, "how long to wait for the account to become usable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required: the app key is a secret and is written to a file " +
			"rather than printed, so it does not land in a terminal's scrollback or a recording")
	}

	// The phrase is read once, used twice, to register and to derive the vault
	// keys, and never written anywhere.
	phrase, err := keys.ReadPhrase(os.Stdin)
	if err != nil {
		return err
	}

	fmt.Printf("Connecting to %s\n", *indexer)
	fmt.Printf("This needs one approval in a browser. The link below expires after about ten\n" +
		"minutes; a fresh one is issued automatically until you approve or the budget runs out.\n")

	result, err := sia.Approve(ctx, phrase, sia.ApprovalRequest{
		Indexer: *indexer,
		Budget:  *budget,
		OnURL: func(url string, attempt int) {
			if attempt > 1 {
				fmt.Printf("\n  the previous link expired unapproved, here is a fresh one (#%d)\n", attempt)
			}
			fmt.Printf("\n  approve this: %s\n\n  waiting...\n", url)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("\n  approved after %s, over %d request(s)\n",
		result.WaitedFor.Round(time.Second), result.Attempts)

	if err := writeAppKey(*out, result.AppKey); err != nil {
		return err
	}
	fmt.Printf("  app key %s… written to %s\n", result.AppKey.Fingerprint(), *out)

	// Approval is not readiness. The indexer funds host accounts after the
	// connection is approved, and a write before that completes fails with an
	// error about hosts that says nothing about waiting.
	client, err := sia.Connect(sia.Config{Indexer: *indexer, AppKey: result.AppKey})
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Printf("  funding host accounts (this is the ~16 s step that follows approval)...\n")
	waited := time.Now()
	account, err := client.WaitReady(ctx, *ready)
	if err != nil {
		return err
	}
	fmt.Printf("  ready after %s · %s of %s quota in use\n\n",
		time.Since(waited).Round(time.Second),
		humanBytes(account.PinnedData), humanBytes(account.MaxPinnedData))

	fmt.Printf("Load the key into this shell without putting it in your history:\n"+
		"  export %s=$(cat %s)\n", keys.AppKeyEnv, *out)
	return nil
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
