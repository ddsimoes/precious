package content

import (
	"errors"
	"fmt"
	"hash"
	"io"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// failure is a read that gave no result: the file is changed (it is not
// the file its row describes, or it changed while read) or unreadable.
type failure struct {
	state  domain.ContentState
	detail string
}

var changedRead = &failure{domain.ContentChanged, "changed during the read"}

// classify turns a failed walk, open, read, or fstat into a failure. A
// failure the outcome does not explain is checked against the source root:
// when the root no longer answers the job stops with source_offline,
// otherwise the file is unreadable.
func (r *run) classify(err error) (*failure, error) {
	if errors.Is(err, errMismatch) {
		return &failure{domain.ContentChanged, "changed since the scan"}, nil
	}
	o, ok := fsaccess.OutcomeOf(err)
	if !ok || o == "" {
		return nil, fmt.Errorf("content: read source %q: %w", r.source, err)
	}
	switch o {
	case domain.OutcomeAbsent, domain.OutcomeChangedDuringObservation:
		return &failure{domain.ContentChanged, "changed since the scan"}, nil
	case domain.OutcomeUnreadable:
		return &failure{domain.ContentUnreadable, cause(err)}, nil
	}
	done := r.rt.FSCall("FSInfo")
	_, rerr := r.root.FSInfo()
	done()
	if rerr != nil {
		return nil, domain.Wrap(domain.CodeSourceOffline, rerr, "source %q became unavailable during hashing", r.source)
	}
	return &failure{domain.ContentUnreadable, cause(err)}, nil
}

func cause(err error) string {
	var fe *fsaccess.Error
	if errors.As(err, &fe) && fe.Err != nil {
		return fe.Err.Error()
	}
	return err.Error()
}

// result is the outcome of reading one file.
type result struct {
	digest []byte // a full read
	sample []byte // a sample read
	fail   *failure
	// inode: the result concerns the open file, so every name of its inode.
	inode bool
}

// openRow opens a row's file through the job's chain.
func (r *run) openRow(f *fileRow) (fsaccess.File, fsaccess.EntryInfo, *failure, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, fsaccess.EntryInfo{}, nil, err
	}
	fh, info, err := r.chain.open(f.row(), r.caps)
	if err != nil {
		fail, err := r.classify(err)
		return nil, fsaccess.EntryInfo{}, fail, err
	}
	return fh, info, nil, nil
}

// closeFile closes an open file in a watched call.
func (r *run) closeFile(fh fsaccess.File) {
	done := r.rt.FSCall("Close")
	_ = fh.Close()
	done()
}

// readFile opens f with its row's identity and reads its three samples or
// its whole content, then checks that the open file is still the one it
// opened (design D4). The handle is closed on return, also on a cancel.
func (r *run) readFile(f *fileRow, samples bool) (result, error) {
	fh, info, fail, err := r.openRow(f)
	if err != nil || fail != nil {
		return result{fail: fail}, err
	}
	defer r.closeFile(fh)
	r.opened++
	r.sum.Reset()
	if samples {
		for _, off := range [Samples]int64{0, f.size/2 - SampleBytes/2, f.size - SampleBytes} {
			if fail, err := r.readRange(fh, off, SampleBytes, false); err != nil || fail != nil {
				return result{fail: fail, inode: true}, err
			}
		}
	} else if fail, err := r.readRange(fh, 0, f.size, true); err != nil || fail != nil {
		return result{fail: fail, inode: true}, err
	}
	if fail, err := r.restat(fh, info); err != nil || fail != nil {
		return result{fail: fail, inode: true}, err
	}
	if samples {
		return result{sample: r.sum.Sum(nil)}, nil
	}
	return result{digest: r.sum.Sum(nil)}, nil
}

// restat checks that the open file is still the one lstat described.
func (r *run) restat(fh fsaccess.File, info fsaccess.EntryInfo) (*failure, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	done := r.rt.FSCall("FileStat")
	got, err := fh.Stat()
	done()
	if err != nil {
		return r.readFailure(err)
	}
	if !sameFile(got, info) {
		return changedRead, nil
	}
	return nil, nil
}

