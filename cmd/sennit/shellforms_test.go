package main

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui/uitest"
)

// The line that loads the key is written for the shells a user of that system
// can be in, and never for one they cannot.
func TestTheKeyIsLoadedInEachShellsOwnWords(t *testing.T) {
	for _, c := range []struct {
		name, goos, shell, file string
		want                    []shellForm
	}{
		{
			name: "a POSIX system has one form and names no shell",
			goos: "linux", shell: "/bin/zsh", file: "sennit.key",
			want: []shellForm{{"", "export SENNIT_APP_KEY=$(cat sennit.key)"}},
		},
		{
			name: "a POSIX system with SHELL unset is still POSIX",
			goos: "darwin", shell: "", file: "sennit.key",
			want: []shellForm{{"", "export SENNIT_APP_KEY=$(cat sennit.key)"}},
		},
		{
			name: "Windows names both of its shells",
			goos: "windows", shell: "", file: "sennit.key",
			want: []shellForm{
				{"PowerShell", "$env:SENNIT_APP_KEY = Get-Content sennit.key"},
				{"cmd", "set /p SENNIT_APP_KEY=<sennit.key"},
			},
		},
		{
			name: "a POSIX shell on Windows is given its own form as well",
			goos: "windows", shell: `C:\Program Files\Git\usr\bin\bash.exe`, file: "sennit.key",
			want: []shellForm{
				{"PowerShell", "$env:SENNIT_APP_KEY = Get-Content sennit.key"},
				{"cmd", "set /p SENNIT_APP_KEY=<sennit.key"},
				{"bash", "export SENNIT_APP_KEY=$(cat sennit.key)"},
			},
		},
		{
			name: "a Windows path is left as typed where the shell takes it as typed",
			goos: "windows", shell: "/usr/bin/bash", file: `C:\Users\ada\sennit.key`,
			want: []shellForm{
				{"PowerShell", `$env:SENNIT_APP_KEY = Get-Content C:\Users\ada\sennit.key`},
				{"cmd", `set /p SENNIT_APP_KEY=<C:\Users\ada\sennit.key`},
				// Unquoted, bash would read each backslash as an escape.
				{"bash", `export SENNIT_APP_KEY=$(cat 'C:\Users\ada\sennit.key')`},
			},
		},
		{
			name: "a name with a space is quoted the way each shell quotes",
			goos: "windows", shell: "/usr/bin/bash", file: `my keys\sennit.key`,
			want: []shellForm{
				{"PowerShell", `$env:SENNIT_APP_KEY = Get-Content 'my keys\sennit.key'`},
				{"cmd", `set /p SENNIT_APP_KEY=<"my keys\sennit.key"`},
				{"bash", `export SENNIT_APP_KEY=$(cat 'my keys\sennit.key')`},
			},
		},
		{
			name: "a quote in a name survives each shell's quoting",
			goos: "linux", shell: "/bin/sh", file: "ada's key",
			want: []shellForm{{"", `export SENNIT_APP_KEY=$(cat 'ada'\''s key')`}},
		},
	} {
		got := loadFromFile(c.goos, c.shell, "SENNIT_APP_KEY", c.file)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d forms, want %d: %q", c.name, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s:\n got %q\nwant %q", c.name, got[i], c.want[i])
			}
		}
	}
	if got := powershellWord("ada's key"); got != "'ada''s key'" {
		t.Errorf("PowerShell writes a quote twice inside quotes: got %q", got)
	}
}

// Nothing connect says on Windows is written for a POSIX shell, unless a POSIX
// shell is what it was started from.
func TestWindowsIsNotAskedForPOSIXSyntax(t *testing.T) {
	hint := keyHint("windows", "", "sennit.key")
	said := hint.Text + "\n" + strings.Join(hint.Explanation, "\n")
	for _, posix := range []string{"export ", "$(", "cat "} {
		if strings.Contains(said, posix) {
			t.Errorf("the hint on Windows contains %q:\n%s", posix, said)
		}
	}
}

// The same holds for every help screen: on Windows none of them shows a line
// only a POSIX shell runs, and none prompts with a dollar sign.
func TestHelpOnWindowsIsNotWrittenForAPOSIXShell(t *testing.T) {
	s := terminal(t, 80)
	screens := map[string]string{"sennit --help": uitest.Strip(rendered(s, topLevelHelp("windows")))}
	for _, command := range []string{"init", "connect", "remember", "recall", "flush", "status", "reclaim", "recover", "hydrate"} {
		screens["sennit "+command+" --help"] = uitest.Strip(rendered(s, commandHelpLines("windows", command, flagsOf(t, command))))
	}
	for name, screen := range screens {
		for _, line := range strings.Split(screen, "\n") {
			if strings.HasPrefix(line, "  $ ") || strings.Contains(line, "export ") || strings.Contains(line, "$(") ||
				strings.Contains(line, "~/") {
				t.Errorf("%s on Windows shows a POSIX line: %q", name, line)
			}
		}
	}
	// The vault's default directory is under the profile folder there, which
	// is what os.UserHomeDir returns on Windows, whatever HOME a shell sets.
	for _, name := range []string{"sennit --help", "sennit status --help", "sennit recall --help"} {
		if !strings.Contains(screens[name], `(default %USERPROFILE%\.sennit)`) {
			t.Errorf("%s on Windows does not give the vault's default directory as Windows writes it:\n%s",
				name, screens[name])
		}
	}
	got := uitest.Strip(rendered(s, commandHelpLines("windows", "connect", flagsOf(t, "connect"))))
	if want := fixture(t, "help-connect-windows.txt"); got != want {
		t.Errorf("sennit connect --help on Windows:\n got %q\nwant %q", got, want)
	}
}

// Everywhere else the screens are the ones already approved, dollar sign and
// all, so a reader on Linux or macOS sees no change.
func TestHelpElsewhereKeepsItsPrompt(t *testing.T) {
	s := terminal(t, 80)
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		got := uitest.Strip(rendered(s, commandHelpLines(goos, "connect", flagsOf(t, "connect"))))
		if want := fixture(t, "help-connect.txt"); got != want {
			t.Errorf("sennit connect --help on %s:\n got %q\nwant %q", goos, got, want)
		}
	}
}
