package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"precious/internal/domain"
	"precious/internal/search"
)

// contentKeys are the EntryRow fields R2 adds (design D16).
var contentKeys = []string{"content_state", "copies", "candidate_bytes", "checked_bytes", "duplicated_bytes",
	"archive_state", "archive_id"}

// Every EntryRow of children, treemap, search, and the detail carries the R2
// content fields; before the content index fills them they are null.
func TestEntryRowContentFieldsPinned(t *testing.T) {
	e := newEnv(t)
	s := detailTree(t, e)
	check := func(where string, rows []map[string]json.RawMessage) {
		t.Helper()
		if len(rows) == 0 {
			t.Fatalf("%s: no rows", where)
		}
		for _, r := range rows {
			for _, k := range contentKeys {
				if v, ok := r[k]; !ok || string(v) != "null" {
					t.Errorf("%s %s: %s = %s, present %v; want null", where, r["path"], k, v, ok)
				}
			}
		}
	}
	var list struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/children", s.Root), 200, &list)
	check("children", list.Items)
	e.get(t, "/api/search?name=txt", 200, &list)
	check("search", list.Items)
	var tm struct {
		Entry map[string]json.RawMessage   `json:"entry"`
		Items []map[string]json.RawMessage `json:"items"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/treemap", s.Root), 200, &tm)
	check("treemap", append(tm.Items, tm.Entry))
	var detail struct {
		Entry map[string]json.RawMessage `json:"entry"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", s.ID("a")), 200, &detail)
	check("detail", []map[string]json.RawMessage{detail.Entry})
}

// A row with the content fields set renders them; a member row is
// addressed as "m<id>" and names its archive by ID string.
func TestEntryRowContentFieldsRender(t *testing.T) {
	r := search.Row{
		Member: 45, ArchiveID: 12, Kind: domain.EntryFile, State: "present", EffDecision: domain.DecisionKeep,
		ContentState: domain.ContentHashed, Copies: sql.NullInt64{Int64: 3, Valid: true},
		CandidateBytes: sql.NullInt64{Int64: 10, Valid: true}, CheckedBytes: sql.NullInt64{Int64: 8, Valid: true},
		DuplicatedBytes: sql.NullInt64{Int64: 0, Valid: true}, ArchiveState: "complete",
		Name: []byte("a.jpg"), Path: []byte("fotos/a.jpg"),
	}
	b, err := json.Marshal(rowJSON(&r))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"id": `"m45"`, "content_state": `"hashed"`, "copies": `3`, "candidate_bytes": `10`, "checked_bytes": `8`,
		"duplicated_bytes": `0`, "archive_state": `"complete"`, "archive_id": `"12"`, "decision": `null`,
		"tag_ids": `[]`,
	}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
}
