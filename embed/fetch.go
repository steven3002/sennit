package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/internal/rename"
)

// EndpointEnv names another address to fetch models from, for a machine that
// reaches the hub through a mirror. What a mirror serves is held to the same
// pinned digests as what the hub serves, so it can be slow or absent and it
// cannot be wrong.
const EndpointEnv = "HF_ENDPOINT"

// hubEndpoint is where models are fetched from unless EndpointEnv says
// otherwise.
const hubEndpoint = "https://huggingface.co"

const (
	// downloadBudget bounds a whole fetch, however many files it has left.
	downloadBudget = 30 * time.Minute
	// fetchAttempts is how many times one file is asked for before the failure
	// is reported, and fetchPause is the wait between two of them. A transfer
	// of this size over a home connection does break, and breaking is the one
	// failure that asking again can fix.
	fetchAttempts = 3
	fetchPause    = 2 * time.Second
	// fetchStall is how long a transfer may go without a byte arriving before
	// it is taken for dead. Without it a connection that stops without closing
	// holds the command until the whole budget has run out.
	fetchStall = time.Minute

	// fetchLock is the file two fetches of one model exclude each other on, and
	// partSuffix marks a file that is still arriving. Both are kept beside the
	// model's directory and not in it, so that the directory holds the model's
	// files and nothing else.
	fetchLock  = "fetch.lock"
	partSuffix = ".part"
)

// ErrNotTheModel reports that what was served for one of a model's files is not
// that file.
var ErrNotTheModel = errors.New("the file served is not the one this model is pinned to")

// errStalled is why a transfer that stopped sending was abandoned, and
// errOverBudget is why a fetch that ran past its budget was.
var (
	errStalled    = errors.New("the transfer stopped sending")
	errOverBudget = fmt.Errorf("the download did not finish in the %s allowed for it", downloadBudget)
)

// A source is where a model's files are fetched from, and how patiently.
type source struct {
	// endpoint is the hub's address, with no slash at its end.
	endpoint string
	client   *http.Client
	// attempts is how many times one file is asked for before its failure is
	// the fetch's, and pause is the wait between two of them.
	attempts int
	pause    time.Duration
	// stall is how long a transfer may go without a byte arriving.
	stall time.Duration
}

// theHub is the source a vault fetches from.
func theHub() source {
	endpoint := strings.TrimRight(os.Getenv(EndpointEnv), "/")
	if endpoint == "" {
		endpoint = hubEndpoint
	}
	return source{
		endpoint: endpoint,
		client:   &http.Client{},
		attempts: fetchAttempts,
		pause:    fetchPause,
		stall:    fetchStall,
	}
}

// Fetch downloads the model under root unless it is already there.
//
// Each file is asked for at the model's pinned revision, written under a name
// of its own, checked against the size and the digest pinned here, and only
// then renamed to the name the model is loaded by. So a file is under its name
// whole or not at all: a fetch that is cancelled, or cut off, or killed, leaves
// nothing a later run could take for the model, and the next one starts the
// file again.
//
// It keeps no cache and makes no links. The files go straight into the model
// directory, which needs nothing of the account running it but the right to
// write there.
//
// A file already there at its pinned size is left alone, so a directory that
// holds some of the model is completed and one that holds all of it is not
// touched.
func (m Model) Fetch(ctx context.Context, root string) (string, error) {
	return m.fetch(ctx, root, theHub())
}

func (m Model) fetch(ctx context.Context, root string, from source) (string, error) {
	dir := m.Dir(root)
	if m.Present(root) {
		return dir, nil
	}
	if len(m.Files) == 0 || m.Revision == "" {
		return "", fmt.Errorf("model %s names no files to fetch, and %s does not hold it", m.Name, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("prepare model directory %s: %w", dir, err)
	}

	ctx, cancel := context.WithTimeoutCause(ctx, downloadBudget, errOverBudget)
	defer cancel()

	// Two processes started together both find the model missing: a command
	// and an agent's server on a first run, or two servers. One fetches and the
	// other waits, then finds the files there and fetches nothing.
	staging := filepath.Dir(dir)
	release, err := alone(ctx, filepath.Join(staging, fetchLock))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", m.Repo, err)
	}
	defer release()

	for _, file := range m.Files {
		if m.holds(root, file) {
			continue
		}
		part := filepath.Join(staging, file.name()+partSuffix)
		if err := from.fetch(ctx, m, file, part, filepath.Join(dir, file.name())); err != nil {
			// A budget that ran out reaches here as a deadline on whatever the
			// transfer was doing at the time, which names nothing.
			if errors.Is(context.Cause(ctx), errOverBudget) {
				err = errOverBudget
			}
			return "", fmt.Errorf("download %s: fetch %s: %w", m.Repo, file.Path, err)
		}
	}
	return dir, nil
}

