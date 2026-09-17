// Command sennit is the command-line interface to a vault.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/steven3002/sennit/build"
	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	out, restore := newSession(ctx.Done())
	defer restore()

	if len(os.Args) < 2 {
		printHelp(out, bareHelp())
		return 0
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(ctx, out, os.Args[2:])
	case "connect":
		err = runConnect(ctx, out, os.Args[2:])
	case "remember":
		err = runRemember(ctx, out, os.Args[2:])
	case "recall":
		err = runRecall(ctx, out, os.Args[2:])
	case "flush":
		err = runFlush(ctx, out, os.Args[2:])
	case "status":
		err = runStatus(ctx, out, os.Args[2:])
	case "reclaim":
		err = runReclaim(ctx, out, os.Args[2:])
	case "recover":
		err = runRecover(ctx, out, os.Args[2:])
	case "hydrate":
		err = runHydrate(ctx, out, os.Args[2:])
	case "version", "-version", "--version":
		fmt.Fprintf(out.stdout, "sennit %s\n", build.String())
		return 0
	case "help", "-h", "--help":
		printHelp(out, topLevelHelp())
		return 0
	default:
		err = unknownCommand(os.Args[1])
	}
	return report(ctx, out, err)
}

// errHelpAsked reports that a command was asked for its help rather than run.
// Help is something a reader asked for, so it goes to stdout and the run
// succeeds.
var errHelpAsked = errors.New("help was asked for")

// report ends the run: the live line, then what went wrong, then anything the
// vault could not finish cleanly.
//
// An interrupt is not reported as an error. The person who pressed Ctrl-C knows
// what happened and needs to be told what it left behind, which the cancel line
// says; printing "context canceled" underneath it would be the program
// explaining the user's own key press back to them.
func report(ctx context.Context, out *session, err error) int {
	switch {
	case err == nil:
		out.finish()
		return 0
	case errors.Is(err, errHelpAsked):
		out.finish()
		return 0
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		out.cancelledBy()
		out.finish()
		return 1
	}
	out.clear()
	if out.printed {
		fmt.Fprintln(out.stderr)
	}
	for _, line := range out.messageLines(messageFor(err)) {
		fmt.Fprintln(out.stderr, line)
	}
	out.finish()
	return statusOf(err)
}

// An invocation is one command's flags: the ones it defines itself, and the
// ones every command shares.
type invocation struct {
	name  string
	set   *flag.FlagSet
	vault *vaultFlags
	color colorFlag
	// verbose is nil for a command that does not offer it.
	verbose *bool
}

// newInvocation prepares a command's flags. Parse errors are the command's own
// to report, so the flag package neither prints nor exits here.
func newInvocation(name string) *invocation {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	r := &invocation{name: name, set: set}
	set.Var(&r.color, "color", "when to colour output: auto, always or never")
	return r
}

// withVault adds the three flags every command that opens a vault shares.
func (r *invocation) withVault() *invocation {
	r.vault = &vaultFlags{}
	r.vault.bind(r.set)
	return r
}

// withVerbose adds the flag that shows timings and read tiers.
func (r *invocation) withVerbose() *invocation {
	r.verbose = r.set.Bool("verbose", false, "show timings, read tiers and other detail")
	return r
}

// parse reads the command's arguments and settles what its output may look
// like.
//
// A request for help is answered here rather than by the flag package, because
// the flag package writes to stderr and exits 2, and help is neither an error
// nor something to hunt for in a redirect. A real parse error stays an error
// and still exits 2.
func (r *invocation) parse(out *session, args []string) error {
	err := r.set.Parse(args)
	out.setColor(r.color.mode())
	if r.verbose != nil {
		out.verbose = *r.verbose
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, flag.ErrHelp):
		printHelp(out, commandHelpLines(r.name, r.set))
		return errHelpAsked
	}
	return refuse(err.Error()).
		try("`sennit " + r.name + " --help` lists its flags").
		exit(2).from(err)
}

// A colorFlag is --color, which takes auto, always or never.
type colorFlag string

func (c *colorFlag) String() string { return string(*c) }

func (c *colorFlag) Set(value string) error {
	mode, ok := ui.ParseColorMode(value)
	if !ok {
		return errParseFlag
	}
	*c = colorFlag(mode)
	return nil
}

// errParseFlag is the flag package's own wording for a value it cannot read,
// which is what a reader sees for every other flag that refuses one.
var errParseFlag = errors.New("parse error")

func (c colorFlag) mode() ui.ColorMode {
	if mode, ok := ui.ParseColorMode(string(c)); ok {
		return mode
	}
	return ui.ColorAuto
}
