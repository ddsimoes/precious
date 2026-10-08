package executor

import (
	"testing"
)

// r4 D12 (tasks 1.6, 2.3): the cleanup steps act only inside the
// quarantine, for what they were planned for. A record with no done
// rename, an unlink outside the quarantine, and a purge with no ready
// check never reach the disk or the index; the purge stops the action.
func TestCleanupStepsOutsideTheirPlace(t *testing.T) {
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
	e.wantStates(action, actionStopped, stateNotAttempted, stateChanged, stateChanged, stateNotAttempted,
		stateNotAttempted)
	if r := e.item(action, 3).Reason; r != reasonCheckStale {
		t.Errorf("purge reason %q, want check_stale", r)
	}
	if got := e.writes(); got != 0 {
		t.Errorf("%d writes, want none", got)
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
