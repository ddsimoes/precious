package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/domain"
)

// TestServeScansTheCorpus drives the wired server the way the app does,
// through the real router, authentication, CSRF, and middleware: log in, pick
// the regression corpus through picker handles, add it as a source, scan it,
// and read Home, search, and the viewer. Home's totals are the ground truth's.
func TestServeScansTheCorpus(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can list the corpus's mode-000 folder, which the ground truth counts as unreadable")
	}
	disk := t.TempDir()
	dir := filepath.Join(disk, "corpus")
	// Cleanups run last-registered first: privado is readable again before
	// the temporary directory is removed.
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(dir, "privado"), 0o755); err != nil {
			t.Error(err)
		}
	})
	gt, err := corpus.WriteDir(dir, corpus.Corpus())
	if err != nil {
		t.Fatal(err)
	}
	origin := startServe(t, builtUI(), func(cfg *config.Config) { cfg.Sources.AllowedRoots = []string{disk} })

	c := newBrowser(t, origin)
	csrf := c.login()

	var roots struct {
		Roots []struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		} `json:"roots"`
	}
	c.getJSON("/api/picker", &roots)
	if len(roots.Roots) != 1 {
		t.Fatalf("picker roots %+v, want the one allowed root", roots.Roots)
	}
	var listing struct {
		Children []struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		} `json:"children"`
	}
	c.getJSON("/api/picker?handle="+url.QueryEscape(roots.Roots[0].Handle), &listing)
	handle := ""
	for _, ch := range listing.Children {
		if ch.Name == "corpus" {
			handle = ch.Handle
		}
	}
	if handle == "" {
		t.Fatalf("picker children %+v, want corpus", listing.Children)
	}

	code, body := c.command("add-source", `{"handle":"`+handle+`","label":"Old disk"}`, origin, csrf)
	var added struct {
		Source struct {
			ID string `json:"id"`
		} `json:"source"`
	}
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &added) != nil || added.Source.ID == "" {
		t.Fatalf("add-source = %d %s", code, body)
	}
	code, body = c.command("start-scan", `{"source_id":"`+added.Source.ID+`"}`, origin, csrf)
	var scan struct {
		JobID string `json:"job_id"`
	}
	if code != http.StatusAccepted || json.Unmarshal([]byte(body), &scan) != nil || scan.JobID == "" {
		t.Fatalf("start-scan = %d %s", code, body)
	}
	if state := c.awaitJob(scan.JobID); state != "succeeded" {
		t.Fatalf("scan ended %q", state)
	}
	if state := c.jobState(scan.JobID); state != "succeeded" {
		t.Fatalf("GET /api/jobs/%s state = %q", scan.JobID, state)
	}

	var want struct{ bytes, files, dirs int64 }
	for _, e := range gt.Entries {
		switch {
		case e.Size != nil:
			want.bytes += *e.Size
			want.files++
		case e.Kind == domain.EntryDirectory:
			want.dirs++
		}
	}
	var home struct {
		Totals struct {
			Bytes int64 `json:"bytes"`
			Files int64 `json:"files"`
			Dirs  int64 `json:"dirs"`
		} `json:"totals"`
		Partial bool  `json:"partial"`
		Scans   []any `json:"scans"`
	}
	c.getJSON("/api/home", &home)
	if home.Totals.Bytes != want.bytes || home.Totals.Files != want.files || home.Totals.Dirs != want.dirs {
		t.Errorf("home totals %+v, ground truth %d bytes, %d files, %d folders", home.Totals, want.bytes, want.files, want.dirs)
	}
	if !home.Partial || len(home.Scans) != 0 {
		t.Errorf("home partial %v (privado is unreadable), scans %v", home.Partial, home.Scans)
	}

	var found struct {
		Items []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"items"`
	}
	c.getJSON("/api/search?name="+url.QueryEscape("orcamento"), &found)
	const sheet = "Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"
	if len(found.Items) != 1 || found.Items[0].Path != sheet {
		t.Errorf("search orcamento = %+v, want the spreadsheet", found.Items)
	}

	c.getJSON("/api/search?name="+url.QueryEscape("foto.jpg"), &found)
	if len(found.Items) != 1 || found.Items[0].Path != "Midia/foto.jpg" {
		t.Fatalf("search foto.jpg = %+v", found.Items)
	}
	resp, jpeg := c.fetch(http.MethodGet, "/api/entries/"+found.Items[0].ID+"/content")
	onDisk, err := os.ReadFile(filepath.Join(dir, "Midia", "foto.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || jpeg != string(onDisk) {
		t.Fatalf("GET foto.jpg content = %d, %d bytes, want %d", resp.StatusCode, len(jpeg), len(onDisk))
	}
	if ct, csp := resp.Header.Get("Content-Type"), resp.Header.Get("Content-Security-Policy"); ct != "image/jpeg" ||
		csp != "sandbox; default-src 'none'" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("foto.jpg served as %q with CSP %q", ct, csp)
	}
}

// getJSON GETs path, which must answer 200, and decodes its JSON body.
func (b *browser) getJSON(path string, v any) {
	b.t.Helper()
	resp, body := b.fetch(http.MethodGet, path)
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		b.t.Fatalf("GET %s: %v: %s", path, err, body)
	}
}
