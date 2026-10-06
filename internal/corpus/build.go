package corpus

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"precious/internal/fsaccess/synthfs"
)

// BuildSynth builds t as the synthfs root at the absolute path root and
// returns the root and the ground truth. The unreadable folder is marked
// with (*synthfs.Node).Unreadable.
func BuildSynth(f *synthfs.FS, root string, t *Tree) (*synthfs.Node, GroundTruth) {
	b := &synthBuilder{nodes: map[string]*synthfs.Node{"": f.Root(root)}}
	if err := t.Build(b); err != nil {
		panic(err) // the synthfs builder never fails
	}
	return b.nodes[""], t.GroundTruth()
}

type synthBuilder struct {
	nodes map[string]*synthfs.Node
}

// parent returns the folder holding p, and p's name.
func (b *synthBuilder) parent(p string) (*synthfs.Node, string) {
	dir, name := path.Split(p)
	if dir != "" {
		dir = dir[:len(dir)-1]
	}
	return b.nodes[dir], name
}

func (b *synthBuilder) Mkdir(p string) error {
	parent, name := b.parent(p)
	b.nodes[p] = parent.Dir(name)
	return nil
}

func (b *synthBuilder) WriteFile(p string, data []byte, mtime time.Time) error {
	parent, name := b.parent(p)
	parent.File(name, 0, mtime).Content(data)
	return nil
}

func (b *synthBuilder) Symlink(p, target string) error {
	parent, name := b.parent(p)
	parent.Symlink(name, target)
	return nil
}

func (b *synthBuilder) DirTime(p string, mtime time.Time) error {
	b.nodes[p].ModTime(mtime)
	return nil
}

func (b *synthBuilder) Unreadable(p string) error {
	b.nodes[p].Unreadable()
	return nil
}

// WriteDir writes t into the directory dir, which must be missing or empty,
// and returns the ground truth. The unreadable folder gets mode 000; making
// it readable again (chmod 755) is needed before the tree can be removed.
func WriteDir(dir string, t *Tree) (GroundTruth, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return GroundTruth{}, err
	}
	existing, err := os.ReadDir(dir)
	if err != nil {
		return GroundTruth{}, err
	}
	if len(existing) > 0 {
		return GroundTruth{}, fmt.Errorf("corpus: %s is not empty", dir)
	}
	if err := t.Build(dirBuilder{root: dir}); err != nil {
		return GroundTruth{}, err
	}
	return t.GroundTruth(), nil
}

type dirBuilder struct {
	root string
}

func (b dirBuilder) abs(p string) string { return filepath.Join(b.root, filepath.FromSlash(p)) }

func (b dirBuilder) Mkdir(p string) error { return os.Mkdir(b.abs(p), 0o755) }

func (b dirBuilder) WriteFile(p string, data []byte, mtime time.Time) error {
	name := b.abs(p)
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Chtimes(name, mtime, mtime)
}

func (b dirBuilder) Symlink(p, target string) error { return os.Symlink(target, b.abs(p)) }

func (b dirBuilder) DirTime(p string, mtime time.Time) error {
	return os.Chtimes(b.abs(p), mtime, mtime)
}

func (b dirBuilder) Unreadable(p string) error { return os.Chmod(b.abs(p), 0) }
