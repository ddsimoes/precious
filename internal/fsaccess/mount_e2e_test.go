//go:build e2e && linux

package fsaccess_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

const nsEnv = "PRECIOUS_FSACCESS_E2E_NS"

// inUserNamespace runs body as root of a fresh user and mount namespace by
// re-executing this test binary under unshare(1). It skips, naming the reason,
// when unprivileged user namespaces are unavailable.
func inUserNamespace(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	if os.Getenv(nsEnv) == t.Name() {
		body(t)
		return
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skipf("unshare(1) not installed: %v", err)
	}
	ns := []string{"--user", "--mount", "--map-root-user"}
	if out, err := exec.Command("unshare", append(ns, "true")...).CombinedOutput(); err != nil {
		t.Skipf("unprivileged user namespaces denied (unshare --user --mount --map-root-user: %v: %s)", err, bytes.TrimSpace(out))
	}
	cmd := exec.Command("unshare", append(ns, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")...)
	cmd.Env = append(os.Environ(), nsEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("--- PASS: "+t.Name())) {
		t.Fatalf("namespaced run failed: %v\n%s", err, out)
	}
}

func mount(t *testing.T, source, target, fstype string, flags uintptr, data string) {
	t.Helper()
	if err := unix.Mount(source, target, fstype, flags, data); err != nil {
		t.Fatalf("mount %s on %s: %v", source, target, err)
	}
	t.Cleanup(func() { unix.Unmount(target, unix.MNT_DETACH) })
}

func assertBoundary(t *testing.T, rec *instrument.Recorder, d fsaccess.Dir, name string) fsaccess.EntryInfo {
	t.Helper()
	if _, ok := find(listAll(t, d, 8), name); !ok {
		t.Fatalf("%s is not listed", name)
	}
	info := mustLstat(t, d, name)
	if info.Kind != domain.EntryDirectory || !info.MountBoundary {
		t.Fatalf("Lstat(%s) = %+v, want a mount-boundary directory", name, info)
	}
	if _, err := d.OpenDir([]byte(name), info); !errors.Is(err, fsaccess.ErrMountBoundary) {
		t.Fatalf("OpenDir(%s) = %v, want ErrMountBoundary", name, err)
	}
	for _, c := range rec.Calls() {
		if c.Depth() > 1 || (c.Op == instrument.OpOpenDir && c.Err == nil) {
			t.Errorf("call %s %q observed the mounted filesystem", c.Op, c.FullPath())
		}
	}
	return info
}

// TestA11NestedMount: a filesystem mounted at <root>/external is a mount
// boundary, and none of its entries are observed.
func TestA11NestedMount(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := t.TempDir()
		external := filepath.Join(src, "external")
		mkdir(t, external)
		mount(t, "tmpfs", external, "tmpfs", 0, "size=1m")
		writeFile(t, filepath.Join(external, "inside"))

		rec := instrument.Wrap(fsaccess.NewOS())
		d := openRoot(t, rec, src)
		if info := assertBoundary(t, rec, d, "external"); info.Dev == d.Self().Dev {
			t.Errorf("tmpfs shares the parent's device %d", info.Dev)
		}
	})
}

// Bind mount detected: a same-device bind mount at <root>/again is a mount
// boundary; only the mount table reveals it.
func TestBindMountDetected(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := t.TempDir()
		target := filepath.Join(src, "target")
		mkdir(t, target)
		writeFile(t, filepath.Join(target, "x"))
		again := filepath.Join(src, "again")
		mkdir(t, again)
		mount(t, target, again, "", unix.MS_BIND, "")

		rec := instrument.Wrap(fsaccess.NewOS())
		d := openRoot(t, rec, src)
		if info := assertBoundary(t, rec, d, "again"); info.Dev != d.Self().Dev {
			t.Errorf("bind mount on device %d, parent on %d: want the same device", info.Dev, d.Self().Dev)
		}
		if info := mustLstat(t, d, "target"); info.MountBoundary {
			t.Error("the bind source itself is not a mount point")
		}
	})
}

// A source root on a read-only mount reports ReadOnly and its mount row.
func TestReadOnlyMountReported(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := t.TempDir()
		mount(t, "tmpfs", src, "tmpfs", unix.MS_RDONLY, "size=1m")
		d := openRoot(t, fsaccess.NewOS(), src)
		info, err := d.FSInfo()
		if err != nil {
			t.Fatal(err)
		}
		if !info.ReadOnly || info.Type != unix.TMPFS_MAGIC || info.Mount == nil || info.Mount.FSType != "tmpfs" {
			t.Errorf("FSInfo = %+v (mount %+v), want a read-only tmpfs", info, info.Mount)
		}
	})
}