// readRange hashes n bytes at off in chunks of at most read_chunk_bytes,
// each a watched call, with ctx checked before each. With probeEOF, off+n
// is the listed size and a read there must return EOF and no byte; the
// last short chunk asks one byte more to check it in the same call.
func (r *run) readRange(fh fsaccess.File, off, n int64, probeEOF bool) (*failure, error) {
	end, chunk := off+n, int64(len(r.buf))
	eof := false
	for off < end {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		want := min(chunk, end-off)
		ask := want
		if probeEOF && want < chunk {
			ask = want + 1
		}
		done := r.rt.FSCall("ReadAt")
		k, err := fh.ReadAt(r.buf[:ask], off)
		done()
		r.addBytes(int64(k))
		switch {
		case err != nil && err != io.EOF:
			return r.readFailure(err)
		case int64(k) != want:
			return changedRead, nil // shorter or longer than listed
		case ask > want && err != io.EOF:
			return changedRead, nil
		}
		eof = ask > want || (err == io.EOF && off+want == end)
		r.sum.Write(r.buf[:want])
		off += want
		if err := r.maybeYield(); err != nil {
			return nil, err
		}
	}
	if !probeEOF || eof {
		return nil, nil
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	done := r.rt.FSCall("ReadAt")
	k, err := fh.ReadAt(r.buf[:1], end)
	done()
	r.addBytes(int64(k))
	switch {
	case err != nil && err != io.EOF:
		return r.readFailure(err)
	case k != 0 || err != io.EOF:
		return changedRead, nil // the file grew
	}
	return nil, nil
}

// readFailure classifies a failed read or fstat of an open file.
func (r *run) readFailure(err error) (*failure, error) {
	if o, ok := fsaccess.OutcomeOf(err); ok && (o == domain.OutcomeChangedDuringObservation || o == domain.OutcomeAbsent) {
		return changedRead, nil
	}
	return r.classify(err)
}

func (r *run) addBytes(n int64) {
	r.readBytes += n
	r.sinceYield += n
}

// maybeYield yields after every yield_bytes read, even inside a file: the
// handle stays open and no transaction is held (design D4).
func (r *run) maybeYield() error {
	if r.sinceYield < r.s.h.YieldBytes {
		return nil
	}
	r.sinceYield = 0
	r.report()
	return r.rt.Yield(r.ctx)
}

// errChunkRead marks a read of an archive file that failed or did not
// return the listed bytes; chunkFile.fail tells which.
var errChunkRead = errors.New("content: the archive file could not be read as listed")

// chunkFile reads an open archive file of a listed size for the archive
// readers (restored from m4b's chunks.go). Every read of the file is one
// watched call of at most read_chunk_bytes, with ctx checked before it,
// into a read-ahead window of one chunk filled from the first offset not
// buffered. archive/zip reads 4 KiB at a time, so a zip's directory costs
// about its size divided by the chunk; a sequential Read reads each byte
// once.
//
// A call whose window reaches the listed size asks one byte more, so the
// end of the file is confirmed in the same call. A read that fails,
// returns fewer bytes than the listed size holds, or finds more, sticks:
// fail holds the failure (changed when the file changed) and err is
// errChunkRead; err is instead the source's failure or ctx's error. With
// sum set, the bytes read contiguously from offset 0 are hashed, so that a
// streamed archive's own digest comes with its listing.
type chunkFile struct {
	r    *run
	fh   fsaccess.File
	size int64
	buf  []byte

	sum    hash.Hash
	hashed int64

	off  int64 // the file offset of buf[0]
	n    int64 // the bytes of buf holding file content
	pos  int64 // Read's next offset
	eof  bool  // a read at the listed size returned io.EOF
	fail *failure
	err  error
}

// fill reads the chunk at start into the window, in one watched call.
func (f *chunkFile) fill(start int64) error {
	if f.err != nil {
		return f.err
	}
	f.n = 0
	if err := f.r.ctx.Err(); err != nil {
		f.err = err
		return err
	}
	chunk := int64(len(f.buf))
	want := max(0, min(chunk, f.size-start))
	ask := want
	if want < chunk {
		ask = want + 1 // the end of the file, checked in the same call
	}
	done := f.r.rt.FSCall("ReadAt")
	k, err := f.fh.ReadAt(f.buf[:ask], start)
	done()
	f.r.addBytes(int64(k))
	switch {
	case err != nil && err != io.EOF:
		fail, ferr := f.r.readFailure(err)
		if ferr != nil {
			f.err = ferr
			return ferr
		}
		f.fail, f.err = fail, errChunkRead
		return f.err
	case int64(k) != want, ask > want && err != io.EOF:
		f.fail, f.err = changedRead, errChunkRead // shorter or longer than listed
		return f.err
	}
	f.off, f.n, f.eof = start, want, f.eof || ask > want
	if f.sum != nil && start <= f.hashed && start+want > f.hashed {
		f.sum.Write(f.buf[f.hashed-start : want])
		f.hashed = start + want
	}
	if err := f.r.maybeYield(); err != nil {
		f.err = err
		return err
	}
	return nil
}

func (f *chunkFile) holds(off int64) bool { return off >= f.off && off < f.off+f.n }

// ReadAt reads the file through the window.
func (f *chunkFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("content: read an archive file at negative offset %d", off)
	}
	n := 0
	for n < len(p) {
		if f.err != nil {
			return n, f.err
		}
		o := off + int64(n)
		if o >= f.size {
			return n, io.EOF
		}
		if !f.holds(o) {
			if err := f.fill(o); err != nil {
				return n, err
			}
		}
		n += copy(p[n:], f.buf[o-f.off:f.n])
	}
	return n, nil
}

// Read reads the file sequentially from its start, and confirms its end
// before it returns io.EOF.
func (f *chunkFile) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !f.holds(f.pos) {
		if f.pos >= f.size {
			if !f.eof {
				if err := f.fill(f.size); err != nil {
					return 0, err
				}
			}
			return 0, io.EOF
		}
		if err := f.fill(f.pos); err != nil {
			return 0, err
		}
	}
	k := copy(p, f.buf[f.pos-f.off:f.n])
	f.pos += int64(k)
	return k, nil
}

// digest is the whole file's SHA-256 when every listed byte went through
// sum in order and the end of the file was confirmed; nil otherwise.
func (f *chunkFile) digest() []byte {
	if f.sum == nil || f.err != nil || f.hashed != f.size || !f.eof {
		return nil
	}
	return f.sum.Sum(nil)
}

// stopErr reports the error that ended an archive read when it was the
// file's own: a failure (fail) or the job's end (err).
func (f *chunkFile) stopErr() (*failure, error) {
	if f.fail != nil {
		return f.fail, nil
	}
	if f.err != nil && !errors.Is(f.err, errChunkRead) {
		return nil, f.err
	}
	return nil, nil
}
