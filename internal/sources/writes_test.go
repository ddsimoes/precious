package sources

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// r3 task 1.6: the reasons writes are unavailable, in their order.
func TestWritesUnavailable(t *testing.T) {
	ro := ext4Caps
	ro.ReadOnly = true
	noFlag := ext4Caps
	noFlag.NoReplaceRename = false
	roNoFlag := noFlag
	roNoFlag.ReadOnly = true
	for _, tc := range []struct {
		caps        fsaccess.Capabilities
		allowWrites bool
		want        string
	}{
		{ext4Caps, true, ""},
		{ext4Caps, false, "forbidden_by_config"},
		{roNoFlag, false, "forbidden_by_config"},
		{ro, true, "read_only"},
		{roNoFlag, true, "read_only"},
		{noFlag, true, "no_replace_rename"},
		{fsaccess.UnknownCapabilities(false), true, "no_replace_rename"},
	} {
		// The owner's permission does not change the answer.
		for _, enabled := range []bool{false, true} {
			src := Source{Caps: tc.caps, WriteEnabled: enabled}
			if got := WritesUnavailable(src, tc.allowWrites); got != tc.want {
				t.Errorf("WritesUnavailable(%+v, enabled %v, allow %v) = %q, want %q",
					tc.caps, enabled, tc.allowWrites, got, tc.want)
			}
		}
	}
}

// CheckWrites answers unknown_source, source_offline, writes_unavailable,
// and writes_disabled, in that order, from the source as recorded, in a
// read or inside a transaction.
func TestCheckWrites(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "Fotos")
	if src.WriteEnabled || e.get(src.ID).WriteEnabled {
		t.Fatal("a new source has writes on")
	}
	check := func(id domain.SourceID, allow bool, want domain.ErrorCode) {
		t.Helper()
		err := CheckWrites(ctx, e.st.Reader(), id, allow)
		if want == "" {
			if err != nil {
				t.Fatalf("CheckWrites(%s, allow %v) = %v, want nil", id, allow, err)
			}
			return
		}
		if code := domain.CodeOf(err); err == nil || code != want {
			t.Fatalf("CheckWrites(%s, allow %v) = %v, want %s", id, allow, err, want)
		}
	}
	setEnabled := func(on bool) {
		t.Helper()
		if _, err := e.st.Writer().Exec(`UPDATE sources SET write_enabled = ? WHERE id = ?`, on, string(src.ID)); err != nil {
			t.Fatal(err)
		}
	}

	check("nada", true, domain.CodeUnknownSource)
	check(src.ID, true, domain.CodeWritesDisabled)
	check(src.ID, false, domain.CodeWritesUnavailable)

	setEnabled(true)
	if !e.get(src.ID).WriteEnabled {
		t.Fatal("write_enabled = 1 does not read as WriteEnabled")
	}
	check(src.ID, true, "")
	check(src.ID, false, domain.CodeWritesUnavailable)
	err := e.st.Write(ctx, func(tx *sql.Tx) error { return CheckWrites(ctx, tx, src.ID, true) })
	if err != nil {
		t.Fatalf("CheckWrites inside a transaction = %v", err)
	}

	// The filesystem as last recorded: read-only, then without the flag.
	ro := ext4Caps
	ro.ReadOnly = true
	e.fs.SetCapabilities(dev, ro)
	e.refresh()
	if got := WritesUnavailable(e.get(src.ID), true); got != "read_only" {
		t.Fatalf("WritesUnavailable on a read-only source = %q", got)
	}
	check(src.ID, true, domain.CodeWritesUnavailable)
	noFlag := ext4Caps
	noFlag.NoReplaceRename = false
	e.fs.SetCapabilities(dev, noFlag)
	e.refresh()
	if got := WritesUnavailable(e.get(src.ID), true); got != "no_replace_rename" {
		t.Fatalf("WritesUnavailable without the no-replace rename = %q", got)
	}
	check(src.ID, true, domain.CodeWritesUnavailable)

	// Offline comes before every write reason.
	e.fs.SetCapabilities(dev, ext4Caps)
	e.refresh()
	check(src.ID, true, "")
	e.fs.Unmount(dev)
	e.refresh()
	if st := e.get(src.ID).State; st == StateOnline {
		t.Fatalf("state after unmount = %s", st)
	}
	check(src.ID, true, domain.CodeSourceOffline)
	check(src.ID, false, domain.CodeSourceOffline)
	setEnabled(false)
	check(src.ID, true, domain.CodeSourceOffline)
}
