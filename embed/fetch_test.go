package embed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Nothing here touches the network. A server on this machine stands in for the
// hub, and serves a model small enough to make up on the spot.

// fetcherEnv makes this test binary fetch the test model from the hub it names
// into the directory rootEnv names, instead of running tests. Two processes
// fetching at once is a claim about two processes, so the other one is real.
const (
	fetcherEnv = "SENNIT_TEST_FETCH_FROM"
	rootEnv    = "SENNIT_TEST_FETCH_INTO"
)

func TestMain(m *testing.M) {
	if endpoint := os.Getenv(fetcherEnv); endpoint != "" {
		model, _ := tinyModel()
		from := source{endpoint: endpoint, client: &http.Client{}, attempts: 1, pause: time.Millisecond, stall: time.Minute}
		if _, err := model.fetch(context.Background(), os.Getenv(rootEnv), from); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// tinyModel is a model of four files, pinned to what they really hold, with the
// model file a directory down in the repo as the real one's is. It returns the
// files' contents by their paths in the repo.
func tinyModel() (Model, map[string][]byte) {
	weights := make([]byte, 512<<10)
	for i := range weights {
		weights[i] = byte(i*31 + i>>8)
	}
	contents := map[string][]byte{
		"config.json":     []byte(`{"max_position_embeddings": 512}`),
		"vocab.txt":       []byte("[PAD]\n[UNK]\ntide\ngauge\n"),
		"tokenizer.json":  []byte(`{"version": "1.0"}`),
		"onnx/model.onnx": weights,
	}
	model := Model{
		Name:     "tiny-fp32",
		Repo:     "tidepool/tiny",
		Revision: "0123456789abcdef0123456789abcdef01234567",
		OnnxFile: "onnx/model.onnx",
		Dim:      8,
	}
	for _, path := range []string{"config.json", "vocab.txt", "tokenizer.json", "onnx/model.onnx"} {
		sum := sha256.Sum256(contents[path])
		model.Files = append(model.Files, File{Path: path, Size: int64(len(contents[path])), SHA256: hex.EncodeToString(sum[:])})
	}
	return model, contents
}

// A hub stands in for the model hub. It serves a model's files at the paths the
// real one serves them at, counts what it was asked for, and lets a test answer
// for one path itself.
type hub struct {
	*httptest.Server
	mu    sync.Mutex
	asked map[string]int
	// answer, when set, is tried first. See answers.
	answer func(w http.ResponseWriter, r *http.Request, path string, asked int) bool
}

func newHub(t *testing.T, model Model, contents map[string][]byte) *hub {
	t.Helper()
	h := &hub{asked: map[string]int{}}
	prefix := "/" + model.Repo + "/resolve/" + model.Revision + "/"
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.mu.Lock()
		h.asked[path]++
		asked, answer := h.asked[path], h.answer
		h.mu.Unlock()
		if answer != nil && answer(w, r, path, asked) {
			return
		}
		body, ok := contents[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	t.Cleanup(h.Close)
	return h
}

// answers has the hub try answer first for every request from now on. It
// reports whether it answered.
func (h *hub) answers(answer func(w http.ResponseWriter, r *http.Request, path string, asked int) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answer = answer
}

// requests is how many times the hub was asked for path.
func (h *hub) requests(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.asked[path]
}

// total is how many requests the hub answered for the model's files.
func (h *hub) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int
	for _, count := range h.asked {
		n += count
	}
	return n
}

// source is this hub as a fetch reads from it, with waits short enough for a
// test.
func (h *hub) source() source {
	return source{endpoint: h.URL, client: h.Client(), attempts: 3, pause: time.Millisecond, stall: 10 * time.Second}
}

// holdsExactly fails the test unless the model directory under root holds each
// of the model's files under its flat name with the bytes given, nothing else,
// and nothing beside it that is still arriving.
func holdsExactly(t *testing.T, model Model, root string, contents map[string][]byte) {
	t.Helper()
	dir := model.Dir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the model directory: %v", err)
	}
	if len(entries) != len(model.Files) {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("the model directory holds %v, want the model's %d files and nothing else", names, len(model.Files))
	}
	for _, file := range model.Files {
		got, err := os.ReadFile(filepath.Join(dir, file.name()))
		if err != nil {
			t.Errorf("%s is not in the model directory: %v", file.name(), err)
			continue
		}
		if !bytes.Equal(got, contents[file.Path]) {
			t.Errorf("%s holds %d bytes that are not the file's %d", file.name(), len(got), len(contents[file.Path]))
		}
	}
	nothingArriving(t, model, root)
}

// nothingArriving fails the test if a file a fetch was still writing is left
// beside the model directory.
func nothingArriving(t *testing.T, model Model, root string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(filepath.Dir(model.Dir(root)), "*"+partSuffix))
	if err != nil {
		t.Fatalf("look for files still arriving: %v", err)
	}
	if len(left) > 0 {
		t.Errorf("a fetch that ended left %v behind", left)
	}
}

