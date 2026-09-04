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
	"os"
	"os/signal"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/mcp"
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
// is the one thing a caller that cannot reach its vault still needs.
func serve(ctx context.Context) (*mcp.Server, func()) {
	opened, err := open(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sennit-mcp: %v\n", err)
		explain(err)
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
	fmt.Fprintf(os.Stderr, "sennit-mcp: serving %s over stdio\n", describe(opened))
	return mcp.New(opened), func() {
		// The process is on its way out, so this changes no exit code. It is
		// still the only notice that a vault did not shut down cleanly, which is
		// what someone is looking for when the next run has to recover from it.
		if err := opened.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "sennit-mcp: close: %v\n", err)
		}
	}
}

func open(ctx context.Context) (*vault.Vault, error) {
	// The phrase comes from the environment and never from stdin. stdin is the
	// protocol transport here, so the command line's fallback would consume the
	// client's first message, and neither secret is ever a flag, because a flag
	// lands in the process table and in shell history.
	phrase, err := keys.ReadPhrase(nil)
	if err != nil {
		return nil, err
	}
	opts := vault.Options{Home: vault.DefaultHome(), Phrase: phrase, Indexer: vault.DefaultIndexer()}

	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		return nil, err
	}
	opts.AppKey = appKey
	return vault.Open(ctx, opts)
}

func describe(v *vault.Vault) string {
	if !v.Online() {
		return "a vault with no connection to an indexer"
	}
	return "a vault on " + v.Indexer()
}

// explain turns the two failures a first run actually hits into instructions
// rather than an error string.
func explain(err error) {
	switch {
	case keys.MissingAppKey(err):
		fmt.Fprintf(os.Stderr, ""+
			"  Set %s in this server's environment to the app key issued when you\n"+
			"  approved this installation with your indexer. In a host's MCP config that is\n"+
			"  the server's \"env\" block. It is a secret: never pass it as an argument.\n",
			keys.AppKeyEnv)
	case keys.WrongAppKeyLength(err):
		fmt.Fprintf(os.Stderr, ""+
			"  The key in %s reached this server damaged, most likely\n"+
			"  truncated by the copy or the secret store it came through. Set it to the\n"+
			"  whole value approval issued, or run sennit connect again for a new one.\n",
			keys.AppKeyEnv)
	case errors.Is(err, keys.ErrNoPhrase):
		fmt.Fprintf(os.Stderr, ""+
			"  Set %s in this server's environment. The vault derives its keys from it\n"+
			"  on every run and never stores it. It cannot be read from stdin here, because\n"+
			"  stdin carries the protocol.\n", keys.PhraseEnv)
	}
	fmt.Fprint(os.Stderr, "  Serving anyway: every tool will report this until it is fixed.\n")
}

func usage() {
	fmt.Fprintf(os.Stderr, `sennit-mcp, the user's own memory over the Model Context Protocol

It takes no arguments. A host launches it and speaks MCP over stdin and stdout.

environment:
  %-18s BIP-39 recovery phrase (required)
  %-18s Sia app key, issued by indexer approval (required)
  %-18s vault directory (default %s)
  %-18s indexer URL (default %s)
  %-18s where embedding models are kept

Add it to a host's MCP configuration as a stdio server running this binary with
no arguments and both secrets in its environment.
`,
		keys.PhraseEnv, keys.AppKeyEnv,
		vault.HomeEnv, vault.DefaultHome(),
		vault.IndexerEnv, vault.DefaultIndexer(),
		vault.ModelDirEnv)
}
