package main

import (
	"flag"
	"fmt"
	"strings"
	"unicode"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/cmd/sennit/internal/width"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/vault"
)

// helpWidth is the width help is laid out for.
//
// It does not follow the terminal. Help goes to stdout, which is as often a
// pager or a file as a window, and eighty columns is the width that reads the
// same in all three.
const helpWidth = 80

// summary is the one line every screen opens with.
const summary = "User-owned storage for an AI's memory, on Sia."

// A command is one of the nine things sennit does, in the order the help groups
// them: what a reader does first at the top, and what they do after a loss at
// the bottom.
type command struct {
	name    string
	summary string
}

var groups = []struct {
	title    string
	commands []command
}{
	{"GET STARTED", []command{
		{"init", "Prepare a vault on this device"},
		{"connect", "Approve this installation with an indexer"},
	}},
	{"MEMORY", []command{
		{"remember", "Store a memory"},
		{"recall", "Find memories by meaning"},
	}},
	{"STORAGE ON SIA", []command{
		{"status", "Show what is held, queued and billed"},
		{"flush", "Upload queued records now"},
		{"reclaim", "Release storage nothing points to"},
	}},
	{"RESTORE", []command{
		{"recover", "Rebuild this vault from its phrase and the indexer"},
		{"hydrate", "Restore this vault on a machine that never held it"},
	}},
}

// environment lists the variables a reader has to set, and only those: the
// ones a person never sets by hand belong in the documentation.
var environment = [][2]string{
	{keys.PhraseEnv, "Recovery phrase; read from stdin when unset"},
	{keys.AppKeyEnv, "Sia app key, issued by sennit connect"},
	{vault.HomeEnv, "Vault directory (default ~/.sennit)"},
	{vault.IndexerEnv, "Indexer URL (default https://sia.storage)"},
	{vault.ModelDirEnv, "Where embedding models are kept"},
}

// topLevelHelp is everything sennit does, grouped by what a reader is trying to
// get done rather than alphabetically.
func topLevelHelp() []ui.Line {
	out := []ui.Line{text(summary), {}, heading("USAGE"), text("  sennit <command> [flags]")}
	for _, group := range groups {
		out = append(out, ui.Line{}, heading(group.title))
		for _, c := range group.commands {
			out = append(out, text("  "+width.Pad(c.name, 12)+c.summary))
		}
	}
	out = append(out, ui.Line{}, heading("FLAGS"),
		text("  -h, --help       Show help for sennit or a command"),
		text("      --version    Show the version"),
		ui.Line{}, heading("EXAMPLES"),
		example("sennit init --new-phrase"),
		example(`sennit remember --context "why it matters" "the statement"`),
		example(`sennit recall "what did we decide about packing"`),
		ui.Line{}, heading("ENVIRONMENT"))
	for _, variable := range environment {
		out = append(out, text("  "+width.Pad(variable[0], 19)+variable[1]))
	}
	return append(out, ui.Line{}, heading("LEARN MORE"),
		text("  Run `sennit <command> --help` for a command's flags."),
		text("  https://github.com/steven3002/sennit"))
}

// bareHelp is what someone who typed the name alone sees: the two commands the
// tool is for, and where to go next.
func bareHelp() []ui.Line {
	return []ui.Line{
		text(summary),
		{},
		text(`  sennit remember --context "<why it matters>" "<statement>"`),
		text(`  sennit recall "<query>"`),
		{},
		text("New here?      sennit init --new-phrase"),
		text("All commands:  sennit --help"),
	}
}

func heading(title string) ui.Line { return ui.Line{ui.S(ui.Bold, title)} }
func text(line string) ui.Line     { return ui.Line{ui.T(line)} }

// example marks the prompt faint, so what a reader copies is the command and
// not the dollar sign in front of it.
func example(line string) ui.Line {
	return ui.Line{ui.T("  "), ui.S(ui.Faint, "$"), ui.T(" " + line)}
}

// A flagHelp is one flag as the help shows it: the name with its placeholder,
// the flag it belongs to, and the default worth stating.
//
// The description is not repeated here. It is the flag's own, capitalised, so
// the help and the code cannot drift apart.
type flagHelp struct {
	flag        string
	shown       string
	defaultText string
}

// A commandHelp is one command's screen.
type commandHelp struct {
	summary  string
	usage    []string
	flags    []flagHelp
	vault    bool
	verbose  bool
	examples []string
	footer   string
}