// absent fails the test if the model directory under root holds the file at
// path under its name, whole or in part.
func absent(t *testing.T, model Model, root, path string) {
	t.Helper()
	name := filepath.Join(model.Dir(root), File{Path: path}.name())
	if info, err := os.Stat(name); err == nil {
		t.Errorf("%s is in the model directory, %d bytes of it, after a fetch that did not get it", name, info.Size())
	}
}

// place puts bytes in the model directory under root, under the name the file
// at path is loaded by, the way a copy made by hand does.
func place(t *testing.T, model Model, root, path string, content []byte) {
	t.Helper()
	dir := model.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make the model directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, File{Path: path}.name()), content, 0o644); err != nil {
		t.Fatalf("place %s: %v", path, err)
	}
}

// A first run has an empty model directory. The files land in it under their
// own names with the repo's subdirectory flattened away, which is the layout
// the model is loaded from, and each is asked for once, at the pinned revision.
func TestAFetchPutsTheModelsFilesInItsDirectoryUnderTheirOwnNames(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	root := t.TempDir()

	if model.Present(root) {
		t.Fatal("an empty directory holds the model")
	}
	dir, err := model.fetch(t.Context(), root, hub.source())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if want := filepath.Join(root, "tiny-fp32", "tidepool_tiny"); dir != want {
		t.Errorf("the model was fetched into %s, want %s", dir, want)
	}
	holdsExactly(t, model, root, contents)
	if !model.Present(root) {
		t.Error("the model is not present after a fetch that succeeded")
	}
	for _, file := range model.Files {
		if got := hub.requests(file.Path); got != 1 {
			t.Errorf("the hub was asked for %s %d time(s), want once", file.Path, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "onnx")); err == nil {
		t.Error("the repo's onnx directory was kept, and the model is loaded from a flat one")
	}
}

// The hub answers a file's address with a redirect, to a cache on its own host
// for a small file and to another host for the model file. The fetch follows
// it and checks what it is finally given.
func TestAFetchFollowsTheRedirectTheHubAnswersWith(t *testing.T) {
	model, contents := tinyModel()
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(contents[strings.TrimPrefix(r.URL.Path, "/blob/")])
	}))
	t.Cleanup(elsewhere.Close)
	hub := newHub(t, model, contents)
	hub.answers(func(w http.ResponseWriter, r *http.Request, path string, _ int) bool {
		if path == "onnx/model.onnx" {
			http.Redirect(w, r, elsewhere.URL+"/blob/"+path, http.StatusFound)
			return true
		}
		return false
	})
	root := t.TempDir()

	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	holdsExactly(t, model, root, contents)
}

// A directory can hold part of a model: an earlier fetch was stopped, or some
// of the files were copied in by hand. The model loads without some of its
// files and embeds differently for it, so the directory is completed, and what
// it already held is not fetched again or written over.
func TestADirectoryHoldingSomeOfTheFilesIsCompleted(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	root := t.TempDir()
	// Bytes of the right length that are not the file's show whether the file
	// was left as it was found.
	mine := bytes.Repeat([]byte{'m'}, len(contents["vocab.txt"]))
	place(t, model, root, "vocab.txt", mine)
	place(t, model, root, "onnx/model.onnx", contents["onnx/model.onnx"])

	if model.Present(root) {
		t.Fatal("a directory holding two of the four files holds the model")
	}
	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	contents["vocab.txt"] = mine
	holdsExactly(t, model, root, contents)
	for path, want := range map[string]int{"config.json": 1, "tokenizer.json": 1, "vocab.txt": 0, "onnx/model.onnx": 0} {
		if got := hub.requests(path); got != want {
			t.Errorf("the hub was asked for %s %d time(s), want %d", path, got, want)
		}
	}
}

