package executor

import (
	"testing"
	"time"
)

// V1: a step into or out of the source root reopens the root for its facts
// before the outcome transaction. When the source's recorded observation
// differs by then, reopening writes it through the store's single writer,
// which the outcome transaction would hold; every step still completes and
// records done.
func TestReviewRootFactsOutsideTheOutcome(t *testing.T) {
	e := newEnv(t)
	root := photos(e)
	root.Dir("Vazio")
	e.scan()
	// After each step, the recorded capabilities differ from the disk's.
	e.hooks = Hooks{AfterStep: func(int64) error {
		_, err := e.st.Writer().Exec(`UPDATE sources SET capabilities = '{}' WHERE id = ?`, string(srcID))
		return err
	}}
	e.executor()
	action := e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "", Name: "a.jpg"},
		step{Op: opMkdir, To: "", Name: "Novo"},
		step{Op: opRename, From: "Arquivo", To: "Fotos", Name: "Arquivo"},
		step{Op: opRmdir, From: "Vazio"})
	done := make(chan error, 1)
	go func() { done <- e.attempt(e.jobOf(action)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("action: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the action did not end: the outcome waits for the writer it holds")
	}
	e.wantStates(action, actionDone, stateDone, stateDone, stateDone, stateDone)
	for path, want := range map[string]bool{"a.jpg": true, "Novo": true, "Fotos/Arquivo": true, "Arquivo": false,
		"Vazio": false} {
		if _, ok := e.lstat("/src", path); ok != want {
			t.Errorf("%s on the disk: %v, want %v", path, ok, want)
		}
	}
}
