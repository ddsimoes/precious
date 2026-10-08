//go:build slow

package index

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/rules"
)

// Task 2.3 (r3 design, Risks): moving a folder holding 100,000 entries
// through MoveEntry, Reinherit, and Refold, in one transaction, takes at
// most 10 s on the development machine (timing skipped under the race
// detector). The subtree keeps its IDs and totals under the new path, and
// the folders above both places add up again.
func TestMoveAtScale(t *testing.T) {
	const entries, budget = 100_000, 10 * time.Second
	e := newEnv(t)
	root := e.disk("big", "/big", writable)
	root.Generated("Arquivo", entries, 100)
	root.Dir("Destino").File("a.txt", 1, testNow)
	e.scan("big")
	before := e.entries("big")
	arquivo, destino := get(t, before, "Arquivo"), get(t, before, "Destino")
	if arquivo.Dirs.Int64+arquivo.Files.Int64 != entries {
		t.Fatalf("Arquivo holds %d folders and %d files, want %d entries", arquivo.Dirs.Int64, arquivo.Files.Int64, entries)
	}

	rf := NewRefolder(rules.Default())
	start := time.Now()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		ctx := context.Background()
		if _, _, err := MoveEntry(ctx, tx, Move{Source: "big", Entry: domain.EntryID(arquivo.ID),
			NewParent: domain.EntryID(destino.ID), NewName: []byte("Arquivo antigo")}); err != nil {
			return err
		}
		if err := decisions.Reinherit(ctx, tx, domain.EntryID(arquivo.ID)); err != nil {
			return err
		}
		return rf.Refold(ctx, tx, "big", []domain.EntryID{domain.EntryID(arquivo.ID), domain.EntryID(before[""].ID)})
	})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("moved %d entries in %v", entries, took)
	if !raceEnabled && took > budget {
		t.Errorf("the move took %v, want at most %v", took, budget)
	}

	after := e.entries("big")
	if len(after) != len(before) {
		t.Fatalf("%d entries after the move, %d before", len(after), len(before))
	}
	for p, r := range before {
		if p == "" || p == "Destino" || p == "Destino/a.txt" {
			continue
		}
		moved := "Destino/Arquivo antigo" + strings.TrimPrefix(p, "Arquivo")
		if got := get(t, after, moved); got.ID != r.ID || got.TotalBytes != r.TotalBytes || got.TotalFiles != r.TotalFiles {
			t.Fatalf("%q moved to %q as %+v", p, moved, got)
		}
	}
	d := get(t, after, "Destino")
	if d.TotalFiles != destino.TotalFiles+arquivo.TotalFiles || d.TotalBytes != destino.TotalBytes+arquivo.TotalBytes {
		t.Errorf("Destino totals %d files %d bytes", d.TotalFiles, d.TotalBytes)
	}
	if r := get(t, after, ""); r.TotalFiles != before[""].TotalFiles || r.TotalBytes != before[""].TotalBytes {
		t.Errorf("root totals %d files %d bytes, were %d and %d", r.TotalFiles, r.TotalBytes,
			before[""].TotalFiles, before[""].TotalBytes)
	}
	for p, r := range after {
		if strings.HasPrefix(p, "Destino/Arquivo antigo") && strings.Contains(r.Indicators.String+r.Inside.String, `"Arquivo/`) {
			t.Fatalf("%q lists an old path: %s %s", p, r.Indicators.String, r.Inside.String)
		}
	}
}
