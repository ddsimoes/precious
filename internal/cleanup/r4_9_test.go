package cleanup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"precious/internal/fsaccess/synthfs"
)

// r49Get reads path, answering the recorder.
func (w *world) r49Get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

// R4.9: the export of a drafted cleanup plan with a blocked item has the
// header and one row per item in seq order, the blocked one with reason
// holds_kept; cells a spreadsheet would read as formulas are neutralized
// and quotes are doubled.
func TestR4_9ExportingAPlan(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2012, 6, 7, 8, 9, 10, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.File("=soma.xls", 10, at).Seed(1)
		root.File("+mais.txt", 20, at).Seed(2)
		root.File("-menos.txt", 30, at).Seed(3)
		root.File("@arroba.txt", 40, at).Seed(4)
		root.File(`aspas "x".txt`, 50, at).Seed(5)
		p := root.Dir("Pasta")
		p.File("fica.doc", 60, at).Seed(6)
		p.File("vai.doc", 70, at).Seed(7)
		root.File("fora.txt", 80, at).Seed(8)
	})
	for _, p := range []string{"=soma.xls", "+mais.txt", "-menos.txt", "@arroba.txt", `aspas "x".txt`, "Pasta"} {
		w.decide("casa", p, "discard")
	}
	w.decide("casa", "Pasta/fica.doc", "keep")

	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	id := p.Action.ID
	q := ".precious-quarantine/" + id
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+q,
		"mkdir planned -> "+q+"/1",
		"rename planned +mais.txt -> "+q+"/1/+mais.txt",
		"record planned -> "+q+"/1.json",
		"mkdir planned -> "+q+"/2",
		"rename planned -menos.txt -> "+q+"/2/-menos.txt",
		"record planned -> "+q+"/2.json",
		"mkdir planned -> "+q+"/3",
		"rename planned =soma.xls -> "+q+"/3/=soma.xls",
		"record planned -> "+q+"/3.json",
		"mkdir planned -> "+q+"/4",
		"rename planned @arroba.txt -> "+q+"/4/@arroba.txt",
		"record planned -> "+q+"/4.json",
		"rename blocked holds_kept Pasta",
		"mkdir planned -> "+q+"/5",
		`rename planned aspas "x".txt -> `+q+`/5/aspas "x".txt`,
		"record planned -> "+q+"/5.json",
	)

	rec := w.r49Get("/api/history/" + id + "/export.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rec.Code, rec.Body)
	}
	for name, want := range map[string]string{
		"Content-Type":        "text/csv; charset=utf-8",
		"Content-Disposition": `attachment; filename="precious-cleanup-` + id + `.csv"`,
		"Cache-Control":       "no-store",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	want := strings.Join([]string{
		"path,size,operation,state,reason",
		`".precious-quarantine","0","create_folder","planned",""`,
		`"` + q + `","0","create_folder","planned",""`,
		`"` + q + `/1","0","create_folder","planned",""`,
		`"'+mais.txt","20","quarantine","planned",""`,
		`"` + q + `/1.json","0","write_record","planned",""`,
		`"` + q + `/2","0","create_folder","planned",""`,
		`"'-menos.txt","30","quarantine","planned",""`,
		`"` + q + `/2.json","0","write_record","planned",""`,
		`"` + q + `/3","0","create_folder","planned",""`,
		`"'=soma.xls","10","quarantine","planned",""`,
		`"` + q + `/3.json","0","write_record","planned",""`,
		`"` + q + `/4","0","create_folder","planned",""`,
		`"'@arroba.txt","40","quarantine","planned",""`,
		`"` + q + `/4.json","0","write_record","planned",""`,
		`"Pasta","130","quarantine","blocked","holds_kept"`,
		`"` + q + `/5","0","create_folder","planned",""`,
		`"aspas ""x"".txt","50","quarantine","planned",""`,
		`"` + q + `/5.json","0","write_record","planned",""`,
		"",
	}, "\r\n")
	if got := rec.Body.String(); got != want {
		t.Fatalf("export:\n%s\nwant:\n%s", got, want)
	}

	// Exporting reads only: the plan is still planned and unchanged.
	if a := w.action(id); a.State != "planned" || a.Entries["planned"] != 5 || a.Entries["blocked"] != 1 {
		t.Fatalf("action after export %+v", a)
	}

	// An unknown action answers 404 with the JSON error envelope.
	rec = w.r49Get("/api/history/999/export.csv")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		rec.Body.String() != `{"error":{"code":"not_found","message":"action \"999\" not found"}}`+"\n" {
		t.Fatalf("unknown action export = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}
