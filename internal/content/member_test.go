package content

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
)

// memberRef returns the ref of the member at mpath of the archive at path.
func (e *env) memberRef(src domain.SourceID, path, mpath string) domain.Ref {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT m.id FROM archive_members m JOIN entries e ON e.id = m.archive_id
		WHERE e.source_id = ? AND e.path = ? AND m.path = ?`, string(src), []byte(path), []byte(mpath)).Scan(&id); err != nil {
		e.t.Fatalf("member %s!%s: %v", path, mpath, err)
	}
	return domain.Ref{Member: domain.MemberID(id)}
}

// files lists every file below dir, by relative path.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// OpenMember serves a stored zip member as a section with ranges, a small
// deflated member from memory with ranges, a larger one and a tar member as
// streams without, and refuses a changed archive; nothing is written to the
// state directory or TMPDIR.
func TestOpenMember(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	e := newEnv(t)
	e.a.ViewMaxBytes = 64 << 10
	e.service()
	video := randomBytes(9, 100<<10)
	photo := []byte(strings.Repeat("jpeg ", 4000))
	big := []byte(strings.Repeat("a long text ", 20000))
	root := e.disk("fotos", "/mnt/fotos", posix)
	z := root.File("coisas.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "video.avi", data: video, stored: true},
		zipEntry{name: "fotos/foto.jpg", data: photo}, zipEntry{name: "grande.txt", data: big}))
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	note := []byte("a note inside a tar.gz")
	tw.WriteHeader(&tar.Header{Name: "a/nota.txt", Mode: 0o644, Size: int64(len(note)), ModTime: fileTime})
	tw.Write(note)
	tw.Close()
	gz.Close()
	root.File("site.tar.gz", 0, fileTime).Content(buf.Bytes())
	e.scan("fotos")
	e.hash("fotos")
	stateDir := filepath.Dir(e.st.Path())
	beforeState, beforeTmp := files(t, stateDir), files(t, tmp)
	ctx := context.Background()

	open := func(path, mpath string) Opened {
		t.Helper()
		o, err := e.svc.OpenMember(ctx, e.st.Reader(), e.memberRef("fotos", path, mpath))
		if err != nil {
			t.Fatalf("open %s!%s: %v", path, mpath, err)
		}
		return o
	}
	read := func(o Opened) []byte {
		t.Helper()
		defer o.Content.Close()
		b, err := io.ReadAll(o.Content)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	o := open("coisas.zip", "video.avi")
	if o.Seeker == nil || o.Size != int64(len(video)) {
		t.Errorf("stored member: seeker %v, size %d", o.Seeker != nil, o.Size)
	} else {
		if _, err := o.Seeker.Seek(50<<10, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		part := make([]byte, 1000)
		if _, err := io.ReadFull(o.Seeker, part); err != nil || !bytes.Equal(part, video[50<<10:50<<10+1000]) {
			t.Errorf("a range of the stored member: %v", err)
		}
		// Content and Seeker are one reader: rewind it.
		if _, err := o.Seeker.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(o); !bytes.Equal(got, video) {
		t.Error("the stored member's bytes differ")
	}

	o = open("coisas.zip", "fotos/foto.jpg")
	if o.Seeker == nil {
		t.Error("a small deflated member has no ranges")
	}
	if got := read(o); !bytes.Equal(got, photo) {
		t.Error("the deflated member's bytes differ")
	}

	o = open("coisas.zip", "grande.txt")
	if o.Seeker != nil {
		t.Error("a deflated member over view_max_bytes has ranges")
	}
	if got := read(o); !bytes.Equal(got, big) {
		t.Error("the large deflated member's bytes differ")
	}

	o = open("site.tar.gz", "a/nota.txt")
	if o.Seeker != nil {
		t.Error("a tar member has ranges")
	}
	if got := read(o); !bytes.Equal(got, note) {
		t.Error("the tar member's bytes differ")
	}

	if _, err := e.svc.OpenMember(ctx, e.st.Reader(), e.memberRef("fotos", "coisas.zip", "fotos")); domain.CodeOf(err) != domain.CodeInvalidEntryState {
		t.Errorf("a member folder: %v", err)
	}
	if _, err := e.svc.OpenMember(ctx, e.st.Reader(), domain.Ref{Member: 999999}); domain.CodeOf(err) != domain.CodeNotFound {
		t.Errorf("an unknown member: %v", err)
	}

	if a, b := files(t, stateDir), files(t, tmp); !slices.Equal(a, beforeState) || !slices.Equal(b, beforeTmp) {
		t.Errorf("files appeared: state %q -> %q, TMPDIR %q -> %q", beforeState, a, beforeTmp, b)
	}

	// The zip changes on disk after its listing.
	z.ModTime(fileTime.Add(3600e9))
	_, err := e.svc.OpenMember(ctx, e.st.Reader(), e.memberRef("fotos", "coisas.zip", "video.avi"))
	if domain.CodeOf(err) != domain.CodeInvalidEntryState {
		t.Errorf("a member of a changed archive: %v; want invalid_entry_state", err)
	}
}
