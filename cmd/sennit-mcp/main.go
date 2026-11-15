// Command sennit-mcp serves a vault over the Model Context Protocol.
//
// It speaks stdio, which is the transport the specification directs a local
// server to use and the one with no listening socket at all: the plaintext never
// leaves the device, and there is nothing on the network to attack. Nothing but
// protocol traffic goes to stdout, every diagnostic goes to stderr, which is
// the channel hosts read and display.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/profile"
	"github.com/steven3002/sennit/vault"
)

func main() { os.Exit(run()) }

// run returns the exit status rather than taking it, so that the vault is closed
// on the way out however this ends. A transport that fails is the ordinary way a
// host ends this process, and a flush in flight when it does holds a claim on the
// records it is writing: waiting for it hands them back, abandoning it leaves them
// claimed until the claim expires.
func run() int {
	// SIGTERM as well as interrupt: a host that restarts its servers terminates
	// them, and a queued record that never reached a flush should be released by
	// a clean shutdown rather than left claimed until it expires.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 {
		usage()
		return 2
	}

	server, closeVault := serve(ctx)
	defer closeVault()

	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "sennit-mcp: %v\n", err)
		return 1
	}
	return 0
}

// serve opens the vault, or explains why it could not and serves anyway.
//
// A server that refuses to start tells the model nothing: the host reports a
// failed connection and the user is left guessing. One that starts and answers
// every call with what to do about it gives them a sentence they can act on,
// which is what the specification asks for, an unconfigured server is a tool
// execution error, not a protocol error. The guide reads either way, because it
// is the one thing a caller that cannot reach its vault still needs. A start
// stopped before the vault was open is the one exception, see notOpened.
func serve(ctx context.Context) (*mcp.Server, func()) {
	opened, err := open(ctx)
	if err != nil {
		notOpened(os.Stderr, ctx, err)
		return mcp.Unopened(err), func() {}
	}

	// The flush deadlines run for as long as the host keeps this process alive.
	// A short command flushes explicitly or leaves the queue for the next run; a
	// server that may sit for hours is exactly the case the timer was written
	// for, and without it a conversation stored an hour ago would still be on
	// this device alone.
	opened.StartFlushing(ctx, func(err error) {
		fmt.Fprintf(os.Stderr, "sennit-mcp: background flush: %v\n", err)
	})
	announce(os.Stderr, opened)
	return mcp.New(opened), func() {
		// The process is on its way out, so this changes no exit code. It is
		// still the only notice that a vault did not shut down cleanly, which is
		// what someone is looking for when the next run has to recover from it.
		if err := opened.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "sennit-mcp: close: %v\n", err)
		}
	}
}

// openProfiles is where the server looks for the profile sennit connect
// stored. Tests point it at a store of their own.
var openProfiles = profile.Open

// open opens the vault.
//
// Where the phrase and the key came from is said before the vault is opened,
// not after, so that a start that fails, on a key the indexer refuses, says
// which key that was.
func open(ctx context.Context) (*vault.Vault, error) {
	creds, err := credentialsFor(os.Stderr)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "sennit-mcp: %s\n", creds.from)
	return vault.Open(ctx, vault.Options{
		Home: vault.DefaultHome(), Phrase: creds.phrase, Indexer: creds.indexer, AppKey: creds.key,
		OnProgress: rebuildProgress(os.Stderr, time.Now),
	})
}

// credentials are the secrets a vault is opened with, the indexer that goes
// with the key, and where they came from.
type credentials struct {
	phrase  string
	key     keys.AppKey
	indexer string
	from    string
}

// credentialsFor takes the phrase and the key from the environment, and either
// one the environment does not supply from the profile sennit connect stored.
//
// Neither comes from stdin, which is the protocol transport here, so reading a
// phrase from it would consume the client's first message, and neither is ever
// a flag, because a flag lands in the process table and in shell history. A
// stored key comes with the indexer that issued it, unless SENNIT_INDEXER names
// one. A key in the environment that is not the stored one is used, and said
// in the log, since a variable left in a host's configuration from an earlier
// connect would otherwise point this server at another account in silence.
func credentialsFor(log io.Writer) (credentials, error) {
	creds := credentials{indexer: vault.DefaultIndexer()}
	phrase, phraseErr := keys.ReadPhrase(nil)
	if phraseErr != nil && !errors.Is(phraseErr, keys.ErrNoPhrase) {
		return creds, phraseErr
	}
	key, keyErr := keys.AppKeyFromEnv()
	if keyErr != nil && !keys.MissingAppKey(keyErr) {
		return creds, keyErr
	}

	var (
		stored  profile.Profile
		summary profile.Summary
		found   bool
	)
	if store, err := openProfiles(); err == nil {
		if phraseErr != nil || keyErr != nil {
			stored, summary, err = store.Load()
			switch {
			case err == nil:
				found = true
			case !errors.Is(err, profile.ErrNone):
				return creds, fmt.Errorf("read the default profile that sennit connect stored: %w", err)
			}
		} else if stored, err := store.Summary(); err == nil && stored.KeyID != profile.KeyID(key) {
			fmt.Fprintf(log, "sennit-mcp: %s is set, and is not the app key sennit connect stored; the variable "+
				"is used, so this server reaches the account its key was issued for\n", keys.AppKeyEnv)
		}
	}

	phraseFrom, keyFrom := keys.PhraseEnv, keys.AppKeyEnv
	if phraseErr != nil {
		if !found {
			return creds, phraseErr
		}
		phrase, phraseFrom = stored.Phrase, "the default profile"
	}
	if keyErr != nil {
		if !found {
			return creds, keyErr
		}
		key, keyFrom = stored.AppKey, "the default profile"
		if os.Getenv(vault.IndexerEnv) == "" {
			creds.indexer = stored.Indexer
		}
	}
	creds.phrase, creds.key = phrase, key
	creds.from = describeSources(phraseFrom, keyFrom, summary.Place)
	return creds, nil
}

