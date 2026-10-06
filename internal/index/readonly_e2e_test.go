//go:build e2e && linux

package index

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store/storetest"
)

const (
	// nsEnv names the test this binary re-runs as root of a user and mount
	// namespace.
	nsEnv = "PRECIOUS_INDEX_E2E_NS"
	// scanEnv is the read-only mount this binary re-runs the test to scan,
	// without the capabilities that bypass permission bits.
	scanEnv = "PRECIOUS_INDEX_E2E_SCAN"
)

// dacCaps are the capabilities that let root read a mode-000 folder.
const dacCaps = 1<<unix.CAP_DAC_OVERRIDE | 1<<unix.CAP_DAC_READ_SEARCH

// R1.9 on a real read-only mount: the regression corpus, written to a tmpfs
// that is then remounted read-only, is scanned in full and rescanned through
// the OS backend, the source registry, and the job runner. Both scans succeed
// and index the ground truth, and no entry on the mount changes: not its
// mode, size, inode, link count, owner, mtime, atime, or ctime.
//
// Mounting needs root: the test runs directly as root, inside a user and
// mount namespace (unshare(1)) otherwise, and skips, naming the reason, when
// neither works. The scans run in a re-run of the test binary without
// CAP_DAC_OVERRIDE and CAP_DAC_READ_SEARCH, so the corpus's mode-000 folder
// is unreadable to them, as the ground truth says.
func TestR1_9ReadOnlyMountUnchangedByScans(t *testing.T) {
	if mnt := os.Getenv(scanEnv); mnt != "" {
		scanReadOnlyCorpus(t, mnt)
		return
	}
	asRoot(t, func(t *testing.T) {
		mnt := readOnlyCorpus(t)
		before := listing(t, mnt)
		if n := len(corpus.Corpus().GroundTruth().Entries); len(before) <= n {
			t.Fatalf("the listing holds %d entries, the ground truth %d plus those below the unreadable folder", len(before), n)
		}
		rerun(t, dropDACCaps, scanEnv+"="+mnt)
		after := listing(t, mnt)
		changed := 0
		for p, b := range before {
			if a, ok := after[p]; !ok || a != b {
				changed++
				t.Errorf("%q changed:\n before %+v\n after  %+v (present %v)", p, b, a, ok)
			}
			if changed == 20 {
				t.Fatal("stopping after 20 changed entries")
			}
		}
		if len(after) != len(before) {
			t.Errorf("the mount held %d entries before the scans and %d after", len(before), len(after))
		}
	})
}

// asRoot runs body with root's capabilities: directly when the process is
// root, otherwise as root of a fresh user and mount namespace, by re-running
// this test under unshare(1). It skips, naming the reason, when unprivileged
// user namespaces are unavailable.
func asRoot(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	if os.Geteuid() == 0 || os.Getenv(nsEnv) == t.Name() {
		body(t)
		return
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skipf("not root, and unshare(1) is not installed: %v", err)
	}
	ns := []string{"--user", "--mount", "--map-root-user"}
	if out, err := exec.Command("unshare", append(ns, "true")...).CombinedOutput(); err != nil {
		t.Skipf("not root, and unprivileged user namespaces are denied (unshare --user --mount --map-root-user: %v: %s)",
			err, bytes.TrimSpace(out))
	}
	rerun(t, nil, nsEnv+"="+t.Name(), append([]string{"unshare"}, ns...)...)
}

