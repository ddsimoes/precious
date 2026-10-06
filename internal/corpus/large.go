package corpus

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

// The large-file cases: how the two files of one pair compare.
const (
	// LargeEqualSamples: the three 64 KiB samples (at 0, size/2 - 32 KiB,
	// and size - 64 KiB) are equal, and the content differs between them.
	LargeEqualSamples = "equal_samples"
	// LargeDifferentSamples: the middle sample differs.
	LargeDifferentSamples = "different_samples"
	// LargeIdentical: the content is identical.
	LargeIdentical = "identical"
)

// LargeFile is a synthfs-only fixture for the hashing tests (R2 design D19):
// a file of at least 16 MiB, too large for the written corpus. synthfs
// generates its content from Seed, then overwrites the Patches, so a file
// costs no memory until it is read. The fixtures come in pairs, one pair per
// case; the files of a pair have one size, and no two pairs share a size.
type LargeFile struct {
	Name    string
	Case    string // LargeEqualSamples, LargeDifferentSamples, or LargeIdentical
	Size    int64
	Seed    uint64
	Patches []LargePatch
	// SHA256 is the content's digest, in hex.
	SHA256 string
}

// LargePatch overwrites Data at Off.
type LargePatch struct {
	Off  int64
	Data []byte
}

const mib = 1 << 20

// largeFiles are the fixtures without their digests.
func largeFiles() []LargeFile {
	patch := func(off int64, label string) []LargePatch {
		return []LargePatch{{Off: off, Data: random(label, 4096)}}
	}
	const eq, diff, same = 16 * mib, 17 * mib, 18 * mib
	return []LargeFile{
		{Name: "amostras_iguais_a.bin", Case: LargeEqualSamples, Size: eq, Seed: 1601},
		// The patch lies between the first and the middle sample.
		{Name: "amostras_iguais_b.bin", Case: LargeEqualSamples, Size: eq, Seed: 1601, Patches: patch(eq/4, "large equal samples")},
		{Name: "amostras_diferentes_a.bin", Case: LargeDifferentSamples, Size: diff, Seed: 1701},
		// The patch lies inside the middle sample.
		{Name: "amostras_diferentes_b.bin", Case: LargeDifferentSamples, Size: diff, Seed: 1701, Patches: patch(diff/2, "large different samples")},
		{Name: "identico_a.bin", Case: LargeIdentical, Size: same, Seed: 1801},
		{Name: "identico_b.bin", Case: LargeIdentical, Size: same, Seed: 1801},
	}
}

// LargeFiles returns the large-file fixtures with their digests. The digests
// are computed once per process, by reading the files through synthfs.
func LargeFiles() []LargeFile {
	files := largeOnce()
	out := make([]LargeFile, len(files))
	copy(out, files)
	return out
}

var largeOnce = sync.OnceValue(func() []LargeFile {
	files := largeFiles()
	f := synthfs.New()
	addLarge(f.Root("/large"), time.Time{}, files)
	dir, err := f.OpenRoot("/large")
	if err == nil {
		defer dir.Close()
		buf := make([]byte, mib)
		for i := range files {
			if files[i].SHA256, err = digest(dir, files[i].Name, buf); err != nil {
				break
			}
		}
	}
	if err != nil {
		panic(err) // synthfs reads never fail here
	}
	return files
})

// digest returns the SHA-256 of the named file of dir, in hex.
func digest(dir fsaccess.Dir, name string, buf []byte) (string, error) {
	info, err := dir.Lstat([]byte(name))
	if err != nil {
		return "", err
	}
	file, err := dir.OpenFile([]byte(name), info)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	for off := int64(0); ; {
		n, err := file.ReadAt(buf, off)
		h.Write(buf[:n])
		off += int64(n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AddLargeFiles adds the large-file fixtures to the synthfs folder dir, with
// modification time mtime, and returns them. A hashing test reads them
// through its source like any file: in each pair, equal_samples must be read
// in full and found different, different_samples must be told apart by its
// samples alone, and identical must form a duplicate group.
func AddLargeFiles(dir *synthfs.Node, mtime time.Time) []LargeFile {
	files := LargeFiles()
	addLarge(dir, mtime, files)
	return files
}

func addLarge(dir *synthfs.Node, mtime time.Time, files []LargeFile) {
	for _, lf := range files {
		n := dir.File(lf.Name, lf.Size, mtime).Seed(lf.Seed)
		for _, p := range lf.Patches {
			n.Patch(p.Off, p.Data)
		}
	}
}
