package sia

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ErrIndexerUnreachable reports that the indexer did not answer at all.
//
// It is distinguished from a refusal because the two need opposite responses:
// an unreachable indexer is a reason to wait and retry with the work still
// queued, and a refusal is a reason to change something.
var ErrIndexerUnreachable = errors.New("the indexer could not be reached")

// An IndexerError is a failure talking to the indexer, in a form a person can
// act on.
//
// The SDK's own errors are not that. They arrive with the full request URL,
// which carries the app key's signed authentication parameters, so printing one
// puts credentials in a terminal's scrollback, and with the response body
// inline, which for the hosted service is an HTML page or a CDN's JSON. A
// user's first encounter with a transient gateway error should not be a
// paragraph of markup.
type IndexerError struct {
	// Op is what was being attempted, in the user's terms.
	Op string
	// Indexer is the service, without any query string.
	Indexer string
	// Status is the HTTP status when there was a reply, and zero when there
	// was not.
	Status int
	// Advice is what to do about it.
	Advice string

	err error
}

func (e *IndexerError) Error() string {
	var b strings.Builder
	b.WriteString(e.Op)
	if e.Indexer != "" {
		fmt.Fprintf(&b, " (%s)", e.Indexer)
	}
	b.WriteString(": ")
	switch {
	case e.Status == 0:
		b.WriteString("the indexer did not answer")
	default:
		fmt.Fprintf(&b, "the indexer answered %d %s", e.Status, statusMeaning(e.Status))
	}
	if e.Advice != "" {
		b.WriteString(". ")
		b.WriteString(e.Advice)
	}
	return b.String()
}

func (e *IndexerError) Unwrap() error { return e.err }

// Retryable reports whether the same request is worth making again unchanged.
func (e *IndexerError) Retryable() bool {
	return e.Status == 0 || e.Status == 429 || e.Status >= 500
}

// statusURL matches the "unexpected status code NNN" the api client produces.
var statusURL = regexp.MustCompile(`status code (\d{3})`)

// asIndexerError shapes an SDK or api-client failure into something readable.
//
// It deliberately does not carry the underlying message forward into the text.
// Everything worth knowing is the operation, the service, the status and what
// to do; everything else in the original is either the signed URL or the body,
// and neither belongs in front of a user. The original is still reachable with
// errors.Unwrap for a debugger who wants it.
func asIndexerError(op, indexer string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	shaped := &IndexerError{Op: op, Indexer: redactURL(indexer), err: err}

	if match := statusURL.FindStringSubmatch(err.Error()); match != nil {
		shaped.Status, _ = strconv.Atoi(match[1])
	}
	switch {
	case shaped.Status == 0 && !isNetworkFailure(err):
		// Not a transport failure and not a status we recognise: shaping it
		// would hide something rather than clarify it.
		return err
	case shaped.Status == 0:
		shaped.err = fmt.Errorf("%w: %w", ErrIndexerUnreachable, err)
	}
	shaped.Advice = adviceFor(shaped.Status, indexer)
	return shaped
}

func isNetworkFailure(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func adviceFor(status int, indexer string) string {
	switch {
	case status == 0:
		return fmt.Sprintf("Check that %s is the right address and that this machine has a network. "+
			"Nothing was lost: records stay on this device until a run that reaches the indexer", indexer)
	case status == 401 || status == 403:
		return "This installation is not authorized. Run `sennit connect -out <file>` to approve it " +
			"and set SENNIT_APP_KEY to the key it writes"
	case status == 404:
		return fmt.Sprintf("That address answered but is not an indexer. Check %s, or leave it unset "+
			"to use the default", indexer)
	case status == 429:
		return "The indexer is rate limiting this account. Wait and retry; nothing was lost"
	case status >= 500:
		return "The indexer or the CDN in front of it is having trouble. This is transient and " +
			"retryable; nothing was lost, because a failed write leaves the records queued on this device"
	}
	return ""
}

func statusMeaning(status int) string {
	switch {
	case status == 401 || status == 403:
		return "(not authorized)"
	case status == 404:
		return "(no such endpoint)"
	case status == 429:
		return "(rate limited)"
	case status == 502 || status == 503 || status == 504:
		return "(gateway error)"
	case status >= 500:
		return "(server error)"
	}
	return ""
}

// redactURL strips any query string, because the SDK's request URLs carry the
// app key's signed authentication parameters.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery, parsed.Fragment = "", ""
	return parsed.String()
}