// alone takes the lock at path, waiting for any other holder, and returns the
// function that lets it go.
//
// The lock itself cannot be waited for with a deadline, so the wait runs on a
// goroutine of its own. A caller that is cancelled stops waiting at once, and
// the goroutine gives the lock straight back if it is ever granted.
func alone(ctx context.Context, path string) (release func(), err error) {
	lock, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	taken := make(chan error, 1)
	go func() { taken <- lock.Lock() }()
	select {
	case err := <-taken:
		if err != nil {
			lock.Close()
			return nil, err
		}
		return func() {
			lock.Unlock()
			lock.Close()
		}, nil
	case <-ctx.Done():
		go func() {
			if <-taken == nil {
				lock.Unlock()
			}
			lock.Close()
		}()
		return nil, fmt.Errorf("wait for another fetch of this model to finish: %w", ctx.Err())
	}
}

// fetch brings one file down to part and renames it to dest once it has been
// checked.
//
// Only a transfer that broke is asked for again. A file that arrived whole and
// is not the pinned one will not be a different file the second time, and
// neither will one the hub says it does not have.
func (s source) fetch(ctx context.Context, m Model, file File, part, dest string) error {
	url := s.endpoint + "/" + m.Repo + "/resolve/" + m.Revision + "/" + file.Path
	for attempt := 1; ; attempt++ {
		err := s.download(ctx, url, file, part)
		if err == nil {
			break
		}
		var broken *brokenTransfer
		if attempt >= s.attempts || !errors.As(err, &broken) || ctx.Err() != nil {
			os.Remove(part)
			return err
		}
		select {
		case <-ctx.Done():
			os.Remove(part)
			return ctx.Err()
		case <-time.After(s.pause):
		}
	}
	if err := rename.Over(part, dest); err != nil {
		os.Remove(part)
		return fmt.Errorf("put it in place: %w", err)
	}
	return nil
}

// A brokenTransfer is a failure of the connection rather than of the file: the
// hub could not be reached, answered that it was busy, or stopped part way.
type brokenTransfer struct{ err error }

func (b *brokenTransfer) Error() string { return b.err.Error() }
func (b *brokenTransfer) Unwrap() error { return b.err }

// download writes what url serves to part, and reports anything short of the
// whole pinned file as an error.
func (s source) download(ctx context.Context, url string, file File, part string) error {
	ctx, abandon := context.WithCancelCause(ctx)
	defer abandon(nil)
	stalled := time.AfterFunc(s.stall, func() { abandon(errStalled) })
	defer stalled.Stop()
	// broke says what a failure of the connection was. The request's own error
	// names the context when the transfer was abandoned for stalling, which is
	// true and says nothing, so the reason it was abandoned is reported.
	broke := func(err error) error {
		if errors.Is(context.Cause(ctx), errStalled) {
			return &brokenTransfer{fmt.Errorf("%s: %w: no data for %s", url, errStalled, s.stall)}
		}
		if ctx.Err() != nil {
			return err
		}
		return &brokenTransfer{err}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return broke(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s answered %s", url, response.Status)
		if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode == http.StatusRequestTimeout {
			return &brokenTransfer{err}
		}
		return err
	}
	// A length that is not the file's is refused before any of it is read, so
	// that a hundred megabytes of something else are not downloaded first.
	if length := response.ContentLength; length >= 0 && length != file.Size {
		return fmt.Errorf("%w: %s is %d bytes there, and the file is %d", ErrNotTheModel, url, length, file.Size)
	}

	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	digest := sha256.New()
	body := &watched{from: response.Body, timer: stalled, every: s.stall}
	// One byte more than the file is read at most, which is enough to know that
	// what is being served is longer than it.
	written, err := io.Copy(io.MultiWriter(out, digest), io.LimitReader(body, file.Size+1))
	if err != nil {
		out.Close()
		if body.failed != nil {
			return broke(err)
		}
		return err
	}
	switch {
	case written < file.Size:
		out.Close()
		return fmt.Errorf("%w: %s sent %d bytes, and the file is %d", ErrNotTheModel, url, written, file.Size)
	case written > file.Size:
		out.Close()
		return fmt.Errorf("%w: %s sent more than the file's %d bytes", ErrNotTheModel, url, file.Size)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != file.SHA256 {
		out.Close()
		return fmt.Errorf("%w: the SHA-256 of what %s sent is %s, and the file's is %s",
			ErrNotTheModel, url, got, file.SHA256)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// watched is a transfer's body, read with a timer that is pushed back every
// time bytes arrive, and with the error the connection ended on kept apart from
// one the disk raised.
type watched struct {
	from   io.Reader
	timer  *time.Timer
	every  time.Duration
	failed error
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.from.Read(p)
	if n > 0 {
		w.timer.Reset(w.every)
	}
	if err != nil && err != io.EOF {
		w.failed = err
	}
	return n, err
}
