// Command walkbench measures the scanner's overhead against a bare metadata
// walk of the same tree (R1.14, design D19).
//
//	go run ./tools/walkbench -root DIR [-state DIR] [-cpuprofile FILE] [-only walk|scan]
//
// It primes the cache with one bare walk, then times a bare walk (list every
// folder in batches and Lstat every entry, as the scan does, without
// following symlinks or crossing mount points) and a full scan of DIR into a
// fresh database in the state directory, run as a scan job by the job
// runner. It prints the entry count, both times, and the scan's time as a
// multiple of the bare walk's. Without -state the database goes to a
// temporary directory that is removed afterwards; a given -state directory
// must not hold a database yet. Both measurements are warm. -cpuprofile
// writes a CPU profile of the timed scan, for go tool pprof.
//
// For the cold measurement, drop the filesystem caches (on ZFS, export and
// import the pool) before each of two runs, -only walk and then -only scan:
// each times just that measurement, without priming, and the ratio is the
// scan's time over the walk's.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "walkbench:", err)
		}
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("walkbench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rootFlag := flags.String("root", "", "the tree to walk and scan")
	stateFlag := flags.String("state", "", "state directory for the scan's database (default: a temporary one)")
	profile := flags.String("cpuprofile", "", "write a CPU profile of the scan to this file")
	only := flags.String("only", "", `time only "walk" or "scan", without priming the cache (cold measurement)`)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *rootFlag == "" || flags.NArg() > 0 || (*only != "" && *only != "walk" && *only != "scan") {
		flags.Usage()
		return errors.New("usage: walkbench -root DIR [-state DIR] [-cpuprofile FILE] [-only walk|scan]")
	}
	root, err := filepath.Abs(*rootFlag)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return err
	}
	fsys := fsaccess.NewOS()
	if *only == "walk" {
		start := time.Now()
		entries, err := bareWalk(fsys, root)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "root     %s\nentries  %d walked\nwalk     %s\n", root, entries, time.Since(start).Round(time.Millisecond))
		return nil
	}

	state := *stateFlag
	if state == "" {
		tmp, err := os.MkdirTemp("", "walkbench-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		state = tmp
	} else if err := config.EnsureStateDir(state); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(state, store.FileName)); err == nil {
		return fmt.Errorf("%s already holds a database; walkbench scans into a fresh one", state)
	}

	ctx := context.Background()
	if *only == "scan" {
		scan, scanned, err := scanInto(ctx, fsys, root, state, *profile)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "root     %s\nentries  %d indexed\nscan     %s\n", root, scanned, scan.Round(time.Millisecond))
		return nil
	}
	if _, err := bareWalk(fsys, root); err != nil { // prime the cache
		return err
	}
	start := time.Now()
	entries, err := bareWalk(fsys, root)
	if err != nil {
		return err
	}
	walk := time.Since(start)

	scan, scanned, err := scanInto(ctx, fsys, root, state, *profile)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "root     %s\nentries  %d walked, %d indexed\nwalk     %s\nscan     %s\nratio    %.2f\n",
		root, entries, scanned, walk.Round(time.Millisecond), scan.Round(time.Millisecond),
		scan.Seconds()/walk.Seconds())
	return nil
}

// listBatch is the scan's default listing batch, which the bare walk uses
// too.
var listBatch = config.Defaults().Scan.ListBatch

// bareWalk lists every folder below root and Lstats every entry, as the scan
// does, and returns the number of entries below root. Unreadable folders are
// skipped.
func bareWalk(fsys fsaccess.FS, root string) (int64, error) {
	d, err := fsys.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	var n int64
	walkDir(d, &n)
	return n, nil
}

func walkDir(d fsaccess.Dir, n *int64) {
	for {
		entries, err := d.ReadBatch(listBatch)
		if err != nil {
			return // the end, or an unreadable folder, which the scan records as such
		}
		for _, e := range entries {
			info, err := d.Lstat(e.Name)
			if err != nil {
				continue
			}
			*n++
			if info.Kind != domain.EntryDirectory || info.MountBoundary {
				continue
			}
			sub, err := d.OpenDir(e.Name, info)
			if err != nil {
				continue
			}
			walkDir(sub, n)
			sub.Close()
		}
	}
}

// scanInto adds root as a source of a fresh database in state, through the
// picker as the API does, runs its scan as a job, and returns the time from
// queueing the job to its success and the number of entries indexed.
func scanInto(ctx context.Context, fsys fsaccess.FS, root, state, profile string) (time.Duration, int64, error) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, state, store.Options{Logger: logger})
	if err != nil {
		return 0, 0, err
	}
	defer st.Close()
	src, err := sources.New(st, fsys, config.Sources{AllowedRoots: []string{root}}, clock.Real{})
	if err != nil {
		return 0, 0, err
	}
	picked, err := src.PickerRoots(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(picked) != 1 {
		return 0, 0, fmt.Errorf("the picker offers %d roots for %s", len(picked), root)
	}
	cand, err := src.PrepareAdd(ctx, picked[0].Handle, "walkbench")
	if err != nil {
		return 0, 0, err
	}
	var added sources.Source
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		added, err = src.Add(ctx, tx, cand)
		return err
	}); err != nil {
		return 0, 0, err
	}

	runner, err := jobs.NewRunner(jobs.Options{Store: st, Registry: src, Logger: logger,
		TickInterval: 10 * time.Millisecond})
	if err != nil {
		return 0, 0, err
	}
	index.NewHandler(st, src, rules.Default(), clock.Real{}, config.Defaults().Scan).Register(runner)
	if err := runner.Start(ctx); err != nil {
		return 0, 0, err
	}
	defer runner.Stop(ctx)

	if profile != "" {
		f, err := os.Create(profile)
		if err != nil {
			return 0, 0, err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return 0, 0, err
		}
		defer pprof.StopCPUProfile()
	}
	start := time.Now()
	var job domain.JobID
	if err := runner.Write(ctx, func(tx *jobs.Tx) error {
		acc, err := index.StartScan(ctx, tx, added.ID)
		if err == nil {
			job, err = domain.ParseJobID(acc.JobID)
		}
		return err
	}); err != nil {
		return 0, 0, err
	}
	for {
		rec, err := runner.Get(ctx, job)
		if err != nil {
			return 0, 0, err
		}
		if rec.State.Terminal() {
			elapsed := time.Since(start)
			if rec.State != domain.JobSucceeded {
				return 0, 0, fmt.Errorf("scan %s: %s %s", rec.State, rec.TerminalCode, rec.TerminalDetail)
			}
			var n int64
			err := st.Reader().QueryRowContext(ctx, `SELECT count(*) - 1 FROM entries WHERE source_id = ?`,
				string(added.ID)).Scan(&n)
			return elapsed, n, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
