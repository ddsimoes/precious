package api

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// The R2 read API over the content corpus (tasks 6.2, 6.3): rows and
// details with content, copies, relations, archives, and coverage; member
// rows, details, children, and treemaps; Home, Opportunities, review
// lists, Gems, and Compare with their errors.
func TestR2ReadAPI(t *testing.T) {
	w := newContentWorld(t)
	cov, err := content.CoverageOf(context.Background(), w.st.Reader(), "")
	if err != nil {
		t.Fatal(err)
	}
	wantCov := coverageRes{
		Candidate: amountRes{cov.CandidateFiles, cov.CandidateBytes}, Checked: amountRes{cov.CheckedFiles, cov.CheckedBytes},
		Unchecked: amountRes{cov.UncheckedFiles, cov.UncheckedBytes}, Unreadable: amountRes{cov.UnreadableFiles, cov.UnreadableBytes},
	}
	if cov.CandidateFiles == 0 {
		t.Fatal("the corpus has no candidate files")
	}

	t.Run("copies of curriculo.doc", func(t *testing.T) {
		const p = "Documentos/curriculo.doc"
		group, sum := w.group(t, p)
		d := w.detail(t, w.id(p))
		if d.Entry.ContentState == nil || *d.Entry.ContentState != "hashed" || d.Entry.Copies == nil || *d.Entry.Copies != 4 {
			t.Errorf("row: content_state %v, copies %v", d.Entry.ContentState, d.Entry.Copies)
		}
		c := d.Content
		if c == nil || c.State != "hashed" || c.SHA256 == nil || *c.SHA256 != sum || c.CopiesCount != len(group)-1 {
			t.Fatalf("content %+v, want hashed %s with %d copies", c, sum, len(group)-1)
		}
		var got []string
		for _, cp := range c.Copies {
			got = append(got, string(cp.PathB64))
			if cp.SourceID != "corpus" || cp.ArchiveID != nil || cp.HardLink || cp.Offline || cp.EffDecision != "undecided" {
				t.Errorf("copy %+v", cp)
			}
		}
		want := slices.DeleteFunc(slices.Clone(group), func(s string) bool { return s == p })
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("copies %q, want %q", got, want)
		}
		if d.Coverage != wantCov {
			t.Errorf("coverage %+v, want %+v", d.Coverage, wantCov)
		}
		if d.Archive != nil || len(d.Relations) != 0 {
			t.Errorf("archive %+v, relations %+v", d.Archive, d.Relations)
		}

		// /copies pages through the same copies.
		var all []string
		cursor := ""
		for {
			var page struct {
				Items      []copyRes `json:"items"`
				NextCursor *string   `json:"next_cursor"`
				Count      int       `json:"count"`
			}
			target := fmt.Sprintf("/api/entries/%s/copies?limit=2", w.id(p))
			if cursor != "" {
				target += "&cursor=" + url.QueryEscape(cursor)
			}
			w.get(t, target, 200, &page)
			if page.Count != len(want) || len(page.Items) > 2 {
				t.Fatalf("%s: %d items, count %d", target, len(page.Items), page.Count)
			}
			for _, cp := range page.Items {
				all = append(all, string(cp.PathB64))
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		slices.Sort(all)
		if !slices.Equal(all, want) {
			t.Errorf("/copies %q, want %q", all, want)
		}
		w.fails(t, fmt.Sprintf("/api/entries/%s/copies?cursor=x9", w.id(p)), 400, "invalid_request")
		w.fails(t, "/api/entries/m999999/copies", 404, "not_found")
	})

	t.Run("emule-0.47c and its zip", func(t *testing.T) {
		const folder, zip = "Downloads/emule-0.47c", "Downloads/eMule0.47c-Installer.zip"
		d := w.detail(t, w.id(folder))
		var rel *relationRes
		for i := range d.Relations {
			if d.Relations[i].Other.ID == w.id(zip) {
				rel = &d.Relations[i]
			}
		}
		if rel == nil || rel.Kind != "same" || rel.Self != "b" || rel.Other.ArchiveState == nil ||
			*rel.Other.ArchiveState != "complete" || rel.MatchedBytes == 0 {
			t.Fatalf("relations of %s: %+v", folder, d.Relations)
		}
		if d.Entry.CandidateBytes == nil || d.Entry.DuplicatedBytes == nil || *d.Entry.DuplicatedBytes != d.Entry.TotalBytes {
			t.Errorf("%s: candidate %v, duplicated %v of %d bytes", folder, d.Entry.CandidateBytes, d.Entry.DuplicatedBytes,
				d.Entry.TotalBytes)
		}
		z := w.detail(t, w.id(zip))
		if z.Archive == nil || z.Archive.Format != "zip" || z.Archive.State != "complete" || z.Archive.Members == 0 {
			t.Errorf("archive of %s: %+v", zip, z.Archive)
		}
		if len(z.Relations) == 0 || z.Relations[0].Self != "a" || z.Relations[0].Other.ID != w.id(folder) ||
			z.Relations[0].ID != rel.ID {
			t.Errorf("relations of %s: %+v", zip, z.Relations)
		}
		if z.Entry.ArchiveState == nil || *z.Entry.ArchiveState != "complete" {
			t.Errorf("zip row: archive_state %v", z.Entry.ArchiveState)
		}
	})

	t.Run("members", func(t *testing.T) {
		const zip = "Downloads/eMule0.47c-Installer.zip"
		w.exec(t, `UPDATE entries SET decision = 'keep', eff_decision = 'keep', eff_from = id WHERE id = ?`, int64(w.s.ID(zip)))
		defer w.exec(t, `UPDATE entries SET decision = NULL, eff_decision = 'undecided', eff_from = NULL WHERE id = ?`, int64(w.s.ID(zip)))
		top := w.allChildren(t, w.id(zip), "", 100)
		dir := w.member(t, zip, "emule-0.47c")
		if len(top) != 1 || top[0].ID != dir || top[0].Kind != "directory" || str(top[0].ArchiveID) != w.id(zip) ||
			top[0].Decision != nil || top[0].EffDecision != "keep" || top[0].TagIDs == nil || len(top[0].TagIDs) != 0 ||
			top[0].Path != zip+"!emule-0.47c" || top[0].CandidateBytes == nil || top[0].DuplicatedBytes == nil ||
			*top[0].DuplicatedBytes != top[0].TotalBytes || top[0].TotalBytes == 0 {
			t.Fatalf("children of the zip: %+v", top)
		}
		files := w.allChildren(t, dir, "&sort=name", 100)
		var exe *contentRow
		for i := range files {
			if files[i].Name == "emule.exe" {
				exe = &files[i]
			}
		}
		if exe == nil || str(exe.ContentState) != "hashed" || exe.Copies == nil || *exe.Copies != 2 ||
			str(exe.FileKind) == "<null>" || exe.Kind != "file" {
			t.Fatalf("emule.exe member: %+v", exe)
		}

		d := w.detail(t, exe.ID)
		var anc []string
		for _, a := range d.Ancestors {
			anc = append(anc, a.ID)
		}
		wantAnc := []string{w.s.Root.String(), w.id("Downloads"), w.id(zip), dir}
		if !slices.Equal(anc, wantAnc) {
			t.Errorf("ancestors %q, want %q", anc, wantAnc)
		}
		if d.Content == nil || d.Content.CopiesCount != 1 || string(d.Content.Copies[0].PathB64) != "Downloads/emule-0.47c/emule.exe" {
			t.Errorf("content %+v", d.Content)
		}
		if d.Intent.EffDecision != "keep" || d.Intent.Decision != nil || d.Classification.Category != nil ||
			d.Stats != nil || d.Archive != nil {
			t.Errorf("member detail: %+v", d)
		}
		// The unpacked copy lists the member, with its archive.
		u := w.detail(t, w.id("Downloads/emule-0.47c/emule.exe"))
		if u.Content == nil || u.Content.CopiesCount != 1 || u.Content.Copies[0].Ref != exe.ID ||
			str(u.Content.Copies[0].ArchiveID) != w.id(zip) || u.Content.Copies[0].EffDecision != "keep" {
			t.Errorf("copies of the unpacked emule.exe: %+v", u.Content)
		}
		md := w.detail(t, dir)
		if md.Stats == nil || md.Stats.Files != 6 || md.Stats.Dirs != 2 || len(md.Relations) != 0 {
			t.Errorf("member folder detail: stats %+v, relations %+v", md.Stats, md.Relations)
		}
	})

	t.Run("paging inside an archive", func(t *testing.T) {
		const zip = "Downloads/fotos_2005_do_pendrive.zip"
		folder := w.member(t, zip, "Carnaval")
		for _, sort := range []string{"name", "bytes", "files", "newest"} {
			for _, order := range []string{"asc", "desc"} {
				q := "&sort=" + sort + "&order=" + order
				one := w.allChildren(t, folder, q, 1000)
				paged := w.allChildren(t, folder, q, 2)
				if len(one) != 5 || !slices.Equal(contentIDs(one), contentIDs(paged)) {
					t.Errorf("%s: %q, paged %q", q, contentIDs(one), contentIDs(paged))
				}
				for i := 1; i < len(one); i++ {
					c := compareRows(t, sort, one[i-1].row, one[i].row)
					if (order == "asc" && c > 0) || (order == "desc" && c < 0) {
						t.Errorf("%s: %s before %s", q, one[i-1].Name, one[i].Name)
					}
				}
			}
		}
		var tm struct {
			Entry contentRow   `json:"entry"`
			Items []contentRow `json:"items"`
			Other amountRes    `json:"other"`
		}
		w.get(t, fmt.Sprintf("/api/entries/%s/treemap", w.id(zip)), 200, &tm)
		var sum int64
		for _, it := range tm.Items {
			sum += it.TotalBytes
		}
		if tm.Entry.ID != w.id(zip) || len(tm.Items) != 2 || tm.Other.Files != 0 || tm.Items[0].TotalBytes < tm.Items[1].TotalBytes {
			t.Errorf("treemap of the zip: %+v", tm)
		}
		w.get(t, fmt.Sprintf("/api/entries/%s/treemap", folder), 200, &tm)
		if tm.Entry.ID != folder || len(tm.Items) != 5 || tm.Entry.TotalBytes != func() (s int64) {
			for _, it := range tm.Items {
				s += it.TotalBytes
			}
			return s
		}() {
			t.Errorf("treemap of Carnaval: %+v", tm)
		}
		// A member file has no children; an unknown member is not_found.
		photo := w.member(t, zip, "Carnaval/DSC01001.JPG")
		if rows := w.allChildren(t, photo, "", 10); len(rows) != 0 {
			t.Errorf("children of a member file: %+v", rows)
		}
		w.fails(t, "/api/entries/m999999/children", 404, "not_found")
		w.fails(t, "/api/entries/m999999", 404, "not_found")
		w.fails(t, "/api/entries/m0/treemap", 404, "not_found")
	})

	t.Run("folder figures and search", func(t *testing.T) {
		var copia *contentRow
		rows := w.allChildren(t, w.s.Root.String(), "", 100)
		for i := range rows {
			if rows[i].Name == "Fotos - Copia" {
				copia = &rows[i]
			}
		}
		if copia == nil || copia.DuplicatedBytes == nil || *copia.DuplicatedBytes == 0 ||
			*copia.DuplicatedBytes >= copia.TotalBytes || copia.CheckedBytes == nil || copia.CandidateBytes == nil {
			t.Fatalf("Fotos - Copia: %+v", copia)
		}
		var res struct {
			Items []contentRow `json:"items"`
			Count int          `json:"count"`
		}
		w.get(t, "/api/search?dup=elsewhere&within="+copia.ID, 200, &res)
		for _, r := range res.Items {
			if strings.HasSuffix(r.Path, "DSC_editada.JPG") || str(r.ContentState) != "hashed" || *r.Copies < 2 {
				t.Errorf("elsewhere: %+v", r)
			}
		}
		if res.Count < 30 {
			t.Errorf("elsewhere within Fotos - Copia: %d matches", res.Count)
		}
		w.fails(t, "/api/search?dup=elsewhere", 400, "invalid_request")
	})

	t.Run("home and opportunities", func(t *testing.T) {
		w.exec(t, `INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts, max_attempts,
			available_at, created_at, updated_at, progress)
			VALUES ('hash', 1, '{}', 'corpus', 'running', 0, 3, 0, 0, 0, '{"phase":2,"checked_files":7}')`)
		var home struct {
			Coverage coverageRes `json:"coverage"`
			Cards    []cardRes   `json:"cards"`
			Hashing  []struct {
				SourceID string           `json:"source_id"`
				JobID    string           `json:"job_id"`
				Kind     string           `json:"kind"`
				State    string           `json:"state"`
				Progress map[string]int64 `json:"progress"`
			} `json:"hashing"`
		}
		w.get(t, "/api/home", 200, &home)
		if home.Coverage != wantCov || len(home.Cards) != 7 || len(home.Hashing) != 1 || home.Hashing[0].Kind != "hash" ||
			home.Hashing[0].State != "running" || home.Hashing[0].Progress["checked_files"] != 7 {
			t.Errorf("home: %+v", home)
		}

		var opp struct {
			Cards      []cardRes   `json:"cards"`
			Coverage   coverageRes `json:"coverage"`
			ComputedAt *string     `json:"computed_at"`
		}
		w.get(t, "/api/opportunities", 200, &opp)
		if len(opp.Cards) != 7 || opp.Coverage != wantCov || opp.ComputedAt == nil || !slices.Equal(opp.Cards, home.Cards) {
			t.Errorf("opportunities: %+v", opp)
		}
		var dups *cardRes
		for i := range opp.Cards {
			if i > 0 && opp.Cards[i].Bytes > opp.Cards[i-1].Bytes {
				t.Errorf("cards are not largest first: %+v", opp.Cards)
			}
			if opp.Cards[i].List == "duplicates" {
				dups = &opp.Cards[i]
			}
		}
		if dups == nil || dups.Rows == 0 || dups.Basis != "content" {
			t.Fatalf("duplicates card: %+v", dups)
		}

		var list struct {
			Card  cardRes `json:"card"`
			Items []struct {
				ID       string       `json:"id"`
				Bytes    int64        `json:"bytes"`
				Entry    *contentRow  `json:"entry"`
				Relation *relationRes `json:"relation"`
				Copies   []copyRes    `json:"copies"`
				Summary  struct {
					Files   int64    `json:"files"`
					Bytes   int64    `json:"bytes"`
					Signals []string `json:"signals"`
				} `json:"summary"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		w.get(t, "/api/opportunities/duplicates?limit=500", 200, &list)
		if list.Card != *dups || int64(len(list.Items)) != dups.Rows || list.NextCursor != nil {
			t.Fatalf("duplicates list: card %+v, %d items", list.Card, len(list.Items))
		}
		var sum int64
		relRows, groupRows := 0, 0
		for _, it := range list.Items {
			sum += it.Bytes
			switch {
			case it.Relation != nil:
				relRows++
				if it.Entry == nil || it.Relation.Self != "a" || it.Copies != nil || it.Relation.Other.ID == it.Entry.ID {
					t.Errorf("relation row %+v", it)
				}
			default:
				groupRows++
				if it.Entry != nil || len(it.Copies) < 2 || it.Summary.Signals == nil {
					t.Errorf("group row %+v", it)
				}
			}
		}
		if sum != dups.Bytes || relRows == 0 || groupRows == 0 {
			t.Errorf("duplicates: %d bytes (card %d), %d relation rows, %d group rows", sum, dups.Bytes, relRows, groupRows)
		}
		w.get(t, "/api/opportunities/duplicates?decided=1", 200, &list)
		if len(list.Items) != 0 {
			t.Errorf("decided duplicates: %+v", list.Items)
		}
		w.get(t, "/api/opportunities/system_junk?source=corpus", 200, &list)
		for _, it := range list.Items {
			if it.Entry == nil || it.Relation != nil || it.Copies != nil || it.Summary.Bytes != it.Bytes {
				t.Errorf("system_junk row %+v", it)
			}
		}
		w.fails(t, "/api/opportunities/nada", 404, "not_found")
		w.fails(t, "/api/opportunities/gems_unique", 404, "not_found")
		w.fails(t, "/api/opportunities/caches?decided=2", 400, "invalid_request")
		w.fails(t, "/api/opportunities/caches?source=nada", 404, "not_found")
		w.fails(t, "/api/opportunities/caches?cursor=x", 400, "invalid_request")
		w.fails(t, "/api/opportunities?source=nada", 404, "not_found")
	})

	t.Run("gems", func(t *testing.T) {
		type gem struct {
			Entry    contentRow   `json:"entry"`
			Group    *contentRow  `json:"group"`
			Relation *relationRes `json:"relation"`
		}
		var page struct {
			Section  string      `json:"section"`
			Items    []gem       `json:"items"`
			Coverage coverageRes `json:"coverage"`
		}
		w.get(t, "/api/gems?section=unique&limit=500", 200, &page)
		if page.Section != "unique" || len(page.Items) != len(w.gt.Gems.Unique) || page.Coverage != wantCov {
			t.Errorf("unique: %d items, ground truth %d", len(page.Items), len(w.gt.Gems.Unique))
		}
		w.get(t, "/api/gems?section=rescue&limit=500", 200, &page)
		for _, g := range page.Items {
			if g.Group == nil || !strings.HasPrefix(g.Entry.Path, g.Group.Path+"/") {
				t.Errorf("rescue %+v", g)
			}
		}
		w.get(t, "/api/gems?section=only_in_copy&limit=500", 200, &page)
		// The real relate pass finds the ground truth's overlaps among
		// others, so its files are among the items.
		listed := map[string]bool{}
		for _, g := range page.Items {
			listed[g.Entry.Path] = true
		}
		for _, g := range w.gt.Gems.OnlyInCopy {
			if !listed[g.Path.Path] {
				t.Errorf("only_in_copy lacks %s", g.Path.Path)
			}
		}
		for _, g := range page.Items {
			if g.Group == nil || g.Relation == nil || g.Relation.Kind != "overlap" || g.Relation.Other.ID == g.Group.ID ||
				!strings.HasPrefix(g.Entry.Path, g.Group.Path+"/") || g.Relation.OnlyHere.Files == 0 {
				t.Errorf("only_in_copy %+v", g)
			}
		}
		w.fails(t, "/api/gems", 400, "invalid_request")
		w.fails(t, "/api/gems?section=keeper", 400, "invalid_request")
	})

	t.Run("compare", func(t *testing.T) {
		folder, zip := w.id("Downloads/emule-0.47c"), w.id("Downloads/eMule0.47c-Installer.zip")
		var res struct {
			Left    contentRow           `json:"left"`
			Right   contentRow           `json:"right"`
			Summary map[string]amountRes `json:"summary"`
			Items   []struct {
				Path  string      `json:"path"`
				Left  *contentRow `json:"left"`
				Right *contentRow `json:"right"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		w.get(t, fmt.Sprintf("/api/compare?left=%s&right=%s&bucket=identical", folder, zip), 200, &res)
		if res.Left.ID != folder || res.Right.ID != zip || len(res.Summary) != 5 || res.Summary["identical"].Files != 6 ||
			len(res.Items) != 6 || res.Summary["only_left"].Files != 0 || res.Summary["only_right"].Files != 0 {
			t.Fatalf("compare: %+v", res)
		}
		for _, it := range res.Items {
			if it.Left == nil || it.Right == nil || it.Right.ArchiveID == nil || str(it.Right.ArchiveID) != zip ||
				it.Left.Name != it.Right.Name {
				t.Errorf("item %+v", it)
			}
		}
		w.get(t, fmt.Sprintf("/api/compare?left=%s&right=%s", folder, w.member(t, "Downloads/eMule0.47c-Installer.zip", "emule-0.47c")), 200, &res)
		if len(res.Items) != 0 || res.Summary["identical"].Files != 6 || !strings.HasPrefix(res.Right.ID, "m") {
			t.Errorf("compare with the member folder: %+v", res)
		}
		w.fails(t, fmt.Sprintf("/api/compare?left=%s&right=%s", w.id("Fotos"), w.id("Fotos/2004")), 400, "invalid_request")
		w.fails(t, fmt.Sprintf("/api/compare?left=%s&right=%s", w.id("Fotos"), w.id("Documentos/curriculo.doc")), 400, "invalid_request")
		w.fails(t, fmt.Sprintf("/api/compare?left=%s&right=999999", w.id("Fotos")), 404, "not_found")
		w.fails(t, fmt.Sprintf("/api/compare?left=%s&right=%s&bucket=all", folder, zip), 400, "invalid_request")
		w.fails(t, fmt.Sprintf("/api/compare?left=x&right=%s", zip), 400, "invalid_request")
	})
}

type cardRes struct {
	List  string `json:"list"`
	Bytes int64  `json:"bytes"`
	Rows  int64  `json:"rows"`
	Basis string `json:"basis"`
}

func contentIDs(rows []contentRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// R2.4 Duplicates are information: discarding Documentos/curriculo (1).doc
// through set-decision, after reading its group, changes that copy alone;
// every other copy keeps its decision, triage, and tags. A member has no
// decision or tags of its own: set-decision and set-tags naming one are
// invalid_request.
func TestR2_4DiscardingACopyChangesNoOther(t *testing.T) {
	w := newContentWorld(t)
	const target = "Documentos/curriculo (1).doc"
	group, _ := w.group(t, target)
	cmds := w.commands(t)
	tag := createTag(t, w.env, "cv", w.s.ID("Documentos/curriculo.doc"), w.s.ID(group[0]))
	if code, body := w.post(t, cmds, "set-decision",
		fmt.Sprintf(`{"entry_id":%q,"decision":"keep"}`, w.id("Documentos/curriculo.doc"))); code != 200 {
		t.Fatalf("keep curriculo.doc: %d %s", code, body)
	}

	// Read the group as the interface does.
	d := w.detail(t, w.id(target))
	if d.Content == nil || d.Content.CopiesCount != len(group)-1 {
		t.Fatalf("group of %s: %+v", target, d.Content)
	}
	type state struct {
		decision, eff string
		triage        string
		tags          []int64
	}
	read := func() map[string]state {
		out := map[string]state{}
		for _, c := range append(d.Content.Copies, copyRes{Ref: w.id(target)}) {
			r := w.detail(t, c.Ref)
			var tags []int64
			for _, tg := range r.Intent.Tags {
				tags = append(tags, tg.ID)
			}
			out[c.Ref] = state{decision: str(r.Entry.Decision), eff: r.Entry.EffDecision, triage: str(r.Entry.Triage), tags: tags}
		}
		return out
	}
	before := read()
	if code, body := w.post(t, cmds, "set-decision",
		fmt.Sprintf(`{"entry_id":%q,"decision":"discard"}`, w.id(target))); code != 200 {
		t.Fatalf("discard %s: %d %s", target, code, body)
	}
	after := read()
	for ref, b := range before {
		a := after[ref]
		if ref == w.id(target) {
			if a.decision != "discard" || a.eff != "discard" {
				t.Errorf("%s after discard: %+v", target, a)
			}
			continue
		}
		if a.decision != b.decision || a.eff != b.eff || a.triage != b.triage || !slices.Equal(a.tags, b.tags) {
			t.Errorf("copy %s changed: %+v, then %+v", ref, b, a)
		}
	}
	if b := before[w.id("Documentos/curriculo.doc")]; b.decision != "keep" || !slices.Contains(b.tags, tag) {
		t.Errorf("curriculo.doc before: %+v", b)
	}

	member := w.member(t, "Downloads/eMule0.47c-Installer.zip", "emule-0.47c/emule.exe")
	for _, c := range []struct{ name, body string }{
		{"set-decision", fmt.Sprintf(`{"entry_id":%q,"decision":"discard"}`, member)},
		{"set-decision", `{"entry_id":"m45","decision":"keep"}`},
		{"set-decision", fmt.Sprintf(`{"entry_ids":[%q,%q],"decision":"discard"}`, w.id(target), member)},
		{"set-tags", fmt.Sprintf(`{"entry_ids":[%q],"add":[%d]}`, member, tag)},
		{"set-tags", fmt.Sprintf(`{"entry_ids":["m45"],"add":[%d]}`, tag)},
	} {
		code, body := w.post(t, cmds, c.name, c.body)
		if code != 400 || !strings.Contains(body, `"invalid_request"`) {
			t.Errorf("%s %s: %d %s", c.name, c.body, code, body)
		}
	}
}

// R2.7 Every claim of no other copy carries the checked share: with
// coverage seeded at 80% of the candidate bytes over two sources, the
// detail of a photo whose size is unique says it has no other copy (unique
// size, one copy, none listed) with that share, and a pending photo reads
// as not checked (no copy count, no digest).
func TestR2_7NoOtherCopyCarriesTheCheckedShare(t *testing.T) {
	e := newEnv(t)
	img := domain.FileKindImage
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, MountPoint: "/media/pen",
		Nodes: []indextest.Node{
			{Path: "Fotos/unica.jpg", Size: 4321, FileKind: img},
			{Path: "Fotos/pendente.jpg", Size: 5000, FileKind: img},
		}})
	disco := indextest.Seed(t, e.st, indextest.Tree{Source: "disco", CreateSource: true, MountPoint: "/mnt/disco",
		Nodes: []indextest.Node{{Path: "copia.jpg", Size: 5000, FileKind: img}}})
	s.SetContent(e.st, "Fotos/unica.jpg", indextest.Content{State: domain.ContentUniqueSize})
	s.SetContent(e.st, "Fotos/pendente.jpg", indextest.Content{State: domain.ContentPending})
	disco.SetContent(e.st, "copia.jpg", indextest.Content{State: domain.ContentPending})
	e.exec(t, `UPDATE entries SET nlink = 1 WHERE kind = 'file'`) // as a scan writes it
	e.exec(t, `DELETE FROM content_coverage`)
	for _, c := range []struct {
		src                string
		candidate, checked int64
	}{{"pen", 600, 400}, {"disco", 400, 400}} {
		e.exec(t, `INSERT INTO content_coverage (source_id, candidate_files, candidate_bytes, checked_files, checked_bytes,
			unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes, updated_at)
			VALUES (?, 10, ?, 8, ?, 2, ?, 0, 0, 0)`, c.src, c.candidate, c.checked, c.candidate-c.checked)
	}

	var d detailRes
	e.get(t, fmt.Sprintf("/api/entries/%s", s.ID("Fotos/unica.jpg")), 200, &d)
	share := float64(d.Coverage.Checked.Bytes) / float64(d.Coverage.Candidate.Bytes)
	if d.Content == nil || d.Content.State != "unique_size" || d.Content.CopiesCount != 0 || len(d.Content.Copies) != 0 ||
		d.Entry.Copies == nil || *d.Entry.Copies != 1 || share != 0.8 {
		t.Errorf("unique photo: content %+v, copies %v, share %v", d.Content, d.Entry.Copies, share)
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", s.ID("Fotos/pendente.jpg")), 200, &d)
	if d.Content == nil || d.Content.State != "pending" || d.Content.SHA256 != nil || d.Entry.Copies != nil ||
		str(d.Entry.ContentState) != "pending" {
		t.Errorf("pending photo: content %+v, row copies %v", d.Content, d.Entry.Copies)
	}
}
