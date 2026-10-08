// Package content finds what each indexed file holds (§6.4, §7, R2 design
// D3–D8, D17): the hashing jobs, the listing of archives, coverage, copies,
// and opening archive members. Its reading core is restored from
// curator-m4b's compare package (hash.go, chunks.go, members.go,
// archive.go, chain.go) and works over the index's tables.
//
// Planning (plan.go) is a database-only pass at the start of every hashing
// job: one file_content row per present non-empty regular file, size
// groups over every source's files and the file members of complete
// archives, hard links counted once, and the states unique_size and
// pending. Coverage is recomputed there and kept by deltas in every
// hashing commit.
//
// A hashing job (job.go) reads, in this order: the source's unlisted zip
// archives (central directory only); the files of at least 1 MiB, the
// streamed archives, and the zip archives with large shared members, by
// size descending, files of at least 16 MiB by three 64 KiB samples
// first; the small files inside candidate folder pairs; then every other
// small file. Every read walks the row's path through fsaccess with the
// identity checks of OpenAt, in read_chunk_bytes chunks, each a watched
// call; a digest is kept only for a complete read of an unchanged file.
// Results commit at most 64 files per transaction, each re-checked against
// its entries row (I9): present, with the identity the read observed, and
// at the path the read used, so a read through the old path of a folder
// moved meanwhile is dropped rather than recorded changed (r3 design D18).
// The job yields after every commit and every yield_bytes read.
//
// Cleanup hashes on demand (hashread.go, R4 design D9): HashEntry,
// HashArchive, and HashMember read a file, every file member of a complete
// archive from one open of it, or one member, in full through the same
// identity-checked opens, fstat the file once read, and write nothing.
package content

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/sources"
	"precious/internal/store"
)

// The hashing job kinds (design D5).
const (
	// KindHash hashes one source: ClassBulk, single flight by scope
	// "hash:<source>".
	KindHash jobs.Kind = "hash"
	// KindHashNow hashes chosen folders of one source first: ClassInteractive,
	// single flight by scope "hash_now:<source>", coalescing the folders.
	KindHashNow jobs.Kind = "hash_now"
)

// Constants fixed by the spec (design D18).
const (
	// SampleFromBytes is the size from which a file is compared by samples
	// before any full read.
	SampleFromBytes = 16 << 20
	// SampleBytes is the size of each of the Samples samples, taken at 0,
	// size/2 - SampleBytes/2, and size - SampleBytes.
	SampleBytes = 64 << 10
	Samples     = 3
	// LargeFileBytes splits the large phase (by size) from the small one
	// (by folder).
	LargeFileBytes = 1 << 20
	// CommitFiles bounds the files of one hashing commit (design D4).
	CommitFiles = 64
)

// Internal batch sizes.
const (
	// planSpan is the entries ID span of one planning insert.
	planSpan = 50_000
	// planBatch bounds the state changes of one planning transaction.
	planBatch = 5_000
	// listBatch bounds the member rows of one listing transaction.
	listBatch = 1_000
	// pageRows is the page of rows one reading query returns.
	pageRows = 256
)

// Service hashes sources and answers content queries.
type Service struct {
	st      *store.Store
	src     *sources.Service
	clk     clock.Clock
	h       config.Hashing
	a       config.Archives
	refresh time.Duration
	log     *slog.Logger
	runner  *jobs.Runner

	// planMu serializes planning (design D3).
	planMu sync.Mutex

	// candidates returns the provisional candidate folder pairs of a source
	// (design D4 step 4), and requestRefresh asks for a relate pass inside a
	// hashing commit (design D5). Tests replace them.
	candidates     func(ctx context.Context, q store.Queryer, src domain.SourceID) ([][2]relations.Range, error)
	requestRefresh func(tx *jobs.Tx) error
}

// NewService returns the hashing service. Zero settings take the
// configuration defaults; clk nil is the real clock.
func NewService(st *store.Store, src *sources.Service, clk clock.Clock, h config.Hashing, a config.Archives,
	d config.Duplicates) *Service {
	def := config.Defaults()
	if h.ReadChunkBytes <= 0 {
		h.ReadChunkBytes = def.Hashing.ReadChunkBytes
	}
	if h.YieldBytes <= 0 {
		h.YieldBytes = def.Hashing.YieldBytes
	}
	if a.MaxMembers <= 0 {
		a.MaxMembers = def.Archives.MaxMembers
	}
	if a.MaxUnpackedBytes <= 0 {
		a.MaxUnpackedBytes = def.Archives.MaxUnpackedBytes
	}
	if a.MaxRatio <= 0 {
		a.MaxRatio = def.Archives.MaxRatio
	}
	if a.MaxTime.Duration <= 0 {
		a.MaxTime = def.Archives.MaxTime
	}
	if a.ViewMaxBytes <= 0 {
		a.ViewMaxBytes = def.Archives.ViewMaxBytes
	}
	refresh := d.RefreshInterval.Duration
	if refresh <= 0 {
		refresh = def.Duplicates.RefreshInterval.Duration
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Service{st: st, src: src, clk: clk, h: h, a: a, refresh: refresh, log: slog.Default(),
		candidates: relations.Candidates, requestRefresh: relations.RequestRefresh}
}

// Register registers hash in the bulk class and hash_now in the interactive
// class (design D5). Call it before r.Start; the service commits through r.
func (s *Service) Register(r *jobs.Runner) {
	s.runner = r
	r.RegisterClass(KindHash, &handler{s: s}, jobs.ClassBulk)
	r.RegisterClass(KindHashNow, &handler{s: s, now: true}, jobs.ClassInteractive)
}

// AfterScan is the index's after-scan hook (design D5): it enqueues a
// hashing job for every online source, so that sizes shared across sources
// are found. Failures are logged; the next scan or start enqueues again.
func (s *Service) AfterScan(ctx context.Context, src domain.SourceID) {
	if err := s.enqueueOnline(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("content: enqueue hashing after a scan", "source", src, "err", err)
	}
}

// Startup enqueues a hashing job for every online source, at server start
// after the runner started.
func (s *Service) Startup(ctx context.Context) error {
	return s.enqueueOnline(ctx)
}

// enqueueOnline enqueues hash once for every online source.
func (s *Service) enqueueOnline(ctx context.Context) error {
	return s.runner.Write(ctx, func(tx *jobs.Tx) error {
		rows, err := tx.SQL().QueryContext(ctx, `SELECT id FROM sources WHERE state = ? ORDER BY id`,
			string(sources.StateOnline))
		if err != nil {
			return err
		}
		var ids []domain.SourceID
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, domain.SourceID(id))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, _, err := tx.EnqueueOnce(hashSpec(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

// hashSpec is the hash job of src.
func hashSpec(src domain.SourceID) jobs.Spec {
	return jobs.Spec{Kind: KindHash, SourceID: src, ScopeKey: "hash:" + string(src)}
}

// EnqueueHashing enqueues, in tx, the hashing job of src when src is
// online, once: its plan pass rewrites the source's coverage and size
// groups. organize's index adapter calls it when an action ends, since a
// cleanup or a restore moves files into or out of the quarantine, which
// coverage leaves out (r4 B5).
func EnqueueHashing(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error {
	var state string
	err := tx.SQL().QueryRowContext(ctx, `SELECT state FROM sources WHERE id = ?`, string(src)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state != string(sources.StateOnline) {
		return nil
	}
	if err != nil {
		return err
	}
	_, _, err = tx.EnqueueOnce(hashSpec(src))
	return err
}

func (s *Service) now() int64 { return clock.Millis(s.clk.Now()) }