// A directory that already holds the files is used as it is, whoever put them
// there: an earlier version's fetch, or a copy made by hand on a machine that
// cannot reach the hub. Nothing is asked of the hub and nothing is written, so
// a model directory this account may only read still works.
func TestADirectoryHoldingEveryFileIsLeftAlone(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	root := t.TempDir()
	for path, content := range contents {
		place(t, model, root, path, content)
	}

	if !model.Present(root) {
		t.Fatal("a directory holding every file at its size does not hold the model")
	}
	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := hub.total(); got != 0 {
		t.Errorf("the hub was asked for %d file(s) the directory already held", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(model.Dir(root)), fetchLock)); err == nil {
		t.Error("a fetch with nothing to do wrote a lock file")
	}
	holdsExactly(t, model, root, contents)
}

// A copy that was stopped part way leaves a file under its own name and cut
// short. It is not the file, so the model is not present, and the fetch puts
// the whole one in its place.
func TestAFileCutShortIsFetchedAgain(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	root := t.TempDir()
	for path, content := range contents {
		place(t, model, root, path, content)
	}
	whole := contents["onnx/model.onnx"]
	place(t, model, root, "onnx/model.onnx", whole[:len(whole)/3])

	if model.Present(root) {
		t.Fatal("a model file cut to a third of its size passes for the model")
	}
	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	holdsExactly(t, model, root, contents)
	if got := hub.total(); got != 1 {
		t.Errorf("the hub was asked for %d file(s), want the one that was cut short", got)
	}
}

// The model's name is stored beside every vector, so a file that is not the
// pinned one must never be loaded under it. Whatever a hub or a mirror sends
// that is not the file is refused and said to be, by the file's name and by
// what was wrong with it, and it is not asked for a second time: it would be
// the same wrong file.
func TestWhatIsNotTheModelsFileIsRefusedAndNamed(t *testing.T) {
	model, contents := tinyModel()
	whole := contents["onnx/model.onnx"]
	other := bytes.Clone(whole)
	other[len(other)/2] ^= 0xff
	otherSum := sha256.Sum256(other)

	chunked := func(body []byte) func(http.ResponseWriter, *http.Request, string, int) bool {
		return func(w http.ResponseWriter, _ *http.Request, path string, _ int) bool {
			if path != "onnx/model.onnx" {
				return false
			}
			// Flushing before the body is written leaves the length unsaid.
			w.(http.Flusher).Flush()
			w.Write(body)
			return true
		}
	}
	sized := func(body []byte) func(http.ResponseWriter, *http.Request, string, int) bool {
		return func(w http.ResponseWriter, _ *http.Request, path string, _ int) bool {
			if path != "onnx/model.onnx" {
				return false
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body)
			return true
		}
	}
	for name, c := range map[string]struct {
		answer func(http.ResponseWriter, *http.Request, string, int) bool
		says   []string
	}{
		"the same length and other bytes": {sized(other), []string{
			"SHA-256", hex.EncodeToString(otherSum[:]), model.Files[3].SHA256}},
		"another length":                 {sized(whole[:100]), []string{"100 bytes", fmt.Sprint(len(whole))}},
		"shorter, with no length given":  {chunked(whole[:100]), []string{"100 bytes", fmt.Sprint(len(whole))}},
		"longer, with no length given":   {chunked(append(bytes.Clone(whole), 'x')), []string{"more than", fmt.Sprint(len(whole))}},
		"an empty file, for a whole one": {sized(nil), []string{"0 bytes", fmt.Sprint(len(whole))}},
	} {
		t.Run(name, func(t *testing.T) {
			hub := newHub(t, model, contents)
			hub.answers(c.answer)
			root := t.TempDir()

			_, err := model.fetch(t.Context(), root, hub.source())
			if !errors.Is(err, ErrNotTheModel) {
				t.Fatalf("the fetch ended with %v, want a refusal of a file that is not the model's", err)
			}
			for _, want := range append(c.says, "onnx/model.onnx", model.Repo) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q:\n%v", want, err)
				}
			}
			if got := hub.requests("onnx/model.onnx"); got != 1 {
				t.Errorf("the hub was asked for the refused file %d time(s), want once", got)
			}
			absent(t, model, root, "onnx/model.onnx")
			nothingArriving(t, model, root)
			if model.Present(root) {
				t.Error("the model is present after its model file was refused")
			}
		})
	}
}

