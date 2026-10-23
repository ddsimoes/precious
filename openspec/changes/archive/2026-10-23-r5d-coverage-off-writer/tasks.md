# Tasks

## 1. The change

- [x] 1.1 Split `recomputeCoverage` into `coverageDrift` (read) and `applyDrift` (write), with the `coverageRead` seam (design D1, D2). Owns `internal/content/plan.go`, its tests, and the `docs/operator.md` Coverage section. Verify with tests:
  - **Writer not held** (scenario "Signing in while coverage is recomputed"): while the seam holds the read open, a write through the store commits within 1 s. Before the change this test must fail by timing out.
  - **Concurrent hashing commit kept** (scenario "A hashing commit during the recomputation is kept"): through the seam, a delta commit between read and write survives, and the final coverage equals `indextest`'s full recomputation.
  - **Drift repair:** with the stored rows corrupted before the plan, the plan restores the exact figures.
  - **Edge cases:** a source with no rows, a new source without a coverage row, and a source removed between read and write.
  - **Unchanged behaviour:** the existing coverage tests, and R2.1, pass unchanged.

  Run `go test -race ./internal/content`.

## 2. Integration

- [x] 2.1 Full verification: gofmt, `go vet ./...`, `go test -race ./...`, `npm test`, `npm run e2e`, `scripts/e2e-docker.sh`, and the six builds.
- [x] 2.2 An independent review. Fix each finding with a test that fails first.
- [x] 2.3 Open a PR and wait for CI. Deploy to the test server: back up the database and keep the previous binary. Restart, then measure sign-in latency every second for 10 minutes while the startup hashing jobs plan. No request may take over 2 s. Record the result in the Addendum.
- [x] 2.4 The owner signs off. Record it in the Addendum.
