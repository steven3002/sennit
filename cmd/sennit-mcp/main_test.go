package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runMain lets the test run this command as a program: the test binary re-execs
// itself with this variable set, which is the only way to see what the process
// actually writes to each stream.
const runMain = "SENNIT_MCP_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMain) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// server runs this command and reports what it wrote to each stream.
func server(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	// No phrase and no key: the server explains itself and serves anyway,
	// which is the path that prints the most, and it opens no vault, touches
	// no network and loads no model.
	cmd.Env = append(os.Environ(), runMain+"=1", "SENNIT_PHRASE=", "SENNIT_APP_KEY=")
	cmd.Stdin = strings.NewReader("")
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !asExit(err, &exit) {
			t.Fatalf("run the server: %v (stderr %q)", err, errs.String())
		}
		code = exit.ExitCode()
	}
	return out.String(), errs.String(), code
}

func asExit(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

// The server's diagnostics are read by a host and land in its log, where an
// escape sequence is noise at best. Nothing it writes is ever styled.
func TestTheServerWritesNothingStyled(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"given an argument it does not take", []string{"extra"}},
		{"started with nothing configured", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, _ := server(t, c.args...)
			if strings.ContainsRune(stderr, 0x1b) {
				t.Errorf("stderr carries an escape byte: %q", stderr)
			}
			if stderr == "" {
				t.Error("the server said nothing at all")
			}
			if strings.ContainsRune(stdout, 0x1b) {
				t.Errorf("stdout carries an escape byte: %q", stdout)
			}
		})
	}
}

// Nothing but protocol traffic goes to stdout. A client that has sent no
// request has been sent nothing.
func TestTheServerSaysNothingOnStdoutUnasked(t *testing.T) {
	stdout, _, _ := server(t)
	if stdout != "" {
		t.Errorf("stdout carried %q before any request", stdout)
	}
}

// The terminal code belongs to the command line alone. This is enforced by the
// package being internal to cmd/sennit, and checked here because the cost of
// getting it wrong is escape sequences in a host's log or, worse, in the
// protocol stream.
func TestTheServerDoesNotLinkTheTerminalCode(t *testing.T) {
	gotool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go tool is not on PATH here")
	}
	deps, err := exec.CommandContext(t.Context(), gotool, "list", "-deps", "github.com/steven3002/sennit/cmd/sennit-mcp").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	for _, line := range strings.Split(string(deps), "\n") {
		if strings.Contains(line, "cmd/sennit/internal") {
			t.Errorf("the server links %s", line)
		}
	}
}
