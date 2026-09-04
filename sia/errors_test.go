package sia

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

// An indexer failure must not carry the request URL into the message.
//
// The SDK's errors quote the whole URL, and the SDK's URLs carry the app key's
// signed authentication parameters, sc, ss and sv, so printing one puts
// credentials into a terminal's scrollback, a CI log or a screen recording.
func TestAnIndexerErrorLeaksNoCredentials(t *testing.T) {
	t.Parallel()

	const signed = "https://sia.storage/auth/check?sc=iaAoFopBkMaP5tAGDi03&ss=dXpviV1hR20hlu59QzFW&sv=1786087838"
	raw := fmt.Errorf("failed to check app auth: Get %q: %w", signed,
		&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")})

	shaped := asIndexerError("connect to the indexer", "https://sia.storage", raw)
	message := shaped.Error()

	for _, secret := range []string{"sc=", "ss=", "sv=", "iaAoFopBkMaP5tAGDi03", "dXpviV1hR20hlu59QzFW"} {
		if strings.Contains(message, secret) {
			t.Fatalf("the message carries %q:\n%s", secret, message)
		}
	}
	if !strings.Contains(message, "https://sia.storage") {
		t.Fatalf("the message does not say which indexer:\n%s", message)
	}
	if !errors.Is(shaped, ErrIndexerUnreachable) {
		t.Fatal("a refused connection is not reported as unreachable, so nothing can degrade on it")
	}
}

// A query string never survives into a reported address.
func TestRedactURLDropsTheQuery(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"https://sia.storage/auth/check?sc=a&ss=b": "https://sia.storage/auth/check",
		"https://sia.storage":                      "https://sia.storage",
		"":                                         "",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The body of a failed response is never the message. The hosted indexer sits
// behind a CDN that answers with an HTML page or a JSON document, and a user
// meeting a transient gateway error should not be handed either.
func TestAnIndexerErrorDoesNotQuoteTheResponseBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		raw       error
		status    int
		retryable bool
		says      string
	}{
		{
			name:      "a CDN gateway error",
			raw:       errors.New(`unexpected status code 502: {"title":"Error 502: Bad gateway","detail":"The origin web server returned an invalid or incomplete response to Cloudflare."}`),
			status:    502,
			retryable: true,
			says:      "transient",
		},
		{
			name:   "an address that is not an indexer",
			raw:    errors.New(`unexpected status code 404: <!doctype html><html lang="en"><head><title>Example Domain</title>`),
			status: 404,
			says:   "not an indexer",
		},
		{
			name:   "an installation that was never approved",
			raw:    errors.New("unexpected status code 403: forbidden"),
			status: 403,
			says:   "sennit connect",
		},
		{
			name:      "rate limiting",
			raw:       errors.New("unexpected status code 429: slow down"),
			status:    429,
			retryable: true,
			says:      "rate limiting",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shaped := asIndexerError("read the account", "https://sia.storage", tc.raw)

			var indexerErr *IndexerError
			if !errors.As(shaped, &indexerErr) {
				t.Fatalf("not shaped at all: %v", shaped)
			}
			if indexerErr.Status != tc.status {
				t.Errorf("status %d, want %d", indexerErr.Status, tc.status)
			}
			if indexerErr.Retryable() != tc.retryable {
				t.Errorf("Retryable() = %v, want %v", indexerErr.Retryable(), tc.retryable)
			}

			message := shaped.Error()
			if !strings.Contains(message, tc.says) {
				t.Errorf("the message does not say %q:\n%s", tc.says, message)
			}
			for _, body := range []string{"<!doctype", "<html", "Cloudflare", "{\""} {
				if strings.Contains(message, body) {
					t.Errorf("the message quotes the response body (%q):\n%s", body, message)
				}
			}
		})
	}
}

// A failure that is neither a transport error nor a status is passed through.
// Shaping it would replace something specific with something vague.
func TestAnUnrecognisedFailureIsNotReshaped(t *testing.T) {
	t.Parallel()

	raw := errors.New("slab 4cb9e9c0 not found")
	if got := asIndexerError("unpin", "https://sia.storage", raw); !errors.Is(got, raw) {
		t.Fatalf("an unrecognised error was reshaped into %v", got)
	}
	if asIndexerError("unpin", "https://sia.storage", nil) != nil {
		t.Fatal("nil became an error")
	}
}