// describeSources says where the phrase and the key came from, for the log.
func describeSources(phraseFrom, keyFrom string, place profile.Place) string {
	from := func(source string) string {
		if source == "the default profile" {
			return source + ", in " + place.Name
		}
		return source
	}
	if phraseFrom == keyFrom {
		return "phrase and app key from " + from(phraseFrom)
	}
	return "phrase from " + from(phraseFrom) + ", app key from " + from(keyFrom)
}

// announce tells the host what is being served, after anything the vault found
// as it opened that the person reading the host's log should know.
//
// A change to the catalog that a crash cut off was dropped as the catalog was
// read, and this is the one place a person hears of it from a server. An agent
// finds the same in sennit://vault. A listing the vault could not date by its
// records as it opened is said here too: it stays as it was, and the next open
// tries again.
func announce(w io.Writer, v *vault.Vault) {
	if repair := v.IndexRepair(); repair.Ran {
		fmt.Fprintf(w, "sennit-mcp: %s\n", rebuiltIndex(repair))
	}
	if dropped := v.DroppedCatalogChanges(); dropped > 0 {
		fmt.Fprintf(w, "sennit-mcp: %s\n", droppedChanges(dropped))
	}
	if reason := v.ListingNotDatedBecause(); reason != nil {
		fmt.Fprintf(w, "sennit-mcp: the listing could not be dated by its records, "+
			"and is tried again at the next open: %v\n", reason)
	}
	fmt.Fprintf(w, "sennit-mcp: serving %s over stdio\n", describe(v))
}

// droppedChanges says what dropping changes the catalog found cut off undid,
// and what has to be run again.
func droppedChanges(n int) string {
	if n == 1 {
		return "the catalog's last change was cut off by a crash and has been dropped: it was one " +
			"record's latest change, and a forget, hydrate or recover that was running then should be run again"
	}
	return fmt.Sprintf("%d changes to the catalog were cut off by crashes and have been dropped: each was "+
		"one record's latest change, and a forget, hydrate or recover that was running then should be run again", n)
}

func describe(v *vault.Vault) string {
	if !v.Online() {
		return "a vault with no connection to an indexer"
	}
	return "a vault on " + v.Indexer()
}

// explain turns the two failures a first run actually hits into instructions
// rather than an error string.
func explain(w io.Writer, err error) {
	switch {
	case keys.MissingAppKey(err):
		fmt.Fprintf(w, ""+
			"  Run sennit connect once, as the account this server runs as. It approves this\n"+
			"  installation and stores the app key where this server finds it. Set %s\n"+
			"  in this server's environment instead to supply one here: in a host's MCP config\n"+
			"  that is the server's \"env\" block. It is a secret: never pass it as an argument.\n",
			keys.AppKeyEnv)
	case keys.WrongAppKeyLength(err):
		fmt.Fprintf(w, ""+
			"  The key in %s reached this server damaged, most likely\n"+
			"  truncated by the copy or the secret store it came through. Set it to the\n"+
			"  whole value approval issued, or run sennit connect again for a new one.\n",
			keys.AppKeyEnv)
	case errors.Is(err, keys.ErrNoPhrase):
		fmt.Fprintf(w, ""+
			"  Run sennit connect once, as the account this server runs as. It stores the\n"+
			"  phrase with the app key where this server finds them. Set %s in this\n"+
			"  server's environment instead to supply it here. It cannot be read from stdin,\n"+
			"  because stdin carries the protocol.\n", keys.PhraseEnv)
	}
	fmt.Fprint(w, "  Serving anyway: every tool will report this until it is fixed.\n")
}

func usage() {
	fmt.Fprintf(os.Stderr, `sennit-mcp, the user's own memory over the Model Context Protocol

It takes no arguments. A host launches it and speaks MCP over stdin and stdout.

environment:
  %-18s BIP-39 recovery phrase (default: the one sennit connect stored)
  %-18s Sia app key (default: the one sennit connect stored)
  %-18s vault directory (default %s)
  %-18s indexer URL (default %s, or the one the stored key is for)
  %-18s where embedding models are kept

Add it to a host's MCP configuration as a stdio server running this binary with
no arguments. Once sennit connect has run as the same account, nothing else is
needed: the phrase and the key are read from where it stored them.
`,
		keys.PhraseEnv, keys.AppKeyEnv,
		vault.HomeEnv, vault.DefaultHome(),
		vault.IndexerEnv, vault.DefaultIndexer(),
		vault.ModelDirEnv)
}
