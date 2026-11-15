package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
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
