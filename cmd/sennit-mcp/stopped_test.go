package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
)

// A host that stops waiting for the server to start, or a signal, cancels the
// open, which a rebuild of the search index can make minutes long. The server
// is on its way out then, so its log says it stopped. It said the open had
// failed and that it was serving anyway, which it was not.
func TestAServerStoppedBeforeItServesSaysItStopped(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	stop()
	var log strings.Builder
	notOpened(&log, ctx, fmt.Errorf("embed 1 text(s): %w", ctx.Err()))
	if got := log.String(); got != "sennit-mcp: stopped before serving\n" {
		t.Errorf("a server stopped before it served said %q", got)
	}

	// A vault that could not open for a reason is still explained, and the
	// server still serves, to say so to every call.
	log.Reset()
	notOpened(&log, context.Background(), keys.ErrNoPhrase)
	for _, says := range []string{"sennit-mcp: ", "Set " + keys.PhraseEnv, "Serving anyway"} {
		if !strings.Contains(log.String(), says) {
			t.Errorf("a vault with no phrase is explained without %q: %q", says, log.String())
		}
	}
}
