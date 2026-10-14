// Package embed converts text to a vector.
package embed

import (
	"os"
	"path"
	"path/filepath"
)

// Model describes an embedding model and where its files live.
type Model struct {
	// Name is stored beside every vector, so a model change is a detectable
	// re-index rather than a silent corruption of the index.
	Name string
	// Repo is the model's upstream location.
	Repo string
	// Revision is the commit of Repo the files are fetched at. A branch moves
	// and a commit does not, and a model that changed under its name would put
	// two models' vectors in one index under one label.
	Revision string
	// OnnxFile is the file to fetch from the repo.
	OnnxFile string
	// Files are the files the model is made of, as Repo holds them at Revision.
	// A model that names none cannot be fetched, only loaded from a directory
	// that already holds it.
	Files []File
	// Dim is the vector width.
	Dim int
}

// A File is one file of a model, pinned to the bytes it had at the model's
// revision.
//
// The size and the digest are what make a download checkable: the hub is asked
// for a path, and what comes back is kept only if it is this file.
type File struct {
	// Path is where the file is in the repo, with forward slashes.
	Path string
	// Size is its length in bytes.
	Size int64
	// SHA256 is the digest of its bytes, in lower-case hexadecimal.
	SHA256 string
}

// name is the file's name in the model directory. The repo's subdirectory is
// flattened away, so the path in the repo and the path on disk differ.
func (f File) name() string { return path.Base(f.Path) }

// BGESmallEN is the default: 384 dimensions in full precision.
//
// The quantized build of the same model is deliberately not offered on the
// pure-Go backend. It is strictly worse there, several times slower for
// identical ranking, because that backend has no optimized kernels for it, so
// making it reachable would only be a way to choose the worse option.
//
// The model file is listed last. It is the one file an earlier version looked
// for to decide the model was there, so it lands only once every file the
// model is read with has.
var BGESmallEN = Model{
	Name:     "bge-small-en-v1.5-fp32",
	Repo:     "Xenova/bge-small-en-v1.5",
	Revision: "ea104dacec62c0de699686887e3f920caeb4f3e3",
	OnnxFile: "onnx/model.onnx",
	Files: []File{
		{Path: "config.json", Size: 683,
			SHA256: "fa73f90bf92c8cace1fbcb709626306f2bdbc9ea3e5b5f94b440df9b6aa56350"},
		{Path: "special_tokens_map.json", Size: 125,
			SHA256: "b6d346be366a7d1d48332dbc9fdf3bf8960b5d879522b7799ddba59e76237ee3"},
		{Path: "tokenizer_config.json", Size: 366,
			SHA256: "9261e7d79b44c8195c1cada2b453e55b00aeb81e907a6664974b4d7776172ab3"},
		{Path: "vocab.txt", Size: 231508,
			SHA256: "07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3"},
		{Path: "tokenizer.json", Size: 711396,
			SHA256: "d241a60d5e8f04cc1b2b3e9ef7a4921b27bf526d9f6050ab90f9267a1f9e5c66"},
		{Path: "onnx/model.onnx", Size: 133093490,
			SHA256: "828e1496d7fabb79cfa4dcd84fa38625c0d3d21da474a00f08db0f559940cf35"},
	},
	Dim: 384,
}

// Dir returns the directory a model's files occupy under root.
func (m Model) Dir(root string) string {
	return filepath.Join(root, m.Name, repoDirName(m.Repo))
}

// onnxBasename is the filename inside the model directory. The downloader
// flattens the repo's subdirectory away, so the path in the repo and the path
// on disk differ.
func (m Model) onnxBasename() string {
	if m.OnnxFile == "" {
		return "model.onnx"
	}
	return path.Base(m.OnnxFile)
}

// Present reports whether the model is already on disk under root.
//
// Every file has to be there, at the size it is pinned to. The model loads
// without some of them and embeds differently for it, and a file cut short is
// the one thing an interrupted copy leaves under the right name. A file of the
// right size is taken as it is: reading a model's worth of bytes to check them
// is a cost every command would pay, to guard a directory nothing but a
// verified download or the person who owns it writes to.
func (m Model) Present(root string) bool {
	if len(m.Files) == 0 {
		_, err := os.Stat(filepath.Join(m.Dir(root), m.onnxBasename()))
		return err == nil
	}
	for _, file := range m.Files {
		if !m.holds(root, file) {
			return false
		}
	}
	return true
}

// holds reports whether one of the model's files is on disk under root.
func (m Model) holds(root string, file File) bool {
	info, err := os.Stat(filepath.Join(m.Dir(root), file.name()))
	return err == nil && info.Mode().IsRegular() && info.Size() == file.Size
}

func repoDirName(repo string) string {
	out := []rune(repo)
	for i, r := range out {
		if r == '/' {
			out[i] = '_'
		}
	}
	return string(out)
}
