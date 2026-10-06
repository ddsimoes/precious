package jobs

import (
	"context"
	"maps"
	"testing"
)

// Design D13: RegisterClass records the class of its kind, Register records
// ClassReconciliation, and a pool kind has no class.
func TestRegisterClassRecordsClass(t *testing.T) {
	e := newEnv(t)
	h := handlerFunc(func(context.Context, Job, Runtime) error { return nil })
	r := e.runner(t, nil)
	r.Register(KindScan, h)
	r.RegisterClass(kindInteractive, h, ClassInteractive)
	r.RegisterPool(kindClassify, h, "classifier", 1)
	want := map[Kind]Class{KindScan: ClassReconciliation, kindInteractive: ClassInteractive}
	if !maps.Equal(r.classes, want) {
		t.Fatalf("classes = %v, want %v", r.classes, want)
	}
}

// RegisterClass panics as Register does: on a kind already registered by any
// register method, and after Start.
func TestRegisterClassPanics(t *testing.T) {
	e := newEnv(t)
	h := handlerFunc(func(context.Context, Job, Runtime) error { return nil })
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Runner) // must not panic
		call  func(*Runner)             // must panic
	}{
		{"duplicate kind",
			func(_ *testing.T, r *Runner) { r.RegisterClass("a", h, ClassBulk) },
			func(r *Runner) { r.RegisterClass("a", h, ClassInteractive) }},
		{"kind registered by Register",
			func(_ *testing.T, r *Runner) { r.Register("a", h) },
			func(r *Runner) { r.RegisterClass("a", h, ClassBulk) }},
		{"class kind registered by Register",
			func(_ *testing.T, r *Runner) { r.RegisterClass("a", h, ClassBulk) },
			func(r *Runner) { r.Register("a", h) }},
		{"pool kind",
			func(_ *testing.T, r *Runner) { r.RegisterPool("a", h, "p", 1) },
			func(r *Runner) { r.RegisterClass("a", h, ClassInteractive) }},
		{"after Start",
			func(t *testing.T, r *Runner) { start(t, r) },
			func(r *Runner) { r.RegisterClass("a", h, ClassInteractive) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.runner(t, nil)
			tc.setup(t, r)
			defer func() {
				if recover() == nil {
					t.Fatal("registration did not panic")
				}
			}()
			tc.call(r)
		})
	}
}
