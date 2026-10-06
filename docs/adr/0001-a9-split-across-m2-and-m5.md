# ADR 0001: Acceptance scenario A9 is split between M2 and M5

- Status: accepted
- Date: 2026-10-02
- Context: `directory-first-curator-spec-v0.2.md` §13.1 (A9), §14 (M2 exit condition), §9.2, §10.1; OpenSpec change `m2-directory-policy`, design D12 and D17.

## Context

§14 lists A9 among the scenarios M2 must pass: "Tree totals after expansion/collapse and overlapping plan selections — no double counting or duplicate operations". M2 delivers expansion, collapse, totals, and review selections, but plans (§10.1) arrive only in M5. "Overlapping plan selections" cannot be tested before plans exist.

## Decision

- M2 closes the half of A9 that exists in M2: totals across expansion and collapse use the non-overlapping active frontier (§9.2), and overlapping review selections are normalized so a selected ancestor covers its selected descendants and each node's bytes count once (`inventory.NormalizeSelection`). Bulk commands apply at most once per distinct node.
- M5 re-verifies A9 for plan selections. Plan creation (§10.1 "Collapse overlapping ancestor/descendant selections into a non-overlapping set") reuses `inventory.NormalizeSelection`'s covered-by logic and adds an A9 plan test.

## Alternatives rejected

- Building a minimal plan model in M2 only to test A9: it would add M5 infrastructure early, which the project context forbids.
- Leaving A9 entirely to M5: M2 already has the totals and selection behavior A9 protects, and deferring its tests would leave expansion/collapse accounting unverified for three milestones.

## Consequences

The M5 change must list A9 among its acceptance scenarios and include a plan-selection test task.
