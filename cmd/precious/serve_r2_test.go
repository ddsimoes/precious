package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/store"
)

// r2Tables are the tables of migrations/0002_content.sql, children first.
var r2Tables = []string{"review_row_sources", "review_rows", "relations", "dir_dups", "archive_members",
	"archives", "file_content", "content_coverage", "contents", "review_state"}

// corpusDisk writes the regression corpus under a fresh allowed root and
// returns the root.
func corpusDisk(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can list the corpus's mode-000 folder, which the ground truth counts as unreadable")
	}
	disk := t.TempDir()
	dir := filepath.Join(disk, "corpus")
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(dir, "privado"), 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := corpus.WriteDir(dir, corpus.Corpus()); err != nil {
		t.Fatal(err)
	}
	return disk
}

// r2Config is a server configuration with its own state directory that
// allows disk as the only root.
func r2Config(t *testing.T, disk string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Sources.AllowedRoots = []string{disk}
	cfg.Server.AllowInsecureHTTP = true
	return cfg
}

// serveAt runs serve with cfg on a fresh loopback port and returns its
// origin and a function that stops it; stopping twice is harmless.
func serveAt(t *testing.T, cfg config.Config) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + ln.Addr().String()
	cfg.Server.Listen = ln.Addr().String()
	cfg.Server.ExternalOrigin = origin
	if err := config.Validate(&cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- serve(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), serveDeps{
			Clock: clock.Real{}, UI: builtUI(), Listener: ln, Ready: func(string) { close(ready) },
		})
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}
	t.Cleanup(stop)
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("serve exited early: %v", err)
	}
	return origin, stop
}

