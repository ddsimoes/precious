package executor

import (
	"strings"
	"testing"
)

// r4 task 1.6: the cleanup ops exist in the schema before this executor
// has steps for them. Until it does, such an item fails at intent, nothing
// reaches the disk or the index, and the action goes on with its next item.
func TestStepsWithoutAStepFailSafe(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.scan()
	action := e.action("cleanup", false,
		step{Op: opRecord, To: "Arquivo", Name: "1.json"},
		step{Op: opUnlink, From: "Fotos/2004/a.jpg", To: "Fotos/2004", Name: "a.jpg"},
		step{Op: opPurge, From: "Fotos/2004", To: "Fotos", Name: "2004"},
		step{Op: opVerify, To: "Arquivo", Name: "x"},
		step{Op: opMkdir, To: "Arquivo", Name: "Outro"})
	e.run(action)
	e.wantStates(action, actionDone, stateFailed, stateFailed, stateFailed, stateFailed, stateDone)
	for seq, op := range []string{opRecord, opUnlink, opPurge, opVerify} {
		if got := e.item(action, seq+1).Detail; !strings.Contains(got, "unknown step "+op) {
			t.Errorf("%s item detail %q", op, got)
		}
	}
	if got := e.writes(); got != 1 {
		t.Errorf("%d writes, want the one mkdir", got)
	}
	for _, p := range []string{"Fotos/2004/a.jpg", "Fotos/2004"} {
		if _, ok := e.lstat("/src", p); !ok {
			t.Errorf("%s is gone from the disk", p)
		}
	}
	e.idx.mu.Lock()
	purges, unlinks := len(e.idx.purges), len(e.idx.unlinks)
	e.idx.mu.Unlock()
	if purges != 0 || unlinks != 0 {
		t.Errorf("the index applied %d purges and %d unlinks", purges, unlinks)
	}
}
