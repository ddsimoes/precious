package sources

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// MaxPickerChildren is the most child folders one picker listing returns.
const MaxPickerChildren = 1000

// pickerBatch is how many directory entries a listing reads at once.
const pickerBatch = 256

// PickerItem is one folder the picker offers. Handle is the only way to name
// it in a request; Path is for display only.
type PickerItem struct {
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	VolumeLabel string `json:"volume_label"`
	FSType      string `json:"fs_type"`
	// IsSource reports that the folder is the root of a source.
	IsSource bool `json:"is_source"`
}

// PickerListing is a folder with its child folders, at most
// MaxPickerChildren of them; Truncated reports that more exist.
type PickerListing struct {
	Entry     PickerItem   `json:"entry"`
	Children  []PickerItem `json:"children"`
	Truncated bool         `json:"truncated"`
}

// allowedRoots cleans and resolves the configured roots, or the platform
// defaults that exist when none are configured (design D5).
func allowedRoots(configured []string) ([]string, error) {
	var roots []string
	add := func(p string) {
		if !slices.Contains(roots, p) {
			roots = append(roots, p)
		}
	}
	if len(configured) == 0 {
		for _, p := range defaultRoots() {
			if r, err := resolveRoot(p); err == nil {
				add(r)
			}
		}
		return roots, nil
	}
	for _, p := range configured {
		r, err := resolveRoot(p)
		if err != nil {
			return nil, fmt.Errorf("sources: allowed root %q: %w", p, err)
		}
		add(r)
	}
	return roots, nil
}

func resolveRoot(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", errors.New("not an absolute path")
	}
	r, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(r)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", errors.New("not a directory")
	}
	return r, nil
}

// handle is the opaque name of the canonical folder p:
// base64url(p) "." base64url(HMAC-SHA256(key, p)).
func (s *Service) handle(p string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p)) + "." + base64.RawURLEncoding.EncodeToString(s.mac([]byte(p)))
}

func (s *Service) mac(p []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(p)
	return h.Sum(nil)
}

var errBadHandle = domain.Errorf(domain.CodeInvalidRequest, "invalid or expired folder handle; pick the folder again")

// folder expands handle into the canonical folder it names (design D5): the
// signature must be this process's, the folder after resolving symlinks must
// lie inside an allowed root (else outside_allowed_roots), and it must be a
// directory reached without following a symlink. Anything else is
// invalid_request.
func (s *Service) folder(handle string) (string, error) {
	i := strings.LastIndexByte(handle, '.')
	if i < 0 {
		return "", errBadHandle
	}
	raw, err := base64.RawURLEncoding.DecodeString(handle[:i])
	if err != nil {
		return "", errBadHandle
	}
	sum, err := base64.RawURLEncoding.DecodeString(handle[i+1:])
	if err != nil || !hmac.Equal(sum, s.mac(raw)) {
		return "", errBadHandle
	}
	p := string(raw)
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", errBadHandle
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", domain.Errorf(domain.CodeInvalidRequest, "the folder %s can no longer be reached", domain.DisplayName(raw))
	}
	if !s.allowed(real) {
		return "", domain.Errorf(domain.CodeOutsideAllowedRoots, "the folder %s is outside every allowed root", domain.DisplayName(raw))
	}
	if real != p {
		return "", domain.Errorf(domain.CodeInvalidRequest, "the folder %s is reached through a symbolic link", domain.DisplayName(raw))
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return "", domain.Errorf(domain.CodeInvalidRequest, "%s is not a folder", domain.DisplayName(raw))
	}
	return p, nil
}

// allowed reports whether the resolved path p lies inside an allowed root.
func (s *Service) allowed(p string) bool {
	for _, r := range s.roots {
		if containsPath(r, p) {
			return true
		}
	}
	return false
}

// PickerRoots lists the allowed roots.
func (s *Service) PickerRoots(ctx context.Context) ([]PickerItem, error) {
	pc, err := s.pickerContext(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]PickerItem, len(s.roots))
	for i, r := range s.roots {
		items[i] = pc.item(s, r)
	}
	return items, nil
}

// PickerChildren lists the folder handle names and its child folders, by
// name: never files or symlinks, and at most MaxPickerChildren.
func (s *Service) PickerChildren(ctx context.Context, handle string) (PickerListing, error) {
	p, err := s.folder(handle)
	if err != nil {
		return PickerListing{}, err
	}
	names, truncated, err := childFolders(p)
	if err != nil {
		return PickerListing{}, domain.Wrap(domain.CodeInvalidRequest, err, "the folder %s cannot be listed", domain.DisplayName([]byte(p)))
	}
	pc, err := s.pickerContext(ctx)
	if err != nil {
		return PickerListing{}, err
	}
	out := PickerListing{Entry: pc.item(s, p), Children: make([]PickerItem, len(names)), Truncated: truncated}
	for i, name := range names {
		out.Children[i] = pc.item(s, filepath.Join(p, name))
	}
	return out, nil
}

// childFolders returns the names of up to MaxPickerChildren child folders of
// p, sorted, and whether more exist. Symlinks are not folders here.
func childFolders(p string) (names []string, truncated bool, err error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	for {
		batch, err := f.ReadDir(pickerBatch)
		for _, e := range batch {
			if !e.IsDir() {
				continue
			}
			if len(names) == MaxPickerChildren {
				slices.Sort(names)
				return names, true, nil
			}
			names = append(names, e.Name())
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	slices.Sort(names)
	return names, false, nil
}

// pickerContext is what describing picker items needs: the mount table and
// the sources' roots.
type pickerContext struct {
	mounts  []fsaccess.Mount
	sources map[sourceRoot]bool
}

type sourceRoot struct {
	kind    fsaccess.VolumeKind
	id, rel string
}

func (s *Service) pickerContext(ctx context.Context) (*pickerContext, error) {
	mounts, err := s.fs.Mounts()
	if err != nil {
		return nil, err
	}
	srcs, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	pc := &pickerContext{mounts: mounts, sources: make(map[sourceRoot]bool, len(srcs))}
	for _, src := range srcs {
		pc.sources[sourceRoot{src.Volume.Kind, src.Volume.ID, string(src.RelRoot)}] = true
	}
	return pc, nil
}

func (pc *pickerContext) item(s *Service, p string) PickerItem {
	name := filepath.Base(p)
	if name == string(filepath.Separator) || name == "." || filepath.VolumeName(p)+string(filepath.Separator) == p {
		name = p
	}
	it := PickerItem{Handle: s.handle(p), Name: domain.DisplayName([]byte(name)), Path: domain.DisplayName([]byte(p))}
	if m, ok := mountFor(pc.mounts, p); ok {
		it.VolumeLabel, it.FSType = m.Volume.Label, m.Volume.FSType
		it.IsSource = pc.sources[sourceRoot{m.Volume.Kind, m.Volume.ID, string(relRoot(m, p))}]
	}
	return it
}