// addAndScan adds the corpus folder of the allowed root as a source through
// picker handles, scans it, and returns the source ID.
func addAndScan(t *testing.T, c *browser, origin, csrf string) string {
	t.Helper()
	var roots struct {
		Roots []struct {
			Handle string `json:"handle"`
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
	if code != http.StatusAccepted || json.Unmarshal([]byte(body), &scan) != nil {
		t.Fatalf("start-scan = %d %s", code, body)
	}
	if state := c.awaitJob(scan.JobID); state != "succeeded" {
		t.Fatalf("scan ended %q", state)
	}
	return added.Source.ID
}

// awaitQuiet waits until no job is queued, running, or paused, and at least
// one job of each kind in kinds has succeeded. It reads the database through
// its own store handle, as a second process would.
func awaitQuiet(t *testing.T, st *store.Store, kinds ...string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var active int
		if err := st.Reader().QueryRow(`SELECT count(*) FROM jobs WHERE state IN ('queued','running','paused')`).
			Scan(&active); err != nil {
			t.Fatal(err)
		}
		done := active == 0
		for _, k := range kinds {
			var n int
			if err := st.Reader().QueryRow(`SELECT count(*) FROM jobs WHERE kind = ? AND state = 'succeeded'`, k).
				Scan(&n); err != nil {
				t.Fatal(err)
			}
			done = done && n > 0
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs still active after 60 s (%d active, waiting for %v)", active, kinds)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func openStore(t *testing.T, cfg config.Config) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), cfg.StateDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// downgradeToR1 turns an R2 database into the one R1 leaves: the R1 schema
// is the baseline migration, which R2 only adds tables to, and no R2 job.
func downgradeToR1(t *testing.T, cfg config.Config) {
	t.Helper()
	st := openStore(t, cfg)
	defer st.Close()
	db := st.Writer()
	for _, table := range r2Tables {
		if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	for _, q := range []string{
		`DELETE FROM jobs WHERE kind IN ('hash','hash_now','relate')`,
		`DELETE FROM schema_migrations WHERE version = 2`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

type intent struct {
	ids      map[string]int64
	decision map[int64]string
	tags     map[int64]string
}

func readIntent(t *testing.T, st *store.Store) intent {
	t.Helper()
	in := intent{ids: map[string]int64{}, decision: map[int64]string{}, tags: map[int64]string{}}
	rows, err := st.Reader().Query(`SELECT id, path, coalesce(decision, '') FROM entries`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var path []byte
		var d string
		if err := rows.Scan(&id, &path, &d); err != nil {
			t.Fatal(err)
		}
		in.ids[string(path)] = id
		if d != "" {
			in.decision[id] = d
		}
	}
	rows.Close()
	rows, err = st.Reader().Query(`SELECT et.entry_id, t.name FROM entry_tags et JOIN tags t ON t.id = et.tag_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		in.tags[id] = name
	}
	rows.Close()
	return in
}

// TestServeUpgradesAnR1Database starts the R2 server on a database that R1
// left, with a scanned source, decisions, and tags (state-store "R1
// databases upgrade in place"): the migration is recorded, entries keep their
// IDs, decisions, and tags, and hashing starts without any request.
func TestServeUpgradesAnR1Database(t *testing.T) {
	disk := corpusDisk(t)
	cfg := r2Config(t, disk)
	setAdminPassword(t, cfg)

	origin, stop := serveAt(t, cfg)
	c := newBrowser(t, origin)
	csrf := c.login()
	src := addAndScan(t, c, origin, csrf)

	st := openStore(t, cfg)
	before := readIntent(t, st)
	st.Close()
	fotos, docs := before.ids["Fotos"], before.ids["Documentos"]
	if code, body := c.command("set-decision", `{"entry_id":"`+itoa(fotos)+`","decision":"keep"}`, origin, csrf); code != http.StatusOK {
		t.Fatalf("set-decision = %d %s", code, body)
	}
	code, body := c.command("create-tag", `{"name":"familia"}`, origin, csrf)
	var tag struct {
		Tag struct {
			ID int64 `json:"id"`
		} `json:"tag"`
	}
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &tag) != nil {
		t.Fatalf("create-tag = %d %s", code, body)
	}
	if code, body := c.command("set-tags", `{"entry_ids":["`+itoa(docs)+`"],"add":[`+itoa(tag.Tag.ID)+`]}`, origin, csrf); code != http.StatusOK {
		t.Fatalf("set-tags = %d %s", code, body)
	}
	st = openStore(t, cfg)
	awaitQuiet(t, st)
	before = readIntent(t, st)
	st.Close()
	stop()

	downgradeToR1(t, cfg)

	serveAt(t, cfg)
	st = openStore(t, cfg)
	defer st.Close()
	var name string
	if err := st.Reader().QueryRow(`SELECT name FROM schema_migrations WHERE version = 2`).Scan(&name); err != nil || name != "content" {
		t.Fatalf("migration 2 recorded as %q (%v), want content", name, err)
	}
	awaitQuiet(t, st, "hash")
	after := readIntent(t, st)
	if len(after.ids) != len(before.ids) {
		t.Fatalf("%d entries after the upgrade, %d before", len(after.ids), len(before.ids))
	}
	for path, id := range before.ids {
		if after.ids[path] != id {
			t.Errorf("%s: ID %d after the upgrade, %d before", path, after.ids[path], id)
		}
	}
	if after.decision[fotos] != "keep" || len(after.decision) != len(before.decision) {
		t.Errorf("decisions after the upgrade %v, before %v", after.decision, before.decision)
	}
	if after.tags[docs] != "familia" || len(after.tags) != len(before.tags) {
		t.Errorf("tags after the upgrade %v, before %v", after.tags, before.tags)
	}
	var hashJobs, checked, candidate int64
	if err := st.Reader().QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'hash' AND source_id = ?`, src).Scan(&hashJobs); err != nil {
		t.Fatal(err)
	}
	if err := st.Reader().QueryRow(`SELECT checked_bytes, candidate_bytes FROM content_coverage WHERE source_id = ?`, src).
		Scan(&checked, &candidate); err != nil {
		t.Fatal(err)
	}
	if hashJobs == 0 || candidate == 0 || checked != candidate {
		t.Errorf("after the upgrade: %d hash jobs, coverage %d of %d bytes", hashJobs, checked, candidate)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
