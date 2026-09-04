// Command sennit is the command-line interface to a vault.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/steven3002/sennit/build"
	"github.com/steven3002/sennit/keys"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(os.Args) < 2 {
		usage()
		return 2
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(ctx, os.Args[2:])
	case "connect":
		err = runConnect(ctx, os.Args[2:])
	case "remember":
		err = runRemember(ctx, os.Args[2:])
	case "recall":
		err = runRecall(ctx, os.Args[2:])
	case "flush":
		err = runFlush(ctx, os.Args[2:])
	case "status":
		err = runStatus(ctx, os.Args[2:])
	case "reclaim":
		err = runReclaim(ctx, os.Args[2:])
	case "recover":
		err = runRecover(ctx, os.Args[2:])
	case "hydrate":
		err = runHydrate(ctx, os.Args[2:])
	case "version", "-version", "--version":
		fmt.Printf("sennit %s\n", build.String())
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "sennit: unknown command %q\n\n", os.Args[1])
		usage()
		return 2
	}

	if err != nil {
		report(err)
		return 1
	}
	return 0
}

// report explains the two failures a first run actually hits, rather than
// printing the underlying error and leaving the reader to work out what to do.
func report(err error) {
	switch {
	case keys.MissingAppKey(err):
		fmt.Fprintf(os.Stderr, "sennit: no Sia app key.\n\n"+
			"  Set %s to the app key issued when you approved this\n"+
			"  installation with your indexer. It is a secret: keep it out of\n"+
			"  shell history and never pass it as an argument.\n\n"+
			"  If you have not approved this installation yet, run:\n"+
			"    sennit connect -out sennit.key\n\n"+
			"  To work without an indexer, pass -offline before the arguments:\n"+
			"    sennit remember -offline -context \"...\" \"...\"\n", keys.AppKeyEnv)
	case keys.WrongAppKeyLength(err):
		fmt.Fprintf(os.Stderr, "sennit: %v.\n\n"+
			"  The key in %s arrived damaged, most likely truncated\n"+
			"  by the copy or the secret store it came through. Set it to the whole\n"+
			"  value that approval issued, or issue a new one:\n"+
			"    sennit connect -out sennit.key\n", err, keys.AppKeyEnv)
	case errors.Is(err, keys.ErrNoPhrase):
		fmt.Fprintf(os.Stderr, "sennit: no recovery phrase.\n\n"+
			"  Set %s, or pipe the phrase in on stdin. The vault derives its\n"+
			"  keys from it on every run and never stores it.\n\n"+
			"  If you do not have one yet, this prints a new one and stores nothing:\n"+
			"    sennit init -new-phrase\n", keys.PhraseEnv)
	default:
		fmt.Fprintf(os.Stderr, "sennit: %v\n", err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `sennit, user-owned storage for an AI's memory, on Sia

usage:
  sennit init -new-phrase          print a fresh recovery phrase and exit
  sennit connect -out <file>       approve this installation with an indexer
  sennit init                      derive keys, prepare the vault, connect
  sennit remember -context "<why it matters>" "<statement>"
                                     store a memory; -context is required
  sennit recall "<query>"          retrieve memories by meaning
  sennit flush                     write queued records to Sia now
  sennit status                    what is held, what is queued, what is billed
  sennit reclaim                   release storage nothing points at any more
  sennit recover                   rebuild this vault from the phrase and the indexer
  sennit hydrate                   restore this vault on a machine that never held it

environment:
  SENNIT_PHRASE     BIP-39 recovery phrase; read from stdin when unset
  SENNIT_APP_KEY    Sia app key, issued by indexer approval
  SENNIT_HOME       vault directory (default ~/.sennit)
  SENNIT_INDEXER    indexer URL (default https://sia.storage)
  SENNIT_MODEL_DIR  where embedding models are kept

run "sennit <command> -h" for the flags of one command.
`)
}
