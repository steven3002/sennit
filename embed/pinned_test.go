package embed_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/embed/embedtest"
)

// The digests pinned for the default model are the digests of the model the
// recall regression is measured with. A digit wrong in one of them would refuse
// the real file on every first run, and nothing short of a real download would
// say so.
func TestTheDefaultModelIsPinnedToTheFilesItIsMeasuredWith(t *testing.T) {
	root := embedtest.ModelDir()
	if !embedtest.ModelPresent() {
		t.Skipf("the embedding model is not under %s; set %s to check its pinned digests", root, embedtest.ModelDirEnv)
	}
	model := embed.BGESmallEN
	dir := model.Dir(root)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the model directory: %v", err)
	}
	if len(entries) != len(model.Files) {
		t.Errorf("%s holds %d files, and the model pins %d", dir, len(entries), len(model.Files))
	}
	for _, file := range model.Files {
		name := filepath.Base(filepath.FromSlash(file.Path))
		opened, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("open %s: %v", name, err)
			continue
		}
		digest := sha256.New()
		size, err := io.Copy(digest, opened)
		opened.Close()
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if size != file.Size {
			t.Errorf("%s is %d bytes, and it is pinned at %d", name, size, file.Size)
		}
		if got := hex.EncodeToString(digest.Sum(nil)); got != file.SHA256 {
			t.Errorf("the SHA-256 of %s is %s, and it is pinned at %s", name, got, file.SHA256)
		}
	}
}

// The model file is fetched last. An earlier version took the model for
// present as soon as that one file was, so it must not land before the files
// the model is read with.
func TestTheModelFileIsTheLastOneFetched(t *testing.T) {
	model := embed.BGESmallEN
	if len(model.Files) == 0 {
		t.Fatal("the default model pins no files")
	}
	if last := model.Files[len(model.Files)-1].Path; last != model.OnnxFile {
		t.Errorf("the last file fetched is %s, want the model file %s", last, model.OnnxFile)
	}
}