// rerun runs this test again in a child process with env added to the
// environment, under the command prefix when one is given. It skips when
// the child skips and fails unless the child passes. A non-nil prepare runs
// first on a thread locked for the purpose, which starts the child and then
// exits: thread state prepare sets, such as capabilities, reaches the child
// and no other thread.
func rerun(t *testing.T, prepare func() error, env string, prefix ...string) {
	t.Helper()
	args := append(prefix, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), env)
	type result struct {
		out      []byte
		err      error
		prepared bool
	}
	done := make(chan result, 1)
	go func() {
		// Never unlocked: the thread ends with this goroutine.
		runtime.LockOSThread()
		if prepare != nil {
			if err := prepare(); err != nil {
				done <- result{err: err}
				return
			}
		}
		out, err := cmd.CombinedOutput()
		done <- result{out: out, err: err, prepared: true}
	}()
	r := <-done
	if !r.prepared {
		t.Skipf("cannot prepare the re-run: %v", r.err)
	}
	if bytes.Contains(r.out, []byte("--- SKIP: "+t.Name())) {
		t.Skipf("the re-run (%s) skipped:\n%s", env, r.out)
	}
	if r.err != nil || !bytes.Contains(r.out, []byte("--- PASS: "+t.Name())) {
		t.Fatalf("the re-run (%s) failed: %v\n%s", env, r.err, r.out)
	}
}

// dropDACCaps removes CAP_DAC_OVERRIDE and CAP_DAC_READ_SEARCH from the
// calling thread's bounding and inheritable sets, so a program it executes
// lacks them even as root.
func dropDACCaps() error {
	for _, c := range []uintptr{unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH} {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, c, 0, 0, 0); err != nil {
			return fmt.Errorf("drop capability %d from the bounding set: %w", c, err)
		}
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capget: %w", err)
	}
	data[0].Inheritable &^= dacCaps
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capset: %w", err)
	}
	return nil
}

// readOnlyCorpus mounts a tmpfs, writes the regression corpus into it,
// remounts it read-only, and returns the mount point. It skips when the
// process may not mount.
func readOnlyCorpus(t *testing.T) string {
	t.Helper()
	mnt := filepath.Join(t.TempDir(), "corpus")
	if err := os.Mkdir(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", mnt, "tmpfs", 0, "mode=0755"); errors.Is(err, unix.EPERM) {
		t.Skipf("cannot mount a tmpfs (needs CAP_SYS_ADMIN, e.g. docker run --privileged): %v", err)
	} else if err != nil {
		t.Fatalf("mount a tmpfs on %s: %v", mnt, err)
	}
	t.Cleanup(func() { unix.Unmount(mnt, unix.MNT_DETACH) })
	if _, err := corpus.WriteDir(mnt, corpus.Corpus()); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", mnt, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		t.Fatalf("remount %s read-only: %v", mnt, err)
	}
	var sfs unix.Statfs_t
	if err := unix.Statfs(mnt, &sfs); err != nil {
		t.Fatal(err)
	}
	if sfs.Flags&unix.ST_RDONLY == 0 {
		t.Fatalf("%s is not read-only after the remount (statfs flags %#x)", mnt, sfs.Flags)
	}
	return mnt
}

// stamp is what an entry's lstat reports, all of it but the device and the
// block size.
type stamp struct {
	Mode                uint32
	Ino, Nlink          uint64
	UID, GID            uint32
	Size, Blocks        int64
	Atime, Mtime, Ctime unix.Timespec
}

