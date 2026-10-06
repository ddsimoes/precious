package sources

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"precious/internal/config"
	"precious/internal/domain"
)

func names(items []PickerItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}

// Without configured roots the Linux defaults are the service account's
// home, /media, /mnt, /run/media, and /srv, those that exist, resolved.
func TestDefaultAllowedRoots(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux defaults")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, p := range []string{home, "/media", "/mnt", "/run/media", "/srv"} {
		if r, err := filepath.EvalSymlinks(p); err == nil && !slices.Contains(want, r) {
			if fi, err := os.Stat(r); err == nil && fi.IsDir() {
				want = append(want, r)
			}
		}
	}
	got, err := allowedRoots(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("default roots = %v, want %v", got, want)
	}
}

// Configured roots replace the defaults, resolved through symlinks; one that
// does not exist fails New.
func TestConfiguredRootsReplaceDefaults(t *testing.T) {
	e := newEnv(t)
	link := filepath.Join(realDir(t), "tank")
	if err := os.Symlink(e.base, link); err != nil {
		t.Fatal(err)
	}
	svc := e.service(link)
	roots, err := svc.PickerRoots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].Path != e.base || roots[0].Name != filepath.Base(e.base) {
		t.Fatalf("roots = %+v, want only %s", roots, e.base)
	}
	if _, err := New(e.st, e.fs, config.Sources{AllowedRoots: []string{filepath.Join(e.base, "missing")}}, fixedClock{testNow}); err == nil {
		t.Fatal("New with a missing allowed root succeeded")
	}
	// A folder under another root is refused.
	other := realDir(t)
	_, err = svc.PickerChildren(context.Background(), svc.handle(other))
	wantCode(t, err, domain.CodeOutsideAllowedRoots)
}

// A listing holds the folder's child folders by name, each with a handle
// that lists it in turn; files and symlinks are never listed.
func TestPickerListsFoldersOnly(t *testing.T) {
	e := newEnv(t)
	for _, d := range []string{"b", "a", "a/inner", "c d"} {
		mkdir(t, filepath.Join(e.base, d))
	}
	if err := os.WriteFile(filepath.Join(e.base, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.base, "a"), filepath.Join(e.base, "link-to-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(e.base, "etc")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	roots, err := e.svc.PickerRoots(ctx)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots = %+v, %v", roots, err)
	}
	l, err := e.svc.PickerChildren(ctx, roots[0].Handle)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l.Children); !slices.Equal(got, []string{"a", "b", "c d"}) || l.Truncated || l.Entry.Path != e.base {
		t.Fatalf("listing = %+v, want folders a, b, c d", l)
	}
	inner, err := e.svc.PickerChildren(ctx, l.Children[0].Handle)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(inner.Children); !slices.Equal(got, []string{"inner"}) || inner.Entry.Path != filepath.Join(e.base, "a") {
		t.Fatalf("listing of a = %+v", inner)
	}
}

// A folder that is a source's root is marked so.
func TestPickerMarksSources(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", "FOTOS"), "Fotos", "Other")
	e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	l, err := e.svc.PickerChildren(context.Background(), e.svc.handle(filepath.Join(e.base, "usb")))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range l.Children {
		got[c.Name] = c.IsSource
		if c.VolumeLabel != "FOTOS" || c.FSType != "ext4" {
			t.Errorf("child %+v lacks its volume", c)
		}
	}
	if !got["Fotos"] || got["Other"] || l.Entry.IsSource {
		t.Fatalf("is_source = %v (entry %v), want only Fotos", got, l.Entry.IsSource)
	}
}

// A handle whose signature is not this process's, or that is not a handle
// at all, is invalid_request.
func TestPickerForgedHandle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	good := e.svc.handle(e.base)
	forged := []string{
		"",
		e.base,
		"/etc",
		`C:\Windows`,
		base64.RawURLEncoding.EncodeToString([]byte(e.base)) + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		good[:len(good)-2] + "AA",
		base64.RawURLEncoding.EncodeToString([]byte("/etc")) + good[len(base64.RawURLEncoding.EncodeToString([]byte(e.base))):],
		good + "x",
	}
	for _, h := range forged {
		_, err := e.svc.PickerChildren(ctx, h)
		wantCode(t, err, domain.CodeInvalidRequest)
	}
}

// A symlink inside an allowed root is never listed; a handle naming one is
// refused: 403 when it leads outside every root, 400 when inside.
func TestPickerSymlinkPointingOutside(t *testing.T) {
	e := newEnv(t)
	outside := realDir(t)
	mkdir(t, filepath.Join(e.base, "inside"))
	if err := os.Symlink(outside, filepath.Join(e.base, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.base, "inside"), filepath.Join(e.base, "in")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l, err := e.svc.PickerChildren(ctx, e.svc.handle(e.base))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l.Children); !slices.Equal(got, []string{"inside"}) {
		t.Fatalf("children = %v, want inside only", got)
	}
	_, err = e.svc.PickerChildren(ctx, e.svc.handle(filepath.Join(e.base, "out")))
	wantCode(t, err, domain.CodeOutsideAllowedRoots)
	_, err = e.svc.PickerChildren(ctx, e.svc.handle(filepath.Join(e.base, "out", "x")))
	wantCode(t, err, domain.CodeInvalidRequest) // does not exist
	_, err = e.svc.PickerChildren(ctx, e.svc.handle(filepath.Join(e.base, "in")))
	wantCode(t, err, domain.CodeInvalidRequest)
	_, err = e.svc.PickerChildren(ctx, e.svc.handle(outside))
	wantCode(t, err, domain.CodeOutsideAllowedRoots)
}

// Handles are signed with a key made at start: after a restart they are
// refused.
func TestPickerRestartInvalidatesHandles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	roots, err := e.svc.PickerRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PickerChildren(ctx, roots[0].Handle); err != nil {
		t.Fatal(err)
	}
	restarted := e.service(e.base)
	_, err = restarted.PickerChildren(ctx, roots[0].Handle)
	wantCode(t, err, domain.CodeInvalidRequest)
}

// At most 1,000 child folders are listed; truncated says more exist.
func TestPickerTruncates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, tc := range []struct {
		n         int
		truncated bool
	}{{MaxPickerChildren, false}, {1500, true}} {
		dir := filepath.Join(e.base, fmt.Sprint(tc.n))
		for i := range tc.n {
			mkdir(t, filepath.Join(dir, fmt.Sprintf("d%04d", i)))
		}
		if err := os.WriteFile(filepath.Join(dir, "zz-file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		l, err := e.svc.PickerChildren(ctx, e.svc.handle(dir))
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Children) != MaxPickerChildren || l.Truncated != tc.truncated ||
			!slices.IsSortedFunc(l.Children, func(a, b PickerItem) int { return strings.Compare(a.Name, b.Name) }) {
			t.Fatalf("%d folders: %d children, truncated %v", tc.n, len(l.Children), l.Truncated)
		}
	}
}
