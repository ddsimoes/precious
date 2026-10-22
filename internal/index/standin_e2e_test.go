//go:build e2e && linux

package index

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store/storetest"
)

// r5b task 1.2 (spec source-registry, "An empty folder in place of a path
// source's disk", "A folder of a filesystem of the same type at a path
// source's mount point"), with real mounts, through the OS backend, the
// source registry, and the job runner. A tmpfs holding a few files is bound
// at the source's mount point, added through the picker, and scanned: a
// tmpfs is a weak path volume. Then the mount point holds a bind of an empty
// folder of the test's own filesystem, as a container's bind of the host's
// folder does while the disk is not mounted, and next a bind of an empty
// folder of another tmpfs, the source's own type. With each, the source
// reads offline/other_filesystem, start-scan and Open are refused, and no
// entry row changes. With the first tmpfs bound there again, it is online
// and a rescan writes no row.
//
// Mounting needs root: the test runs directly as root, inside a user and
// mount namespace (unshare(1)) otherwise, and skips, naming the reason, when
// neither works.
func TestPathSourceStandInMounts(t *testing.T) {
	asRoot(t, func(t *testing.T) {
		base := t.TempDir()
		disk, point, empty := filepath.Join(base, "disk"), filepath.Join(base, "mnt", "hd"), filepath.Join(base, "empty")
		other := filepath.Join(base, "other")
		for _, d := range []string{disk, point, empty, other} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, d := range []string{disk, other} {
			if err := unix.Mount("tmpfs", d, "tmpfs", 0, "mode=0755"); errors.Is(err, unix.EPERM) {
				t.Skipf("cannot mount a tmpfs (needs CAP_SYS_ADMIN, e.g. docker run --privileged): %v", err)
			} else if err != nil {
				t.Fatalf("mount a tmpfs on %s: %v", d, err)
			}
			t.Cleanup(func() { unix.Unmount(d, unix.MNT_DETACH) })
		}
		if err := os.Mkdir(filepath.Join(other, "vazio"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(disk, "Fotos"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"Fotos/a.jpg": "a photo", "notes.txt": "notes"} {
			if err := os.WriteFile(filepath.Join(disk, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		at := func(what, src string) {
			t.Helper()
			unix.Unmount(point, unix.MNT_DETACH) // the previous one, if any
			if err := unix.Mount(src, point, "", unix.MS_BIND, ""); err != nil {
				t.Fatalf("bind %s at %s: %v", what, point, err)
			}
		}
		at("the disk", disk)
		t.Cleanup(func() { unix.Unmount(point, unix.MNT_DETACH) })

		ctx := context.Background()
		st := storetest.Open(t)
		svc, err := sources.New(st, fsaccess.NewOS(), config.Sources{AllowedRoots: []string{point}}, clock.Real{})
		if err != nil {
			t.Fatal(err)
		}
		picked, err := svc.PickerRoots(ctx)
		if err != nil || len(picked) != 1 {
			t.Fatalf("picker roots %v, %v: want the mount point", picked, err)
		}
		cand, err := svc.PrepareAdd(ctx, picked[0].Handle, "hd")
		if err != nil {
			t.Fatal(err)
		}
		var src sources.Source
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			src, err = svc.Add(ctx, tx, cand)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if src.Volume.Kind != fsaccess.VolumePath || src.Volume.FSType != "tmpfs" {
			t.Fatalf("source volume %+v, want a tmpfs path volume", src.Volume)
		}
		runner, err := jobs.NewRunner(jobs.Options{Store: st, Registry: svc, Logger: slog.New(slog.DiscardHandler),
			TickInterval: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		NewHandler(st, svc, rules.Default(), clock.Real{}, config.Defaults().Scan).Register(runner)
		if err := runner.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { runner.Stop(ctx) })
		e := &env{t: t, st: st}

		runScanJob(t, runner, src.ID)
		rows := e.entries(src.ID)
		if len(rows) != 4 {
			t.Fatalf("the scan indexed %d rows, want the root, Fotos, and two files", len(rows))
		}
		refresh := func() sources.Source {
			t.Helper()
			if err := svc.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Get(ctx, src.ID)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		if got := refresh(); got.State != sources.StateOnline || got.RootIno == 0 {
			t.Fatalf("after the scan = %+v, want online with the root's inode recorded", got)
		}

		for _, standIn := range []struct{ what, src string }{
			{"an empty folder of the test's own filesystem", empty},
			{"an empty folder of another tmpfs", filepath.Join(other, "vazio")},
		} {
			at(standIn.what, standIn.src)
			got := refresh()
			if got.State != sources.StateOffline || got.StateReason != sources.ReasonOtherFilesystem {
				t.Fatalf("with %s at the mount point = %s/%s, want offline/%s",
					standIn.what, got.State, got.StateReason, sources.ReasonOtherFilesystem)
			}
			err := runner.Write(ctx, func(tx *jobs.Tx) error {
				_, err := StartScan(ctx, tx, src.ID)
				return err
			})
			if domain.CodeOf(err) != domain.CodeSourceOffline {
				t.Fatalf("start-scan with %s at the mount point: %v, want source_offline", standIn.what, err)
			}
			if op, err := svc.Open(ctx, src.ID); domain.CodeOf(err) != domain.CodeSourceOffline {
				if err == nil {
					op.Root.Close()
				}
				t.Fatalf("Open with %s at the mount point: %v, want source_offline", standIn.what, err)
			}
			for p, r := range e.entries(src.ID) {
				if r != rows[p] {
					t.Fatalf("%q changed with %s at the mount point:\n %+v\n %+v", p, standIn.what, rows[p], r)
				}
			}
		}

		at("the disk again", disk)
		if got := refresh(); got.State != sources.StateOnline || got.MountPoint != point {
			t.Fatalf("with the disk back = %+v, want online at %s", got, point)
		}
		writes := e.writes()
		runScanJob(t, runner, src.ID)
		if n := writes(); n != 0 {
			t.Errorf("the rescan with the disk back wrote %d rows", n)
		}
	})
}
