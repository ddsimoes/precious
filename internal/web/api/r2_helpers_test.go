package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"precious/internal/clock"
	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/rules"
)

// contentWorld is the regression corpus scanned as source "corpus", with
// the content of a complete hashing run (SeedContent), then one real relate
// pass: relations, dir_dups, and the review rows of its generation.
type contentWorld struct {
	*env
	s    *indextest.Seeded
	gt   corpus.GroundTruth
	arcs map[string]*indextest.SeededArchive
	keys int
}

func newContentWorld(t *testing.T) *contentWorld {
	t.Helper()
	e := newEnv(t)
	sfs := synthfs.New()
	root, gt := corpus.BuildSynth(sfs, "/corpus", corpus.Corpus())
	e.scanSynth(t, sfs, "corpus", "/corpus", root)
	w := &contentWorld{env: e, gt: gt, s: indextest.Attach(t, e.st, "corpus")}
	w.arcs = w.s.SeedContent(e.st, gt)
	w.relate(t)
	return w
}

// relate runs one relate pass with the review lists as its after hook, as
// serve wires it.
func (w *contentWorld) relate(t *testing.T) {
	t.Helper()
	h := relations.NewHandler(w.st, clock.Real{}, config.Defaults().Duplicates, func(ctx context.Context, gen int64) error {
		return review.Refresh(ctx, w.st, gen)
	})
	if err := h.Run(context.Background(), jobs.Job{Kind: relations.KindRelate}, scanRuntime{}); err != nil {
		t.Fatal(err)
	}
}

// id is the entry ID of a corpus path, as an API ref.
func (w *contentWorld) id(path string) string { return w.s.ID(path).String() }

// member is the API ref of a member of a corpus archive.
func (w *contentWorld) member(t *testing.T, archive, path string) string {
	t.Helper()
	a, ok := w.arcs[archive]
	if !ok {
		t.Fatalf("no archive %q", archive)
	}
	return domain.Ref{Member: a.Member(path)}.String()
}

// group returns the raw paths of the ground truth's duplicate group of
// path ("archive!member" for members), and its digest.
func (w *contentWorld) group(t *testing.T, path string) ([]string, string) {
	t.Helper()
	for _, d := range w.gt.Duplicates {
		var paths []string
		found := false
		for _, c := range d.Copies {
			raw, err := base64.StdEncoding.DecodeString(c.PathB64)
			if err != nil {
				t.Fatal(err)
			}
			paths = append(paths, string(raw))
			found = found || string(raw) == path
		}
		if found {
			return paths, d.SHA256
		}
	}
	t.Fatalf("%q is in no duplicate group", path)
	return nil, ""
}

// commands serves the decisions commands on a mux of their own.
func (w *contentWorld) commands(t *testing.T) *http.ServeMux {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{Store: w.st, Clock: clock.Real{}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	h := commands.New(commands.Options{Store: w.st, Jobs: r, Logger: logger})
	decisions.RegisterCommands(h, decisions.New(clock.Real{}, rules.Default(), index.StartScan))
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", h)
	return mux
}

// post sends a command with a fresh Idempotency-Key and returns the status
// and body.
func (w *contentWorld) post(t *testing.T, mux *http.ServeMux, name, body string) (int, string) {
	t.Helper()
	w.keys++
	req := httptest.NewRequest(http.MethodPost, "/api/commands/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "key-"+strconv.Itoa(w.keys))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// The decoded R2 shapes.

type amountRes struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

type coverageRes struct {
	Candidate  amountRes `json:"candidate"`
	Checked    amountRes `json:"checked"`
	Unchecked  amountRes `json:"unchecked"`
	Unreadable amountRes `json:"unreadable"`
}

// contentRow is an EntryRow with the R2 fields.
type contentRow struct {
	row
	Composition     []map[string]any `json:"composition"`
	ContentState    *string          `json:"content_state"`
	Copies          *int64           `json:"copies"`
	CandidateBytes  *int64           `json:"candidate_bytes"`
	CheckedBytes    *int64           `json:"checked_bytes"`
	DuplicatedBytes *int64           `json:"duplicated_bytes"`
	ArchiveState    *string          `json:"archive_state"`
	ArchiveID       *string          `json:"archive_id"`
}

type copyRes struct {
	Ref         string  `json:"ref"`
	SourceID    string  `json:"source_id"`
	Path        string  `json:"path"`
	PathB64     []byte  `json:"path_b64"`
	ArchiveID   *string `json:"archive_id"`
	HardLink    bool    `json:"hard_link"`
	Offline     bool    `json:"offline"`
	Decision    *string `json:"decision"`
	EffDecision string  `json:"eff_decision"`
}

type relationRes struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	Self           string     `json:"self"`
	Other          contentRow `json:"other"`
	MatchedBytes   int64      `json:"matched_bytes"`
	RedundantBytes int64      `json:"redundant_bytes"`
	OnlyHere       amountRes  `json:"only_here"`
	OnlyThere      amountRes  `json:"only_there"`
}

type detailRes struct {
	Entry     contentRow `json:"entry"`
	Ancestors []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"ancestors"`
	Classification struct {
		Category *string `json:"category"`
		Rules    []any   `json:"rules"`
	} `json:"classification"`
	Intent struct {
		Decision    *string `json:"decision"`
		EffDecision string  `json:"eff_decision"`
		Tags        []struct {
			ID  int64 `json:"id"`
			Own bool  `json:"own"`
		} `json:"tags"`
	} `json:"intent"`
	Stats *struct {
		Dirs  int64 `json:"dirs"`
		Files int64 `json:"files"`
	} `json:"stats"`
	Content *struct {
		State       string    `json:"state"`
		SHA256      *string   `json:"sha256"`
		CheckedAt   *string   `json:"checked_at"`
		Copies      []copyRes `json:"copies"`
		CopiesCount int       `json:"copies_count"`
	} `json:"content"`
	Relations []relationRes `json:"relations"`
	Archive   *struct {
		Format        string  `json:"format"`
		State         string  `json:"state"`
		Detail        *string `json:"detail"`
		Members       int64   `json:"members"`
		UnpackedBytes int64   `json:"unpacked_bytes"`
	} `json:"archive"`
	Coverage coverageRes `json:"coverage"`
}

// detail reads GET /api/entries/{ref}.
func (w *contentWorld) detail(t *testing.T, ref string) detailRes {
	t.Helper()
	var d detailRes
	w.get(t, fmt.Sprintf("/api/entries/%s", ref), 200, &d)
	return d
}

// contentPage is a children or search page with the R2 fields.
type contentPage struct {
	Items      []contentRow `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// allChildren reads every children page of ref at limit rows per page.
func (w *contentWorld) allChildren(t *testing.T, ref, query string, limit int) []contentRow {
	t.Helper()
	var out []contentRow
	cursor := ""
	for {
		target := fmt.Sprintf("/api/entries/%s/children?limit=%d%s", ref, limit, query)
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		var p contentPage
		w.get(t, target, 200, &p)
		if len(p.Items) > limit {
			t.Fatalf("%s: %d rows", target, len(p.Items))
		}
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out
		}
		cursor = *p.NextCursor
	}
}
