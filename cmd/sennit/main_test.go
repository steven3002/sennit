package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/vault"
)

// runMain lets a test run this command as a program: the test binary runs
// itself again with this variable set, and is sennit for that run. It is the
// only way to hand the command a stdin the way a shell hands one over, and to
// see what is left on it afterwards.
const runMain = "SENNIT_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMain) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// program runs this command with stdin as its standard input, and reports what
// it wrote to stderr and the status it ended with.
//
// No phrase and no key are set, the vault directory is the test's own, and so
// is the configuration directory, so a profile stored by whoever runs the tests
// is never found, on any platform.
func program(t *testing.T, stdin *os.File, args ...string) (stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	config := t.TempDir()
	cmd.Env = append(os.Environ(), runMain+"=1", keys.PhraseEnv+"=", keys.AppKeyEnv+"=",
		vault.HomeEnv+"="+t.TempDir(), "APPDATA="+config, "XDG_CONFIG_HOME="+config, "HOME="+config)
	// A file is handed to the program as it stands, the way a shell hands over
	// its own stdin, rather than copied in by a goroutine that would read it
	// whatever the program did.
	cmd.Stdin = stdin
	var errs strings.Builder
	cmd.Stderr = &errs
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run sennit: %v (stderr %q)", err, errs.String())
		}
		code = exit.ExitCode()
	}
	return errs.String(), code
}

// A sennit command in a script leaves the rest of the script where it was.
//
// This is how it was found: on a server, `ssh host 'bash -s' < script`, with no
// phrase set and none stored. The first sennit command took the script's next
// line for its phrase and failed, and its read had swallowed every line after
// that one, so the script ended there. Here the program is started the way that
// script started it, on a pipe that still holds the lines after it, and every
// byte of them has to be there for the shell once it is done.
func TestAScriptKeepsItsLinesAfterASennitCommand(t *testing.T) {
	const rest = "sennit remember --context \"a script\" \"the next line\"\n" +
		"echo the line after it\n" +
		"echo and the last one\n"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	if _, err := w.WriteString(rest); err != nil {
		t.Fatalf("write the script: %v", err)
	}
	w.Close()

	stderr, code := program(t, r, "status", "-offline")
	if code != 1 {
		t.Errorf("sennit status with no phrase anywhere ended with %d, want 1", code)
	}
	for _, says := range []string{"no recovery phrase", "--phrase-stdin"} {
		if !strings.Contains(stderr, says) {
			t.Errorf("the refusal does not say %q:\n%s", says, stderr)
		}
	}
	left, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read what is left of the script: %v", err)
	}
	if string(left) != rest {
		t.Errorf("sennit took the script's lines: %d of %d bytes left, %q", len(left), len(rest), left)
	}
}

// Told to read the phrase from stdin, a sennit command takes the phrase's line
// and nothing after it, from a pipe or from a file, so whatever reads stdin next
// finds every line that followed. The line here is not a phrase, so the command
// stops as soon as it has read it, before any vault is opened.
func TestThePhraseStdinFlagTakesOnlyThePhraseLine(t *testing.T) {
	const line = "eleven words that are not a recovery phrase and open nothing\n"
	const rest = "echo the line after it\necho and the last one\n"
	for name, stdin := range map[string]func(*testing.T) *os.File{
		"a pipe": func(t *testing.T) *os.File {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			t.Cleanup(func() { r.Close() })
			if _, err := w.WriteString(line + rest); err != nil {
				t.Fatalf("write to the pipe: %v", err)
			}
			w.Close()
			return r
		},
		"a file": func(t *testing.T) *os.File {
			path := filepath.Join(t.TempDir(), "phrase-then-script.txt")
			if err := os.WriteFile(path, []byte(line+rest), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
			t.Cleanup(func() { f.Close() })
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := stdin(t)
			stderr, code := program(t, f, "status", "-offline", "--phrase-stdin")
			// Refused as a recovery phrase, so the line was read, and read as the
			// phrase.
			if code != 1 || !strings.Contains(stderr, keys.ErrPhrase.Error()+":") {
				t.Errorf("a line that is not a phrase ended with %d, want 1 and the phrase refused:\n%s", code, stderr)
			}
			left, err := io.ReadAll(f)
			if err != nil {
				t.Fatalf("read what is left of stdin: %v", err)
			}
			if string(left) != rest {
				t.Errorf("sennit took more than the phrase's line: %d of %d bytes left, %q", len(left), len(rest), left)
			}
		})
	}
}