// A fetch that is cancelled part way through a file, by an interrupt at the
// terminal, leaves no file under the model's names that is not whole. The
// error is the cancellation, which is what lets a command report an interrupt
// as one. The next fetch then completes the model.
func TestACancelledFetchLeavesNothingThatPassesForTheModel(t *testing.T) {
	model, contents := tinyModel()
	whole := contents["onnx/model.onnx"]
	hub := newHub(t, model, contents)
	arrived, release := make(chan struct{}), make(chan struct{})
	hub.answers(func(w http.ResponseWriter, r *http.Request, path string, asked int) bool {
		if path != "onnx/model.onnx" || asked > 1 {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(whole)))
		w.Write(whole[:len(whole)/2])
		w.(http.Flusher).Flush()
		close(arrived)
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return true
	})
	t.Cleanup(func() { close(release) })
	root := t.TempDir()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := model.fetch(ctx, root, hub.source())
		done <- err
	}()
	<-arrived
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled fetch ended with %v, want the cancellation", err)
	}
	absent(t, model, root, "onnx/model.onnx")
	nothingArriving(t, model, root)
	if model.Present(root) {
		t.Fatal("the model is present after a fetch that was cancelled half way through the model file")
	}

	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("the fetch after the cancelled one: %v", err)
	}
	holdsExactly(t, model, root, contents)
}

// A process killed part way through a file has no chance to tidy up. What it
// had written is under a name of its own, not the model's, so nothing passes
// for the model, and the next fetch starts that file again from its first
// byte.
func TestAFetchKilledPartWayLeavesNothingUnderTheModelsNames(t *testing.T) {
	model, contents := tinyModel()
	whole := contents["onnx/model.onnx"]
	hub := newHub(t, model, contents)
	arrived, release := make(chan struct{}), make(chan struct{})
	hub.answers(func(w http.ResponseWriter, r *http.Request, path string, asked int) bool {
		if path != "onnx/model.onnx" || asked > 1 {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(whole)))
		w.Write(whole[:len(whole)/2])
		w.(http.Flusher).Flush()
		close(arrived)
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return true
	})
	t.Cleanup(func() { close(release) })
	root := t.TempDir()

	other := anotherProcessFetches(t, hub, root)
	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("the other process never asked for the model file")
	}
	// Half the file is on its way to the other process's disk. Give it a
	// moment to land, so that what is left behind is the worst case.
	part := filepath.Join(filepath.Dir(model.Dir(root)), "model.onnx"+partSuffix)
	for range 200 {
		if info, err := os.Stat(part); err == nil && info.Size() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := other.Process.Kill(); err != nil {
		t.Fatalf("kill the other process: %v", err)
	}
	other.Wait()

	absent(t, model, root, "onnx/model.onnx")
	if model.Present(root) {
		t.Fatal("the model is present after the process fetching it was killed half way through the model file")
	}
	if info, err := os.Stat(part); err != nil || info.Size() == 0 {
		t.Fatalf("the killed process left no part of the file behind (%v), so this checks nothing about one", err)
	}

	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("the fetch after the killed one: %v", err)
	}
	holdsExactly(t, model, root, contents)
}

// anotherProcessFetches starts this test binary again as a process that
// fetches the test model from the hub into root, and returns once it is
// running.
func anotherProcessFetches(t *testing.T, hub *hub, root string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(), fetcherEnv+"="+hub.URL, rootEnv+"="+root)
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &said, &said
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the other process: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() && said.Len() > 0 {
			t.Logf("the other process said:\n%s", said.String())
		}
	})
	return cmd
}

// A command and an agent's server started together on a first run both find
// the model missing, and so do two servers. They are separate processes with
// one model directory between them. Each ends with the whole model, and the
// files are fetched once between them: the others wait and find them there.
func TestProcessesFetchingAtOnceLeaveOneWholeModel(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	hub.answers(func(http.ResponseWriter, *http.Request, string, int) bool {
		// Slow enough that the others arrive while the first is still fetching.
		time.Sleep(150 * time.Millisecond)
		return false
	})
	root := t.TempDir()

	const processes = 3
	var others []*exec.Cmd
	for range processes {
		others = append(others, anotherProcessFetches(t, hub, root))
	}
	for i, other := range others {
		if err := other.Wait(); err != nil {
			t.Errorf("process %d failed to fetch the model: %v", i+1, err)
		}
	}
	holdsExactly(t, model, root, contents)
	for _, file := range model.Files {
		if got := hub.requests(file.Path); got != 1 {
			t.Errorf("%d processes asked the hub for %s %d time(s) between them, want once", processes, file.Path, got)
		}
	}
}