// listing lstats every entry below root, root included, by path relative to
// root. The caller must be able to list every folder.
func listing(t *testing.T, root string) map[string]stamp {
	t.Helper()
	out := map[string]stamp{}
	err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			return fmt.Errorf("lstat %s: %w", p, err)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[rel] = stamp{Mode: st.Mode, Ino: st.Ino, Nlink: st.Nlink, UID: st.Uid, GID: st.Gid, Size: st.Size,
			Blocks: st.Blocks, Atime: st.Atim, Mtime: st.Mtim, Ctime: st.Ctim}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// scanReadOnlyCorpus adds the read-only corpus at mnt as a source through the
// picker, as the API does, runs a full scan and a rescan of it as jobs of the
// job runner over the OS backend, and checks the index against the ground
// truth after the first and that the second changes no row.
func scanReadOnlyCorpus(t *testing.T, mnt string) {
	if eff := effectiveCaps(t); eff&dacCaps != 0 {
		t.Fatalf("the scan run holds CAP_DAC_OVERRIDE or CAP_DAC_READ_SEARCH (CapEff %#x)", eff)
	}
	ctx := context.Background()
	st := storetest.Open(t)
	svc, err := sources.New(st, fsaccess.NewOS(), config.Sources{AllowedRoots: []string{mnt}}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	picked, err := svc.PickerRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(picked) != 1 {
		t.Fatalf("the picker offers %d roots for %s", len(picked), mnt)
	}
	cand, err := svc.PrepareAdd(ctx, picked[0].Handle, "corpus")
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
	checkGroundTruth(t, rows, corpus.Corpus().GroundTruth())

	writes := e.writes()
	runScanJob(t, runner, src.ID)
	if n := writes(); n != 0 {
		t.Errorf("the rescan of the unchanged mount wrote %d rows", n)
	}
	if gen := e.sourceGen(src.ID); gen != 2 {
		t.Errorf("scan_gen %d after two scans", gen)
	}
	again := e.entries(src.ID)
	for p, r := range again {
		if r != rows[p] {
			t.Errorf("%q changed on the rescan:\n %+v\n %+v", p, rows[p], r)
		}
	}
	if len(again) != len(rows) {
		t.Errorf("%d rows after the rescan, %d before", len(again), len(rows))
	}
}

// effectiveCaps returns the process's effective capability set.
func effectiveCaps(t *testing.T) uint64 {
	t.Helper()
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "CapEff:"); ok {
			eff, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return eff
		}
	}
	t.Fatalf("no CapEff line in /proc/self/status (%v)", sc.Err())
	return 0
}

// runScanJob starts a scan of src as start-scan does and waits for the job
// to succeed.
func runScanJob(t *testing.T, r *jobs.Runner, src domain.SourceID) {
	t.Helper()
	ctx := context.Background()
	var job domain.JobID
	if err := r.Write(ctx, func(tx *jobs.Tx) error {
		acc, err := StartScan(ctx, tx, src)
		if err == nil {
			job, err = domain.ParseJobID(acc.JobID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for {
		rec, err := r.Get(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State.Terminal() {
			if rec.State != domain.JobSucceeded {
				t.Fatalf("scan job %s: %s %s", rec.State, rec.TerminalCode, rec.TerminalDetail)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// checkGroundTruth checks that rows hold exactly the ground truth's entries
// and the root, each with its kind, state, and file size, the root with
// every readable byte, and the asserted classifications and file kinds,
// which the scan reads file content for.
func checkGroundTruth(t *testing.T, rows map[string]entry, gt corpus.GroundTruth) {
	t.Helper()
	if len(rows) != len(gt.Entries)+1 {
		t.Errorf("indexed %d rows, ground truth %d entries plus the root", len(rows), len(gt.Entries))
	}
	for _, g := range gt.Entries {
		r := get(t, rows, rawPath(t, g))
		kind, _ := kindColumn(g.Kind)
		state := map[bool]string{false: "present", true: "unreadable"}[g.Unreadable]
		if r.Kind != kind || r.State != state {
			t.Errorf("%s: %s %s; ground truth %s %s", g.Path, r.Kind, r.State, kind, state)
		}
		if g.Size != nil && r.Size != *g.Size {
			t.Errorf("%s: size %d; ground truth %d", g.Path, r.Size, *g.Size)
		}
		if g.Category != "" && (r.Category.String != g.Category || r.Triage.String != g.Triage) {
			t.Errorf("%s = %s/%s; ground truth %s/%s", g.Path, r.Category.String, r.Triage.String, g.Category, g.Triage)
		}
		if g.FileKind != "" && r.FileKind.String != g.FileKind {
			t.Errorf("%s file kind %s; ground truth %s", g.Path, r.FileKind.String, g.FileKind)
		}
	}
	if root := get(t, rows, ""); root.TotalBytes != corpusReadableBytes(t, gt) {
		t.Errorf("root totals %d bytes; ground truth %d", root.TotalBytes, corpusReadableBytes(t, gt))
	}
}