// vaultFlagHelp and outputFlagHelp are the two groups every command shares.
// They are kept apart from a command's own flags because a reader looking for
// what this command does should not have to step over three flags that mean the
// same thing everywhere.
var (
	vaultFlagHelp = []flagHelp{
		{"home", "--home dir", "(default ~/.sennit)"},
		{"indexer", "--indexer url", "(default https://sia.storage)"},
		{"offline", "--offline", ""},
	}
	colorFlagHelp   = flagHelp{"color", "--color when", "(default auto)"}
	verboseFlagHelp = flagHelp{"verbose", "--verbose", ""}
)

var commandHelps = map[string]commandHelp{
	"init": {
		summary: "Prepare a vault on this device.",
		usage:   []string{"sennit init [flags]", "sennit init --new-phrase"},
		flags: []flagHelp{
			{"new-phrase", "--new-phrase", ""},
			{"wait", "--wait duration", "(default 60s)"},
		},
		vault: true, verbose: true,
		examples: []string{"sennit init --new-phrase", "sennit init", "sennit init --offline"},
	},
	"connect": {
		summary: "Approve this installation with an indexer.",
		usage:   []string{"sennit connect --out <file> [flags]"},
		flags: []flagHelp{
			{"out", "--out file", ""},
			{"indexer", "--indexer url", "(default https://sia.storage)"},
			{"wait", "--wait duration", "(default 30m)"},
			{"ready", "--ready duration", "(default 90s)"},
		},
		examples: []string{"sennit connect --out sennit.key", `export SENNIT_APP_KEY="$(cat sennit.key)"`},
		footer:   "The recovery phrase comes from SENNIT_PHRASE, or from stdin when that is unset.",
	},
	"remember": {
		summary: "Store a memory.",
		usage:   []string{"sennit remember --context <text> [flags] <statement>"},
		flags: []flagHelp{
			{"context", "--context text", "(required)"},
			{"type", "--type type", "(default fact)"},
			{"tags", "--tags list", ""},
			{"supersedes", "--supersedes id", ""},
			{"flush", "--flush", "(default false; the next flush uploads it)"},
		},
		vault: true, verbose: true,
		examples: []string{
			`sennit remember --context "why it matters" "the statement"`,
			`sennit remember --type preference --tags cli,ui --context "choosing a CLI look" "Prefers one live status line"`,
		},
		footer: "Flags go before the statement.",
	},
	"recall": {
		summary: "Find memories by meaning.",
		usage:   []string{"sennit recall [flags] <query>"},
		flags: []flagHelp{
			{"limit", "--limit n", "(default 5)"},
			{"tags", "--tags list", ""},
			{"types", "--types list", ""},
			{"history", "--history", ""},
			{"from-network", "--from-network", ""},
			{"memory-text", "--memory-text", ""},
			{"json", "--json", ""},
		},
		vault: true, verbose: true,
		examples: []string{
			`sennit recall "how should progress be shown"`,
			`sennit recall --tags grant --limit 3 "budget"`,
			`sennit recall --memory-text "how should progress be shown"`,
		},
		footer: "Flags go before the query.",
	},
	"flush": {
		summary: "Upload queued records to Sia now.",
		usage:   []string{"sennit flush [flags]"},
		vault:   true, verbose: true,
		examples: []string{"sennit flush"},
	},
	"status": {
		summary: "Show what is held, queued and billed.",
		usage:   []string{"sennit status [flags]"},
		vault:   true, verbose: true,
		examples: []string{"sennit status", "sennit status --offline"},
	},
	"reclaim": {
		summary: "Release storage nothing points to.",
		usage:   []string{"sennit reclaim [flags]"},
		flags: []flagHelp{
			{"repack", "--repack", ""},
			{"orphans", "--orphans", ""},
			{"unreadable", "--unreadable", ""},
			{"take-ownership", "--take-ownership", ""},
			{"release-all", "--release-all", ""},
		},
		vault: true, verbose: true,
		examples: []string{"sennit reclaim", "sennit reclaim --repack"},
	},
	"recover": {
		summary: "Rebuild this vault from its phrase and the indexer.",
		usage:   []string{"sennit recover [flags]"},
		flags:   []flagHelp{{"embed", "--embed", "(default true)"}},
		vault:   true, verbose: true,
		examples: []string{"sennit recover", "sennit recover --embed=false"},
	},
	"hydrate": {
		summary: "Restore this vault on a machine that never held it.",
		usage:   []string{"sennit hydrate [flags]"},
		flags: []flagHelp{
			{"depth", "--depth level", "(default metadata)"},
			{"quiet", "--quiet", ""},
		},
		vault: true, verbose: true,
		examples: []string{"sennit hydrate", "sennit hydrate --depth index"},
	},
}