// Two fetches in one process exclude each other as two processes do, which is
// what a library user opening two vaults at once relies on.
func TestFetchesAtOnceInOneProcessLeaveOneWholeModel(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	hub.answers(func(http.ResponseWriter, *http.Request, string, int) bool {
		time.Sleep(50 * time.Millisecond)
		return false
	})
	root := t.TempDir()

	const fetches = 4
	failed := make(chan error, fetches)
	for range fetches {
		go func() {
			_, err := model.fetch(t.Context(), root, hub.source())
			failed <- err
		}()
	}
	for range fetches {
		if err := <-failed; err != nil {
			t.Errorf("fetch: %v", err)
		}
	}
	holdsExactly(t, model, root, contents)
	if got, want := hub.total(), len(model.Files); got != want {
		t.Errorf("%d fetches asked the hub for %d file(s) between them, want each of the %d once", fetches, got, want)
	}
}

// A fetch that is waiting for another to finish is still a command somebody
// may interrupt, and it stops waiting when they do.
func TestAFetchWaitingForAnotherStopsWhenItIsCancelled(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	root := t.TempDir()
	staging := filepath.Dir(model.Dir(root))
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("make the model's directory: %v", err)
	}
	release, err := alone(t.Context(), filepath.Join(staging, fetchLock))
	if err != nil {
		t.Fatalf("take the fetch lock: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := model.fetch(ctx, root, hub.source())
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a fetch went ahead while another held the model (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the waiting fetch ended with %v, want the cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting fetch did not stop when it was cancelled")
	}
	if got := hub.total(); got != 0 {
		t.Errorf("the waiting fetch asked the hub for %d file(s)", got)
	}
}

// A connection that drops part way through a file is the failure asking again
// fixes, so the file is asked for again, and what arrived the first time is not
// kept in front of what arrives the second.
func TestATransferThatBreaksIsAskedForAgain(t *testing.T) {
	model, contents := tinyModel()
	whole := contents["onnx/model.onnx"]
	hub := newHub(t, model, contents)
	hub.answers(func(w http.ResponseWriter, _ *http.Request, path string, asked int) bool {
		if path != "onnx/model.onnx" || asked > 1 {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(whole)))
		w.Write(whole[:len(whole)/2])
		w.(http.Flusher).Flush()
		// The documented way for a handler to drop its connection.
		panic(http.ErrAbortHandler)
	})
	root := t.TempDir()

	if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	holdsExactly(t, model, root, contents)
	if got := hub.requests("onnx/model.onnx"); got != 2 {
		t.Errorf("the hub was asked for the model file %d time(s), want twice: once broken, once whole", got)
	}
}

// When a file cannot be fetched the error is the reason, with the file's name
// in it. A count of the attempts made says nothing about what to fix.
func TestAFailedFetchNamesTheFileAndWhatWentWrong(t *testing.T) {
	model, contents := tinyModel()
	whole := contents["onnx/model.onnx"]

	t.Run("the hub does not have the file", func(t *testing.T) {
		hub := newHub(t, model, map[string][]byte{"config.json": contents["config.json"]})
		root := t.TempDir()
		_, err := model.fetch(t.Context(), root, hub.source())
		if err == nil {
			t.Fatal("a fetch of a file the hub does not have succeeded")
		}
		for _, want := range []string{"vocab.txt", "404", hub.URL + "/tidepool/tiny/resolve/" + model.Revision + "/vocab.txt"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not say %q:\n%v", want, err)
			}
		}
		if got := hub.requests("vocab.txt"); got != 1 {
			t.Errorf("the hub was asked %d time(s) for a file it had said it does not have, want once", got)
		}
		if model.Present(root) {
			t.Error("the model is present with three of its files missing")
		}
	})

	t.Run("the connection keeps dropping", func(t *testing.T) {
		hub := newHub(t, model, contents)
		hub.answers(func(w http.ResponseWriter, _ *http.Request, path string, _ int) bool {
			if path != "onnx/model.onnx" {
				return false
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(whole)))
			w.Write(whole[:len(whole)/2])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		})
		root := t.TempDir()
		_, err := model.fetch(t.Context(), root, hub.source())
		if err == nil {
			t.Fatal("a fetch whose connection dropped every time succeeded")
		}
		if !strings.Contains(err.Error(), "onnx/model.onnx") || !strings.Contains(err.Error(), "EOF") {
			t.Errorf("the error does not name the file and the broken transfer:\n%v", err)
		}
		if strings.Contains(err.Error(), "attempt") {
			t.Errorf("the error counts attempts instead of saying what went wrong:\n%v", err)
		}
		if got := hub.requests("onnx/model.onnx"); got != 3 {
			t.Errorf("the hub was asked for the model file %d time(s), want the three a broken transfer is given", got)
		}
		absent(t, model, root, "onnx/model.onnx")
		nothingArriving(t, model, root)
	})

	t.Run("the hub cannot be reached", func(t *testing.T) {
		hub := newHub(t, model, contents)
		from := hub.source()
		hub.Close()
		root := t.TempDir()
		_, err := model.fetch(t.Context(), root, from)
		if err == nil {
			t.Fatal("a fetch from a hub that is not there succeeded")
		}
		if !strings.Contains(err.Error(), "config.json") || !strings.Contains(err.Error(), from.endpoint) {
			t.Errorf("the error does not name the file and the address that could not be reached:\n%v", err)
		}
	})

	t.Run("the hub is busy", func(t *testing.T) {
		hub := newHub(t, model, contents)
		hub.answers(func(w http.ResponseWriter, _ *http.Request, path string, asked int) bool {
			if path != "config.json" || asked > 2 {
				return false
			}
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return true
		})
		root := t.TempDir()
		if _, err := model.fetch(t.Context(), root, hub.source()); err != nil {
			t.Fatalf("a hub that was busy twice and then answered: %v", err)
		}
		holdsExactly(t, model, root, contents)
	})
}

