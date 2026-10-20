package index

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/rules"
)

// r5 H4: ApplyModTime alone leaves every folder's newest and oldest times
// and by_year as a full refold of the source would: through a time moving
// off a folder's newest or oldest end (held one folder down, or by a file),
// to and from an unknown time, a folder's only file losing and regaining its
// time, a time staying in its year, a file in the quarantine (which the
// root's fold leaves out, though it holds the oldest time), a file at the
// top, and a folder holding a missing file and a link. A rescan afterwards
// writes nothing.
func TestR5ReviewApplyModTimeFoldsLikeARefold(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	y := func(year int) time.Time { return time.Date(year, 6, 1, 12, 0, 0, 0, time.UTC) }
	a := root.Dir("A")
	a.File("a1.jpg", 10, y(2004))
	a.File("a2.jpg", 20, y(2006))
	a.File("a3.jpg", 30, time.Unix(0, 340_000_000))
	sub := a.Dir("sub")
	sub.File("s1.jpg", 40, y(2001))
	sub.File("s2.jpg", 50, y(2010))
	sub.Dir("deep").File("d1.jpg", 60, y(1999))
	root.Dir("B").File("b1.jpg", 70, y(2008))
	root.File("b.jpg", 80, y(2005))
	root.Dir(QuarantineName).Dir("1").File("q.jpg", 90, y(1990))
	c := root.Dir("C")
	c.File("c1.jpg", 100, y(2003))
	c.File("gone.jpg", 110, y(1980))
	c.Symlink("link", "c1.jpg")
	e.scan("disk")
	c.Remove("gone.jpg")
	e.scan("disk")

	d := disk{t: t, e: e, root: "/disk"}
	rf := NewRefolder(rules.Default())
	ids := map[string]int64{}
	var files []domain.EntryID
	for p, r := range e.entries("disk") {
		ids[p] = r.ID
		if r.Kind == string(domain.EntryFile) && r.State == "present" {
			files = append(files, domain.EntryID(r.ID))
		}
	}
	for _, step := range []struct {
		path string
		to   time.Time
	}{
		{"A/sub/deep/d1.jpg", y(2020)},             // off sub's, A's, and the root's oldest end
		{"B/b1.jpg", time.Unix(0, 0)},              // B's only time becomes unknown
		{"B/b1.jpg", y(2030)},                      // and comes back, the root's newest
		{"A/a3.jpg", y(2004).Add(time.Hour)},       // from unknown into a year A counts
		{"A/sub/s2.jpg", y(2010).Add(time.Minute)}, // within its year, sub's newest
		{"A/sub/deep/d1.jpg", y(1995)},             // back below every oldest end
		{"B/b1.jpg", y(2012)},                      // off the root's newest end
		{QuarantineName + "/1/q.jpg", y(1991)},     // left out of the root's fold
		{"b.jpg", time.Unix(0, 0)},                 // a file at the top
		{"C/c1.jpg", y(2000)},                      // beside a missing file and a link
		{"A/a1.jpg", y(2004)},                      // its own time again: nothing changes
	} {
		node(t, root, step.path).ModTime(step.to)
		m := ModTime{Source: "disk", Entry: domain.EntryID(ids[step.path]), Facts: d.facts(step.path)}
		if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
			return ApplyModTime(context.Background(), tx, m)
		}); err != nil {
			t.Fatalf("%s to %v: %v", step.path, step.to, err)
		}
		applied := e.entries("disk")
		if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
			return rf.Refold(context.Background(), tx, "disk", files)
		}); err != nil {
			t.Fatal(err)
		}
		if diff := diffRows(applied, e.entries("disk")); len(diff) > 0 {
			t.Fatalf("after %s to %v, a refold changed:\n%s", step.path, step.to, strings.Join(diff, "\n"))
		}
	}
	rows := e.entries("disk")
	if r := get(t, rows, ""); r.Oldest.Int64 != y(1995).UnixNano() || r.Newest.Int64 != y(2012).UnixNano() {
		t.Errorf("the root spans %v to %v, want 1995 to 2012 without the quarantine", r.Oldest, r.Newest)
	}
	rescanWritesNothing(t, e, "disk")
}
