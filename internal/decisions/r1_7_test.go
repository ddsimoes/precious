package decisions

import (
	"context"
	"testing"

	"precious/internal/domain"
)

const (
	backupPC = "Backup_PC_2004"
	meusDocs = "Backup_PC_2004/C/Documents and Settings/Joao/Meus documentos"
)

// R1.7 Deciding a folder decides its subtree: on the regression corpus,
// discarding Backup_PC_2004 with Meus documentos kept discards every other
// entry inside it, and Home's totals move exactly those files' bytes from
// undecided to discard.
func TestR1_7DecidingAFolderDecidesItsSubtree(t *testing.T) {
	e := newEnv(t)
	s, gt := e.seedCorpus(t)
	entries := rawPaths(t, gt)
	totals := func() map[domain.Decision]Total {
		got, err := Totals(context.Background(), e.st.Reader(), s.Source)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	e.decide(t, one(s.ID(meusDocs), keep))
	before := totals()
	e.decide(t, one(s.ID(backupPC), discard))
	after := totals()

	var moved Total
	discarded, kept := 0, 0
	for _, en := range entries {
		if !below(en.Path, backupPC) {
			continue
		}
		if en.Path == meusDocs || below(en.Path, meusDocs) {
			own := inherit
			if en.Path == meusDocs {
				own = keep
			}
			wantEffective(t, e, s, en.Path, own, keep, ptr(meusDocs))
			kept++
			continue
		}
		wantEffective(t, e, s, en.Path, inherit, discard, ptr(backupPC))
		discarded++
		if en.Kind == domain.EntryFile {
			moved.Files++
			moved.Bytes += *en.Size
		}
	}
	if discarded < 50 || kept < 5 || moved.Bytes == 0 {
		t.Fatalf("the corpus has %d entries to discard and %d kept inside %s; the test needs both", discarded, kept, backupPC)
	}
	wantEffective(t, e, s, backupPC, discard, discard, ptr(backupPC))
	wantEffective(t, e, s, "HD antigo/backup pc velho", inherit, undecided, nil)

	if got, want := after[discard], (Total{Files: before[discard].Files + moved.Files, Bytes: before[discard].Bytes + moved.Bytes}); got != want {
		t.Errorf("discard totals %+v, want %+v", got, want)
	}
	if got, want := after[undecided], (Total{Files: before[undecided].Files - moved.Files, Bytes: before[undecided].Bytes - moved.Bytes}); got != want {
		t.Errorf("undecided totals %+v, want %+v", got, want)
	}
	if after[keep] != before[keep] {
		t.Errorf("keep totals moved from %+v to %+v", before[keep], after[keep])
	}
	checkConsistent(t, e.st)
}