// A connection can stop sending without closing. Left alone it would hold the
// command until the whole budget for the download had run out, so a transfer
// that goes quiet is abandoned, and the error says that is what happened.
func TestATransferThatGoesQuietIsAbandoned(t *testing.T) {
	model, contents := tinyModel()
	whole := contents["onnx/model.onnx"]
	hub := newHub(t, model, contents)
	release := make(chan struct{})
	hub.answers(func(w http.ResponseWriter, r *http.Request, path string, _ int) bool {
		if path != "onnx/model.onnx" {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(whole)))
		w.Write(whole[:len(whole)/2])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return true
	})
	t.Cleanup(func() { close(release) })
	root := t.TempDir()
	from := hub.source()
	from.attempts, from.stall = 1, 100*time.Millisecond

	started := time.Now()
	_, err := model.fetch(t.Context(), root, from)
	if !errors.Is(err, errStalled) {
		t.Fatalf("the fetch ended with %v after %s, want the transfer abandoned for going quiet", err, time.Since(started))
	}
	if !strings.Contains(err.Error(), "onnx/model.onnx") {
		t.Errorf("the error does not name the file:\n%v", err)
	}
	absent(t, model, root, "onnx/model.onnx")
	nothingArriving(t, model, root)
}

// A model that pins no files cannot be checked, so it is not fetched. It is
// still loaded from a directory that holds it, which is all such a model could
// ever be.
func TestAModelThatPinsNoFilesIsNotFetched(t *testing.T) {
	model := Model{Name: "unpinned", Repo: "tidepool/unpinned", OnnxFile: "onnx/model.onnx", Dim: 8}
	root := t.TempDir()

	_, err := model.Fetch(t.Context(), root)
	if err == nil || !strings.Contains(err.Error(), "names no files to fetch") {
		t.Fatalf("fetching a model that pins nothing ended with %v", err)
	}
	place(t, model, root, "onnx/model.onnx", []byte("weights"))
	if !model.Present(root) {
		t.Fatal("a model that pins nothing is not present with its model file in place")
	}
	if dir, err := model.Fetch(t.Context(), root); err != nil || dir != model.Dir(root) {
		t.Fatalf("a model already in place was not accepted: %q, %v", dir, err)
	}
}

// A machine that reaches the hub through a mirror names it in the environment,
// as it always could. The mirror is asked for the same paths.
func TestTheHubsAddressCanBeAMirrors(t *testing.T) {
	model, contents := tinyModel()
	hub := newHub(t, model, contents)
	t.Setenv(EndpointEnv, hub.URL+"/")
	root := t.TempDir()

	if _, err := model.Fetch(t.Context(), root); err != nil {
		t.Fatalf("fetch through the mirror: %v", err)
	}
	holdsExactly(t, model, root, contents)
}