// commandHelpLines lays out one command's screen from its own flags.
func commandHelpLines(name string, fs *flag.FlagSet) []ui.Line {
	spec := commandHelps[name]
	groups := []struct {
		title string
		flags []flagHelp
	}{{"FLAGS", spec.flags}}
	if spec.vault {
		groups = append(groups, struct {
			title string
			flags []flagHelp
		}{"VAULT FLAGS", vaultFlagHelp})
	}
	output := []flagHelp{colorFlagHelp}
	if spec.verbose {
		output = append(output, verboseFlagHelp)
	}
	groups = append(groups, struct {
		title string
		flags []flagHelp
	}{"OUTPUT FLAGS", output})

	longest := 0
	for _, group := range groups {
		for _, f := range group.flags {
			longest = max(longest, width.String(f.shown))
		}
	}
	column := 2 + longest + 4

	out := []ui.Line{text(spec.summary), {}, heading("USAGE")}
	for _, usage := range spec.usage {
		out = append(out, text("  "+usage))
	}
	for _, group := range groups {
		if len(group.flags) == 0 {
			continue
		}
		out = append(out, ui.Line{}, heading(group.title))
		for _, f := range group.flags {
			for i, line := range ui.Wrap(describe(fs, f), column, helpWidth-1) {
				if i == 0 {
					out = append(out, text(width.Pad("  "+f.shown, column)+line))
					continue
				}
				out = append(out, text(strings.Repeat(" ", column)+line))
			}
		}
	}
	out = append(out, ui.Line{}, heading("EXAMPLES"))
	for _, e := range spec.examples {
		out = append(out, example(e))
	}
	if spec.footer != "" {
		out = append(out, ui.Line{}, text(spec.footer))
	}
	return out
}

// describe is a flag's own usage string, capitalised, with the default that is
// worth stating after it.
func describe(fs *flag.FlagSet, f flagHelp) string {
	usage := ""
	if defined := fs.Lookup(f.flag); defined != nil {
		usage = defined.Usage
	}
	runes := []rune(usage)
	if len(runes) > 0 {
		usage = string(unicode.ToUpper(runes[0])) + string(runes[1:])
	}
	if f.defaultText != "" {
		usage += " " + f.defaultText
	}
	return usage
}

// printHelp writes a screen to stdout, which is where output a reader asked for
// belongs, and where a pager can take it.
func printHelp(s *session, lines []ui.Line) {
	for _, line := range lines {
		fmt.Fprintln(s.stdout, s.out.Render(line))
	}
}

// suggestions names the commands a mistyped word was probably meant to be.
//
// The rule is the one cobra uses, an edit distance of two or a prefix, because
// it is the rule people have met elsewhere and it is tight enough that a
// suggestion is worth reading.
func suggestions(typed string) []string {
	const maximumDistance = 2
	var out []string
	for _, group := range groups {
		for _, c := range group.commands {
			if distance(typed, c.name) <= maximumDistance ||
				strings.HasPrefix(strings.ToLower(c.name), strings.ToLower(typed)) {
				out = append(out, c.name)
			}
		}
	}
	return out
}

// distance is the Levenshtein distance between two words, ignoring case.
func distance(a, b string) int {
	first, second := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	previous := make([]int, len(second)+1)
	current := make([]int, len(second)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(first); i++ {
		current[0] = i
		for j := 1; j <= len(second); j++ {
			cost := 1
			if first[i-1] == second[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(second)]
}

// listOf writes command names as a person reads them: a, b or c.
func listOf(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "`" + name + "`"
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// unknownCommand is what an unrecognised word gets: the refusal, and the
// command it was probably meant to be. The whole help used to follow it, which
// buried the one line that mattered.
func unknownCommand(typed string) *problem {
	p := refuse(fmt.Sprintf("unknown command %q", typed)).exit(2)
	if found := suggestions(typed); len(found) > 0 {
		return p.try("did you mean " + listOf(found) + "?")
	}
	return p.try("`sennit --help` lists every command")
}
