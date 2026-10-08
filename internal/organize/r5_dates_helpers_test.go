package organize

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"precious/internal/dates/datestest"
	"precious/internal/domain"
)

// Helpers of the r5 date plans' tests (tasks 2.10–2.12).

// at is a time of day in UTC, the tests' zone.
func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// seed reads every media file of src and derives its date, as a complete
// media job leaves them (before its cameras pass).
func (w *world) seed(src domain.SourceID) {
	w.t.Helper()
	datestest.Seed(w.t, w.st, w.srcs, w.dates, src)
}

// shift records the owner's shift of an entry's date by s seconds.
func (w *world) shift(id string, s int64) {
	w.t.Helper()
	w.exec(`INSERT INTO date_corrections (entry_id, kind, shift_s, batch_id, created_at) VALUES (?, 'shift', ?, 'test', 0)`,
		id, s)
}

// setDate records the owner's date of an entry, at local's precision.
func (w *world) setDate(id, local string) {
	w.t.Helper()
	w.exec(`INSERT INTO date_corrections (entry_id, kind, set_local, batch_id, created_at) VALUES (?, 'set', ?, 'test', 0)`,
		id, local)
}

// datePlan sends a date plan, which must answer 201; it returns its action
// and every item, and decodes its summary into sum.
func (w *world) datePlan(name, body string, sum any) (actionJSON, []itemJSON) {
	w.t.Helper()
	out := w.ok(http.StatusCreated, name, body)
	var res struct {
		PlanResponse
		Summary json.RawMessage `json:"summary"`
	}
	decode(w.t, out, &res)
	if res.Summary == nil {
		w.t.Fatalf("%s answered no summary: %s", name, out)
	}
	decode(w.t, string(res.Summary), sum)
	items := res.Items
	if res.NextCursor != nil {
		items = append(items, w.items(res.Action.ID, "cursor="+*res.NextCursor)...)
	}
	return res.Action, items
}

// folders is a folder_ids target body of list, with extra JSON fields.
func folders(extra string, list ...string) string { return targetBody("folder_ids", extra, list) }

// entries is an entry_ids target body of list, with extra JSON fields.
func entries(extra string, list ...string) string { return targetBody("entry_ids", extra, list) }

func targetBody(field, extra string, list []string) string {
	body := `{"` + field + `":` + ids(list...)
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// dest is the destination_id field of folder path of src.
func (w *world) dest(src domain.SourceID, path string) string {
	w.t.Helper()
	return fmt.Sprintf(`"destination_id":%q`, w.id(src, path))
}

// mtimeOf is an entry's index modification time.
func (w *world) mtimeOf(id string) time.Time {
	w.t.Helper()
	var ns int64
	if err := w.st.Reader().QueryRow(`SELECT mtime_ns FROM entries WHERE id = ?`, id).Scan(&ns); err != nil {
		w.t.Fatalf("mtime of %s: %v", id, err)
	}
	return time.Unix(0, ns).UTC()
}

// byPath returns the item whose from path is p.
func byPath(t *testing.T, items []itemJSON, p string) itemJSON {
	t.Helper()
	for _, it := range items {
		if it.From != nil && it.From.Path == p {
			return it
		}
	}
	t.Fatalf("no item from %q in:\n  %s", p, strings.Join(summaries(items), "\n  "))
	return itemJSON{}
}
