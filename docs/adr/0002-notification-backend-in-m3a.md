# ADR 0002: The notification backend moves from M2a to M3a

- Status: accepted
- Date: 2026-10-02
- Context: `directory-first-curator-spec-v0.2.md` §5.5.1, §5.5.3, §5.5.7, §9.4 (A32, A41), §13.1 (A27), §14 (M2a and M3a exit conditions, instructions to the coding agent); OpenSpec change `m2a-incremental-indexing`, design D19 and D22.

## Context

§14 lists "optional notifications" among the M2a deliverables, and its exit condition is "A26-A31 pass; polling works alone; atomic coverage remains honest". §5.5.1 requires the baseline to work with notifications entirely disabled and treats `fsnotify` as an optional low-latency optimization, never the sole source of truth. The instructions to the coding agent require polling-only reconciliation to be proven before notification optimizations are added.

The notification settings of §5.5.3 (the watcher backend, the 500 ms / 2 s coalescing window, and the watch budget with degradation to polling at OS watch limits) exist for latency. The latency that matters is inbox intake in M3a: §9.4 requires arrivals to be discovered by polling even without notifications, and A32 and A41 test repeated events, rapid arrivals, and coalesced dirty hints across restart. A27 covers "Watch overflow, watch limit, process downtime, or notification coverage gap"; its overflow and watch-limit cases cannot occur without a watcher.

## Decision

- M2a ships no notification backend. Polling alone keeps sources current: every active directory has a durable reconciliation scope, re-observed on the `[reconciliation]` schedule (D19), on hints, and on `refresh-scope`.
- The fsnotify/inotify backend, the watch budget, and the coalescing window move to M3a, where inbox latency (§9.4, A32, A41) needs them.
- A27's notification-overflow and watch-limit cases move to M3a with them. M2a passes A27 for process downtime and for coverage gaps after source reconfirmation.
- Index health (§5.5.7) reports each source's notifications as `polling_only`, so the absence of a low-latency path is visible rather than implied.

## Alternatives rejected

- Shipping an inotify watcher in M2a: it would be a second change source before polling is proven, against the order that §14 and the instructions to the coding agent require.
- Testing A27's overflow and watch-limit cases in M2a against a fake watcher: the degradation they would verify has no production counterpart until a real backend exists.

## Consequences

- The M3a change must list A27's notification-overflow and watch-limit cases among its acceptance scenarios and include tests for them, together with the backend, the watch budget, and the coalescing window.
- Watcher events in M3a feed the same durable dirty scopes as polling (§5.5.1); polling stays the fallback, and the schedule keeps running when a watch is lost.
- Until M3a, an on-disk change is seen at its directory's next scheduled pass (`reconciliation.expanded_interval`, default 15 minutes, for expanded directories) or after `refresh-scope`.
