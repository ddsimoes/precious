package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/rules"
	"precious/internal/search"
	"precious/internal/sources"
)

// homeRes is the part of GET /api/home the quarantine changes.
type homeRes struct {
	Totals struct {
		Bytes int64 `json:"bytes"`
		Files int64 `json:"files"`
	} `json:"totals"`
	Decisions map[string]amountRes `json:"decisions"`
}

// inQuarantineRes is EntryDetail.in_quarantine.
type inQuarantineRes struct {
	PlanID        *string `json:"plan_id"`
	QuarantinedAt *string `json:"quarantined_at"`
	Original      *struct {
		Path    string `json:"path"`
		PathB64 []byte `json:"path_b64"`
	} `json:"original"`
}

// quarantinePlan is the cleanup plan the scenario quarantines under, and
// quarantinedAt the time its renames were done.
const quarantinePlan = 7

var quarantinedAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// quarantineIndex moves the entries at paths into the quarantine of source
// "corpus" in the index only, as the cleanup executor's outcomes do: the
// quarantine folder when absent, plan folder plan, and one <seq> folder per
// item (InsertFolder and Refold, as a mkdir), then each item into its
// <seq> under its own name (MoveEntry, Reinherit, and Refold of the item
// and the folder it left). Precious records the quarantine folder as its
// own. Items in withPlan get the done cleanup rename of action plan that
// moved them, at quarantinedAt; the others have an unknown origin (r4
// design D4). It returns the quarantine folder's ID.
func quarantineIndex(t *testing.T, w *contentWorld, plan int64, paths []string, withPlan map[string]bool) domain.EntryID {
	t.Helper()
	ctx := context.Background()
	rf := index.NewRefolder(rules.Default())
	var q domain.EntryID
	err := w.st.Write(ctx, func(tx *sql.Tx) error {
		mkdir := func(parent domain.EntryID, name string) (domain.EntryID, error) {
			id, err := index.InsertFolder(ctx, tx, index.NewFolder{Source: "corpus", Parent: parent, Name: []byte(name)})
			if err != nil {
				return 0, err
			}
			return id, rf.Refold(ctx, tx, "corpus", []domain.EntryID{id})
		}
		err := tx.QueryRow(`SELECT id FROM entries WHERE source_id = 'corpus' AND path = ?`,
			[]byte(index.QuarantineName)).Scan(&q)
		if errors.Is(err, sql.ErrNoRows) {
			q, err = mkdir(w.s.Root, index.QuarantineName)
		}
		if err != nil {
			return err
		}
		planDir, err := mkdir(q, fmt.Sprint(plan))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sources SET quarantine_entry_id = ? WHERE id = 'corpus'`, int64(q)); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO actions (id, kind, source_id, state, bulk, created_at, started_at, finished_at, ground)
			VALUES (?, 'cleanup', 'corpus', 'done', 1, 1, 1, ?, 'discard')`, plan, clock.Millis(quarantinedAt)); err != nil {
			return err
		}
		for i, p := range paths {
			seq := fmt.Sprint(i + 1)
			dir, err := mkdir(planDir, seq)
			if err != nil {
				return err
			}
			id := w.s.ID(p)
			var parent int64
			var name []byte
			if err := tx.QueryRow(`SELECT parent_id, name FROM entries WHERE id = ?`, int64(id)).Scan(&parent, &name); err != nil {
				return err
			}
			_, to, err := index.MoveEntry(ctx, tx, index.Move{Source: "corpus", Entry: id, NewParent: dir, NewName: name})
			if err != nil {
				return err
			}
			if err := decisions.Reinherit(ctx, tx, id); err != nil {
				return err
			}
			if err := rf.Refold(ctx, tx, "corpus", []domain.EntryID{id, domain.EntryID(parent)}); err != nil {
				return err
			}
			if !withPlan[p] {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name, from_path,
				to_parent, to_name, to_path, state, finished_at) VALUES (?, ?, 'rename', ?, ?, ?, ?, ?, ?, ?, 'done', ?)`,
				plan, 3*i+2, int64(id), parent, name, []byte(p), int64(dir), name, to,
				clock.Millis(quarantinedAt)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("quarantine %q: %v", paths, err)
	}
	return q
}

// searchAll reads every page of a search.
func (w *contentWorld) searchAll(t *testing.T, query string) []contentRow {
	t.Helper()
	var out []contentRow
	cursor := ""
	for {
		target := "/api/search?limit=1000&" + query
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		var p contentPage
		w.get(t, target, 200, &p)
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out
		}
		cursor = *p.NextCursor
	}
}

// The scenario "Home after quarantining a folder" (r4 inventory-explorer
// spec, design D2, D13, D15) through the read API: the corpus, hashed and
// related, with the discarded folder Documentos and the discarded archive
// eMule0.47c-Installer.zip moved into the quarantine's rows. Home's totals
// and its discarded bytes drop by exactly what moved, which its quarantine
// bucket holds; the Map, Search (pages, counts, and select-all), and copy
// counts leave the quarantine out, while a search that asks for it finds
// it alone; a quarantined entry's detail tells where it came from; and the
// sources JSON reports the quarantine and a name taken by the owner.
func TestR4HomeAfterQuarantiningAFolder(t *testing.T) {
	w := newContentWorld(t)
	const (
		folder    = "Documentos"
		archive   = "Downloads/eMule0.47c-Installer.zip"
		curriculo = "Documentos/curriculo.doc"
		backupCV  = "Backup_PC_2004/C/Documents and Settings/Joao/Meus documentos/curriculo.doc"
		emule     = "Downloads/emule-0.47c/emule.exe"
	)
	cmds := w.commands(t)
	for _, p := range []string{folder, archive} {
		if code, body := w.post(t, cmds, "set-decision", fmt.Sprintf(`{"entry_id":%q,"decision":"discard"}`, w.id(p))); code != 200 {
			t.Fatalf("discard %s: %d %s", p, code, body)
		}
	}
	var before homeRes
	w.get(t, "/api/home", 200, &before)
	moved := amountRes{}
	for _, p := range []string{folder, archive} {
		r := w.detail(t, w.id(p)).Entry
		moved.Files += r.TotalFiles
		moved.Bytes += r.TotalBytes
	}
	if before.Decisions["discard"] != moved || before.Decisions["quarantine"] != (amountRes{}) {
		t.Fatalf("before: discarded %+v, quarantine %+v; want %+v discarded", before.Decisions["discard"],
			before.Decisions["quarantine"], moved)
	}
	copiesBefore := map[string]int64{}
	for _, p := range []string{backupCV, emule} {
		c := w.detail(t, w.id(p)).Entry.Copies
		if c == nil {
			t.Fatalf("%s has no copies count", p)
		}
		copiesBefore[p] = *c
	}
	if copiesBefore[backupCV] != 4 || copiesBefore[emule] != 2 {
		t.Fatalf("copies before: %v", copiesBefore)
	}
	nameQuery := "source=corpus&name=curriculo"
	var countBefore struct {
		Count int `json:"count"`
	}
	w.get(t, "/api/search?count=only&"+nameQuery, 200, &countBefore)
	inFolder := 0
	for _, r := range w.searchAll(t, nameQuery) {
		if index.IsQuarantinePath(r.PathB64) {
			t.Fatalf("%q is in the quarantine before quarantining", r.Path)
		}
		if len(r.PathB64) > len(folder) && string(r.PathB64[:len(folder)+1]) == folder+"/" {
			inFolder++
		}
	}
	if inFolder == 0 {
		t.Fatal("no curriculo below Documentos")
	}

	q := quarantineIndex(t, w, quarantinePlan, []string{folder, archive}, map[string]bool{folder: true})
	var inQ int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM entries e WHERE e.source_id = 'corpus' AND ` +
		index.InQuarantine("e")).Scan(&inQ); err != nil {
		t.Fatal(err)
	}

	t.Run("home", func(t *testing.T) {
		for _, target := range []string{"/api/home", "/api/home?source=corpus"} {
			var after homeRes
			w.get(t, target, 200, &after)
			if after.Totals.Bytes != before.Totals.Bytes-moved.Bytes || after.Totals.Files != before.Totals.Files-moved.Files {
				t.Errorf("%s: totals %+v, want %d bytes and %d files less than %+v", target, after.Totals, moved.Bytes,
					moved.Files, before.Totals)
			}
			if after.Decisions["discard"] != (amountRes{}) || after.Decisions["quarantine"] != moved {
				t.Errorf("%s: discarded %+v, in quarantine %+v; want none and %+v", target, after.Decisions["discard"],
					after.Decisions["quarantine"], moved)
			}
			for _, d := range []string{"undecided", "keep", "later"} {
				if after.Decisions[d] != before.Decisions[d] {
					t.Errorf("%s: %s %+v, before %+v", target, d, after.Decisions[d], before.Decisions[d])
				}
			}
		}
	})

	t.Run("map", func(t *testing.T) {
		root := w.detail(t, w.s.Root.String()).Entry
		var sum int64
		for _, r := range w.allChildren(t, w.s.Root.String(), "&sort=name", 7) {
			if index.IsQuarantinePath(r.PathB64) || string(r.PathB64) == folder {
				t.Errorf("the top lists %q", r.Path)
			}
			sum += r.TotalBytes
		}
		if sum != root.TotalBytes {
			t.Errorf("the top's children hold %d bytes, the top %d", sum, root.TotalBytes)
		}
		var tm struct {
			Items []row `json:"items"`
			Other struct {
				Count int64 `json:"count"`
				Bytes int64 `json:"bytes"`
			} `json:"other"`
		}
		w.get(t, fmt.Sprintf("/api/entries/%s/treemap", w.s.Root), 200, &tm)
		var area int64
		for _, r := range tm.Items {
			if index.IsQuarantinePath(r.PathB64) {
				t.Errorf("the top's treemap lays out %q", r.Path)
			}
			area += r.TotalBytes
		}
		if area+tm.Other.Bytes != root.TotalBytes {
			t.Errorf("the top's treemap lays out %d bytes, the top holds %d", area+tm.Other.Bytes, root.TotalBytes)
		}
		// Below the quarantine nothing is listed, nor are the members of a
		// quarantined archive.
		for _, ref := range []string{q.String(), w.id(folder), w.id(archive)} {
			if rows := w.allChildren(t, ref, "", 100); len(rows) != 0 {
				t.Errorf("children of %s: %d rows", ref, len(rows))
			}
			w.get(t, fmt.Sprintf("/api/entries/%s/treemap", ref), 200, &tm)
			if len(tm.Items) != 0 || tm.Other.Count != 0 || tm.Other.Bytes != 0 {
				t.Errorf("treemap of %s: %d items, other %+v", ref, len(tm.Items), tm.Other)
			}
		}
	})

	t.Run("search", func(t *testing.T) {
		rows := w.searchAll(t, nameQuery)
		for _, r := range rows {
			if index.IsQuarantinePath(r.PathB64) {
				t.Errorf("the search finds %q", r.Path)
			}
		}
		var count struct {
			Count int `json:"count"`
		}
		w.get(t, "/api/search?count=only&"+nameQuery, 200, &count)
		if len(rows) != countBefore.Count-inFolder || count.Count != len(rows) {
			t.Errorf("%d rows, count %d; want %d", len(rows), count.Count, countBefore.Count-inFolder)
		}
		// Every driver leaves the quarantine out: a whole source, a decision,
		// the root's Within, a name, and the duplicate filter.
		for _, query := range []string{"source=corpus", "decision=discard", fmt.Sprintf("within=%s", w.s.Root),
			"name=c", "dup=copies", "dup=unique&source=corpus", "dup=unchecked"} {
			got := w.searchAll(t, query)
			for _, r := range got {
				if index.IsQuarantinePath(r.PathB64) {
					t.Errorf("%s finds %q", query, r.Path)
				}
			}
			w.get(t, "/api/search?count=only&"+query, 200, &count)
			if count.Count != len(got) {
				t.Errorf("%s: %d rows, count %d", query, len(got), count.Count)
			}
		}
		if got := w.searchAll(t, "decision=discard"); len(got) != 0 {
			t.Errorf("decision=discard finds %d rows", len(got))
		}
		// Select all resolves the same rows.
		code, body := w.post(t, cmds, "create-selection", `{"query":{"source":"corpus","name":"curriculo"}}`)
		var sel struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal([]byte(body), &sel); code != 201 || err != nil || sel.Count != len(rows) {
			t.Errorf("create-selection: %d %s, want %d entries", code, body, len(rows))
		}
		// Asking for the quarantine finds it alone, every shape.
		ctx := context.Background()
		for _, sq := range []search.Query{{Source: "corpus"}, {Within: &w.s.Root}, {Name: "curriculo"}} {
			sq.InQuarantine = true
			ids, err := search.Resolve(ctx, w.st.Reader(), sq, search.MaxResolve)
			if err != nil {
				t.Fatal(err)
			}
			n, _, err := search.Count(ctx, w.st.Reader(), sq)
			if err != nil {
				t.Fatal(err)
			}
			res, err := search.Page(ctx, w.st.Reader(), sq, "", search.MaxLimit)
			if err != nil {
				t.Fatal(err)
			}
			if n != len(ids) || len(res.Items) != len(ids) {
				t.Errorf("%+v: %d resolved, count %d, page %d", sq, len(ids), n, len(res.Items))
			}
			for _, r := range res.Items {
				if !index.IsQuarantinePath(r.Path) {
					t.Errorf("%+v finds %q", sq, r.Path)
				}
				if r.Copies.Valid {
					t.Errorf("%q in quarantine has copies %d", r.Path, r.Copies.Int64)
				}
			}
			if sq.Name == "" && len(ids) != inQ {
				t.Errorf("%+v resolves %d entries, the quarantine holds %d", sq, len(ids), inQ)
			}
			if sq.Name != "" && (len(ids) != inFolder || !slices.Contains(ids, w.s.ID(curriculo))) {
				t.Errorf("%+v resolves %v, want the %d below %s", sq, ids, inFolder, folder)
			}
		}
	})

	t.Run("copies", func(t *testing.T) {
		// Two of curriculo's four copies, and the archive's copy of
		// emule.exe, are in the quarantine.
		for p, want := range map[string]int64{backupCV: 2, emule: 1} {
			if c := w.detail(t, w.id(p)).Entry.Copies; c == nil || *c != want {
				t.Errorf("%s: copies %v, want %d", p, c, want)
			}
		}
		within := url.QueryEscape(w.id("Downloads/emule-0.47c"))
		ids := func(q string) []string {
			var out []string
			for _, r := range w.searchAll(t, q) {
				out = append(out, string(r.PathB64))
			}
			return out
		}
		if got := ids("within=" + within + "&dup=copies"); slices.Contains(got, emule) {
			t.Errorf("dup=copies finds emule.exe, whose only other copy is quarantined: %q", got)
		}
		if got := ids("within=" + within + "&dup=unique"); !slices.Contains(got, emule) {
			t.Errorf("dup=unique misses emule.exe, whose only other copy is quarantined: %q", got)
		}
		if got := ids("source=corpus&dup=elsewhere&within=" + url.QueryEscape(w.id("Backup_PC_2004"))); !slices.Contains(got, backupCV) {
			t.Errorf("dup=elsewhere misses the backup's curriculo, copied in HD antigo: %q", got)
		}
	})

	t.Run("detail", func(t *testing.T) {
		read := func(ref string) *inQuarantineRes {
			t.Helper()
			var d struct {
				InQuarantine *inQuarantineRes `json:"in_quarantine"`
				OnlyFolder   *string          `json:"only_folder"`
			}
			w.get(t, "/api/entries/"+ref, 200, &d)
			return d.InQuarantine
		}
		at := quarantinedAt.Format(time.RFC3339)
		for _, p := range []string{folder, curriculo, "Documentos/TCC"} {
			got := read(w.id(p))
			if got == nil || str(got.PlanID) != fmt.Sprint(quarantinePlan) || str(got.QuarantinedAt) != at ||
				got.Original == nil || got.Original.Path != folder || string(got.Original.PathB64) != folder {
				t.Errorf("%s: in_quarantine %+v, want plan %d at %s from %s", p, got, quarantinePlan, at, folder)
			}
		}
		// The quarantine's own folders, and an item of unknown origin, have
		// no plan, time, or origin.
		for _, ref := range []string{q.String(), w.id(archive)} {
			if got := read(ref); got == nil || got.PlanID != nil || got.QuarantinedAt != nil || got.Original != nil {
				t.Errorf("%s: in_quarantine %+v, want every field null", ref, got)
			}
		}
		for _, ref := range []string{w.s.Root.String(), w.id(backupCV)} {
			if got := read(ref); got != nil {
				t.Errorf("%s: in_quarantine %+v, want null", ref, got)
			}
		}
	})

	// sourcesQuarantine reads the quarantine of every source from
	// GET /api/sources, by source ID.
	sourcesQuarantine := func(t *testing.T) map[string]string {
		t.Helper()
		svc, err := sources.New(w.st, synthfs.New(), config.Sources{AllowedRoots: []string{t.TempDir()}}, clock.Real{})
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		sources.Register(mux, svc, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sources", nil))
		var body struct {
			Sources []struct {
				ID         string `json:"id"`
				Quarantine struct {
					Files     int64 `json:"files"`
					Bytes     int64 `json:"bytes"`
					NameTaken bool  `json:"name_taken"`
				} `json:"quarantine"`
			} `json:"sources"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != 200 || err != nil {
			t.Fatalf("GET /api/sources: %d %s", rec.Code, rec.Body)
		}
		out := map[string]string{}
		for _, s := range body.Sources {
			out[s.ID] = fmt.Sprintf("%+v", s.Quarantine)
		}
		return out
	}

	t.Run("sources", func(t *testing.T) {
		want := fmt.Sprintf("{Files:%d Bytes:%d NameTaken:false}", moved.Files, moved.Bytes)
		if got := sourcesQuarantine(t); len(got) != 1 || got["corpus"] != want {
			t.Errorf("quarantine %v, want corpus %s", got, want)
		}
		// A folder of that name Precious did not record is the owner's.
		w.exec(t, `UPDATE sources SET quarantine_entry_id = NULL WHERE id = 'corpus'`)
		want = fmt.Sprintf("{Files:%d Bytes:%d NameTaken:true}", moved.Files, moved.Bytes)
		if got := sourcesQuarantine(t); got["corpus"] != want {
			t.Errorf("quarantine %v, want corpus %s", got, want)
		}
	})

	t.Run("members", func(t *testing.T) {
		// Every copy of the pendrive archive's photos outside it is
		// quarantined: its members have no other copy, and its member
		// folders duplicate nothing.
		const zip = "Downloads/fotos_2005_do_pendrive.zip"
		read := func() []contentRow {
			var rows []contentRow
			for _, f := range w.allChildren(t, w.id(zip), "", 100) {
				rows = append(rows, f)
				rows = append(rows, w.allChildren(t, f.ID, "", 100)...)
			}
			return rows
		}
		for _, r := range read() {
			if (r.Kind == "directory" && (r.DuplicatedBytes == nil || *r.DuplicatedBytes != r.TotalBytes)) ||
				(r.Kind == "file" && (r.Copies == nil || *r.Copies < 3)) {
				t.Fatalf("before: %+v", r)
			}
		}
		quarantineIndex(t, w, quarantinePlan+1, []string{"Downloads/fotos_2005_do_pendrive", "Fotos/2005", "Fotos - Copia/2005"}, nil)
		rows := read()
		if len(rows) == 0 {
			t.Fatal("the archive lists no members")
		}
		for _, r := range rows {
			if (r.Kind == "directory" && (r.DuplicatedBytes == nil || *r.DuplicatedBytes != 0)) ||
				(r.Kind == "file" && (r.Copies == nil || *r.Copies != 1)) {
				t.Errorf("after: %s: copies %v, duplicated bytes %v", r.Path, r.Copies, r.DuplicatedBytes)
			}
		}
	})

	t.Run("the owner's folder", func(t *testing.T) {
		// A source whose top holds one folder and a folder of the owner's
		// own at the reserved name: the top's only folder is the first,
		// and the sources JSON names the second.
		sfs := synthfs.New()
		root := sfs.Root("/pen")
		root.Dir("Fotos").File("a.jpg", 10, quarantinedAt)
		root.Dir(index.QuarantineName).File("b.txt", 5, quarantinedAt)
		penRoot := w.scanSynth(t, sfs, "pen", "/pen", root)
		var fotos, a int64
		if err := w.st.Reader().QueryRow(`SELECT (SELECT id FROM entries WHERE source_id = 'pen' AND path = CAST('Fotos' AS BLOB)),
			(SELECT id FROM entries WHERE source_id = 'pen' AND path = CAST('Fotos/a.jpg' AS BLOB))`).Scan(&fotos, &a); err != nil {
			t.Fatal(err)
		}
		var top struct {
			OnlyFolder *string `json:"only_folder"`
		}
		w.get(t, "/api/entries/"+penRoot.String(), 200, &top)
		if str(top.OnlyFolder) != fmt.Sprint(fotos) {
			t.Errorf("only_folder %s, want %d", str(top.OnlyFolder), fotos)
		}
		var file struct {
			Ancestors []struct {
				ID        string `json:"id"`
				OnlyChild bool   `json:"only_child"`
			} `json:"ancestors"`
		}
		w.get(t, fmt.Sprintf("/api/entries/%d", a), 200, &file)
		if len(file.Ancestors) != 2 || !file.Ancestors[0].OnlyChild || !file.Ancestors[1].OnlyChild {
			t.Errorf("ancestors %+v, want the top and Fotos, each with one child", file.Ancestors)
		}
		if got := sourcesQuarantine(t)["pen"]; got != "{Files:1 Bytes:5 NameTaken:true}" {
			t.Errorf("pen quarantine %s, want 1 file, 5 bytes, and the name taken", got)
		}
	})
}
