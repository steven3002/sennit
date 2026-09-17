package main

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
	"github.com/steven3002/sennit/vault"
)

func rendered(s *screen, lines []ui.Line) string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = s.out.out.Render(line)
	}
	return uitest.Lines(out...)
}

// Every help screen, as approved, from the flags the commands actually define.
func TestHelpScreens(t *testing.T) {
	s := terminal(t, 80)
	for name, lines := range map[string][]ui.Line{
		"help-top.txt":  topLevelHelp(),
		"help-bare.txt": bareHelp(),
	} {
		if got, want := uitest.Strip(rendered(s, lines)), fixture(t, name); got != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
	for _, command := range []string{"init", "connect", "remember", "recall", "flush", "status", "reclaim", "recover", "hydrate"} {
		got := uitest.Strip(rendered(s, commandHelpLines(command, flagsOf(t, command))))
		if want := fixture(t, "help-"+command+".txt"); got != want {
			t.Errorf("sennit %s --help:\n got %q\nwant %q", command, got, want)
		}
	}
}

// The headings are bold and the prompt is faint, and nothing else in a help
// screen is coloured.
func TestHelpIsStyledOnlyWhereItWasApproved(t *testing.T) {
	s := terminal(t, 80)
	got := rendered(s, bareHelp())
	if uitest.HasEscape(got) {
		t.Errorf("the short screen has no headings and no prompts, so nothing in it is styled: %q", got)
	}
	got = rendered(s, topLevelHelp())
	for _, want := range []string{"\x1b[1mUSAGE\x1b[0m", "\x1b[1mLEARN MORE\x1b[0m", "  \x1b[2m$\x1b[0m sennit init --new-phrase"} {
		if !strings.Contains(got, want) {
			t.Errorf("the top-level screen is missing %q", want)
		}
	}
}

// remember does not wait for the upload unless it is asked to.
//
// A flush mints a whole 40 MiB slab whatever it carries, so a default of true
// bought one slab per memory: measured live, 1,018 bytes of payload took 22.8 s
// and moved the account from 40.00 MiB to 80.00 MiB. The default is the defect,
// so the default is what is pinned here, and the help screen that states it is
// checked alongside, because a wrong promise about durability is the one thing
// this interface may not make.
func TestRememberDoesNotWaitForTheUploadByDefault(t *testing.T) {
	flush := flagsOf(t, "remember").Lookup("flush")
	if flush == nil {
		t.Fatal("remember has no --flush flag")
	}
	if flush.DefValue != "false" {
		t.Errorf("--flush defaults to %q: a bare remember pays a whole slab and the upload's wall time "+
			"for one record", flush.DefValue)
	}
	screen := uitest.Strip(rendered(terminal(t, 80), commandHelpLines("remember", flagsOf(t, "remember"))))
	if !strings.Contains(screen, "default false") {
		t.Errorf("the help screen does not say the flag is off by default:\n%s", screen)
	}

	// A fast return must not be readable as a completed upload, so the line a
	// default write ends on names the device and never claims the network.
	out := ended(t, 80, 800*time.Millisecond)
	reportRemembered(out.out, writeOutcome{result: vault.RememberResult{ID: id(t, "4083dbdd9e9f0f816c797080eed61fba")}})
	said := out.text()
	if !strings.Contains(said, "Remembered on this device, queued for Sia") {
		t.Errorf("a write that did not reach Sia ended on %q", said)
	}
	if strings.Contains(said, "stored on Sia") {
		t.Errorf("a write that did not reach Sia said it was stored there: %q", said)
	}
}

// A flag that exists and is not in the help is a flag nobody finds, and a flag
// in the help that does not exist is a lie. The screens are built from the
// FlagSet, so this checks the two lists are the same one.
func TestEveryFlagAppearsInItsHelpScreen(t *testing.T) {
	for _, command := range []string{"init", "connect", "remember", "recall", "flush", "status", "reclaim", "recover", "hydrate"} {
		set := flagsOf(t, command)
		screen := uitest.Strip(rendered(terminal(t, 80), commandHelpLines(command, set)))
		set.VisitAll(func(f *flag.Flag) {
			if !strings.Contains(screen, "--"+f.Name) {
				t.Errorf("sennit %s defines --%s and its help does not mention it", command, f.Name)
			}
		})
		groups := [][]flagHelp{commandHelps[command].flags}
		if commandHelps[command].vault {
			groups = append(groups, vaultFlagHelp)
		}
		for _, group := range groups {
			for _, shown := range group {
				if set.Lookup(shown.flag) == nil {
					t.Errorf("sennit %s --help shows %s and the command has no such flag", command, shown.shown)
				}
			}
		}
	}
}

// flagsOf builds a command's flags the way the command does, from the same
// definition, so a flag that exists in one exists in the other.
func flagsOf(t *testing.T, command string) *flag.FlagSet {
	t.Helper()
	define := map[string]func() *invocation{
		"init": func() *invocation {
			cmd := newInvocation("init").withVault().withVerbose()
			initFlags(cmd)
			return cmd
		},
		"connect": func() *invocation {
			cmd := newInvocation("connect")
			connectFlags(cmd)
			return cmd
		},
		"remember": func() *invocation {
			cmd := newInvocation("remember").withVault().withVerbose()
			rememberFlags(cmd)
			return cmd
		},
		"recall": func() *invocation {
			cmd := newInvocation("recall").withVault().withVerbose()
			recallFlags(cmd)
			return cmd
		},
		"flush":  func() *invocation { return newInvocation("flush").withVault().withVerbose() },
		"status": func() *invocation { return newInvocation("status").withVault().withVerbose() },
		"reclaim": func() *invocation {
			cmd := newInvocation("reclaim").withVault().withVerbose()
			reclaimFlags(cmd)
			return cmd
		},
		"recover": func() *invocation {
			cmd := newInvocation("recover").withVault().withVerbose()
			recoverFlags(cmd)
			return cmd
		},
		"hydrate": func() *invocation {
			cmd := newInvocation("hydrate").withVault().withVerbose()
			hydrateFlags(cmd)
			return cmd
		},
	}[command]
	if define == nil {
		t.Fatalf("no command %q", command)
	}
	return define().set
}

func TestSuggestionsForAMistypedCommand(t *testing.T) {
	for typed, want := range map[string][]string{
		"recal":   {"recall"},
		"re":      {"remember", "recall", "reclaim", "recover"},
		"sync":    nil,
		"Recall":  {"recall"},
		"stat":    {"status"},
		"hydrat":  {"hydrate"},
		"connekt": {"connect"},
	} {
		got := suggestions(typed)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("suggestions(%q) = %v, want %v", typed, got, want)
		}
	}
}

func TestAnUnknownCommandIsRefusedWithWhatWasProbablyMeant(t *testing.T) {
	for _, c := range []struct{ typed, hint string }{
		{"recal", "did you mean `recall`?"},
		{"re", "did you mean `remember`, `recall`, `reclaim` or `recover`?"},
		{"sync", "`sennit --help` lists every command"},
	} {
		p := unknownCommand(c.typed)
		if p.message.Text != `unknown command "`+c.typed+`"` {
			t.Errorf("%q: %q", c.typed, p.message.Text)
		}
		if len(p.message.Hints) != 1 || p.message.Hints[0] != c.hint {
			t.Errorf("%q: hints %v, want %q", c.typed, p.message.Hints, c.hint)
		}
		if statusOf(p) != 2 {
			t.Errorf("%q: exit %d, want 2", c.typed, statusOf(p))
		}
	}
}
