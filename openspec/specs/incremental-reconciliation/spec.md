# incremental-reconciliation Specification

## Purpose

Keeps an observed source current without notifications. Every active directory has a durable reconciliation scope; hints coalesce into dirty versions, and a polling schedule re-observes each scope within the atomic-boundary contract. Lag and gaps stay visible.

## Requirements

### Requirement: One reconciliation scope per active directory
Each active directory that is not a mount boundary SHALL have exactly one reconciliation scope. For an expanded directory, including a source root, the scope is its immediate listing. For any other directory, it is its bounded shallow probe. A scope SHALL keep a monotonic dirty version, the set of reasons since it was last clean, and its first and last hint times. Files, symlinks, mount boundaries, and inactive nodes SHALL have no scope.

#### Scenario: Hints coalesce
- **WHEN** a directory receives three hints before it is reconciled
- **THEN** it has one scope whose dirty version rose by three and whose reasons are the union of the three, and one reconciliation clears it

### Requirement: A scope is clean only after a pass that saw its latest hint
A hint SHALL increment the scope's dirty version immediately. A pass SHALL mark its scope clean only if the pass completed and no hint arrived after it started. A pass that is partial, fails, or yields a stale result SHALL leave the scope due again.

#### Scenario: Hint during a pass
- **WHEN** the owner requests a refresh of a directory while that directory's listing is in progress
- **THEN** the listing does not mark the scope clean, and the directory is listed again in a later pass

### Requirement: Scheduled reconciliation
After a source's first scan, the server SHALL reconcile each scope when it is due, without a command. An expanded directory is due every expanded interval (default 15 min) and any other directory every atomic interval (default 24 h), counted from its last pass. An unpaused inbox boundary and each of its settling directory arrivals are instead due every inbox polling interval (default 5 s). A zero interval disables that schedule. A scope's first due time SHALL be spread across one interval. A hinted scope is due at once.

#### Scenario: A26 new, changed, and deleted direct entries with notifications disabled
- **WHEN** after a scan one file is added to an expanded directory, a second file in it changes size, a third is deleted, and the expanded interval then passes with no command issued
- **THEN** the new file is an active node, the changed file's observation revision has incremented, the deleted file is tombstoned, and the directory's scope is clean

#### Scenario: Work spread over the interval
- **WHEN** a source with 900 never-reconciled expanded directories is first scheduled
- **THEN** their first due times are spread across one expanded interval, not all due at once

#### Scenario: Inbox polled on its own interval
- **WHEN** an unpaused inbox and an ordinary expanded directory of the same source were both reconciled at the same time
- **THEN** the inbox is due again 5 s later, and the ordinary directory 15 min later

### Requirement: Dispatch to the source's scan
Due scopes SHALL be handed to the source's one active scan job, and a given job SHALL receive a scope at most once. The scopes of unpaused inbox boundaries and their settling arrivals are the exception: they SHALL be handed to the source's intake job instead, as often as they are due. The scheduler SHALL NOT resume a paused job. It SHALL skip a source that is unconfigured, unavailable, needs reconfirmation, or was never scanned. A source-level job failure SHALL delay that source's next dispatch by the failure backoff. A failed pass SHALL retry with exponential backoff, capped at the interval.

#### Scenario: Budget-paused scan is not resumed
- **WHEN** a source's scan is paused with `node_budget_reached` and some of its scopes are due
- **THEN** no work is dispatched for that source until `start-scan` resumes the job, and its index health names the pause

#### Scenario: Unavailable source does not block others
- **WHEN** one of two sources becomes unavailable while both have due scopes
- **THEN** the other source keeps being reconciled on schedule, and the unavailable source's scopes are reconciled after it returns

#### Scenario: A busy directory cannot starve the source
- **WHEN** one directory is hinted again after each of its listings
- **THEN** it is listed at most once per job, and the source's other due scopes are still reconciled in each job

#### Scenario: Inbox scopes go to the intake job
- **WHEN** an inbox boundary is due while the source's scan is running a long listing
- **THEN** the source's intake job lists the inbox, and the scan does not receive the inbox's scope

### Requirement: Overdue scopes after downtime
Due times SHALL be durable. After the server was stopped, the first scheduler pass SHALL dispatch every overdue scope, oldest first, and the lag SHALL be visible until they are reconciled. Reconciliation SHALL never descend into an atomic unit.

#### Scenario: A27 process downtime
- **WHEN** the server is stopped for two hours and then started again
- **THEN** index health reports the overdue scopes and their lag, the first scheduler pass dispatches them without a command, and each atomic unit receives only its bounded shallow probe

### Requirement: Coverage gaps reconcile the active frontier
When observation continuity of a source cannot be trusted, the server SHALL hint every scope of that source and SHALL show a coverage gap with its reason and start time. The gap SHALL close once every scope has been reconciled since it started. Reconfirming a source identity is such an event.

#### Scenario: A27 coverage gap after reconfirmation
- **WHEN** the operator runs `curator source confirm` for a source with a changed identity
- **THEN** every scope of that source is dirty, index health shows a coverage gap with reason `epoch_changed`, and the gap closes after each scope's next pass, with no access below any atomic unit's immediate entries

### Requirement: Owner refresh command
`POST /api/commands/refresh-scope` SHALL take one `node_id` and a `scope`: `boundary` re-lists one active expanded directory one level or probes one other active directory; `subtree` applies to an active expanded directory, re-listing every active expanded directory in it and probing every other one, without descending into atomic units. It SHALL hint each affected scope and return HTTP 202 with the scan job ID.

#### Scenario: Refresh an atomic unit
- **WHEN** the owner sends `refresh-scope` with `scope: boundary` for an active atomic directory
- **THEN** the response is HTTP 202 with the source's scan job ID, and the job probes that directory within its shallow budget and catalogs nothing beneath it

#### Scenario: Subtree refresh respects atomic boundaries
- **WHEN** the owner sends `scope: subtree` for an expanded directory that holds two expanded and three atomic directories
- **THEN** the job lists the three expanded directories, probes the three atomic ones, and reads nothing below the atomic ones' immediate entries

#### Scenario: Refresh of a file
- **WHEN** `refresh-scope` targets a file, a symlink, a mount boundary, or an inactive node, or sends `subtree` for a directory that is not expanded
- **THEN** the response is HTTP 409 `invalid_node_state`, and nothing is dispatched

### Requirement: Notifications are low-latency hints
When the notification backend is on, the server SHALL watch, within the watch budget (default 8192):
- first, inbox boundaries;
- then source roots and active expanded directories, shallowest first;
- never an atomic unit or anything beneath one.

An event SHALL hint its directory's scope within the maximum delay (default 2 s), coalescing that scope's events within the window (default 500 ms). It SHALL dispatch the scope without waiting for the schedule, and SHALL leave the polling schedule unchanged.

#### Scenario: Event storm coalesces
- **WHEN** 300 events for one directory arrive within 400 ms
- **THEN** its dirty version rises once, one pass is dispatched within 2 s, and after that pass the directory is next due one expanded interval later, as for any pass

#### Scenario: Atomic units are not watched
- **WHEN** a source holds 10 expanded directories and 50 atomic units
- **THEN** at most the 10 expanded directories are watched, and nothing on or beneath an atomic unit is

### Requirement: Lost notifications open a coverage gap
A notification queue overflow, or a hint that could not be stored, SHALL open a coverage gap for the source with reason `notification_overflow`. The gap then hints every scope of the source, and closes as any coverage gap does.

#### Scenario: A27 watch overflow
- **WHEN** a source's notification queue overflows
- **THEN** index health shows a coverage gap with reason `notification_overflow`, and every active scope of the source is reconciled
- **AND** each atomic unit receives only its bounded shallow probe, and the gap closes after those passes

### Requirement: Watch limits degrade to polling
Some directories may be left unwatched: when the watch budget or an operating-system watch limit is reached, when the backend is off, or when a filesystem cannot report changes. Those directories SHALL stay on the polling schedule. Index health SHALL then show notifications as `degraded` or `polling_only`, with the reason and the count of unwatched directories. A watch that the system drops SHALL hint its directory.

#### Scenario: A27 watch limit reached
- **WHEN** the operating system refuses further watches after 100 of a source's 300 expanded directories are watched
- **THEN** index health shows `degraded`, with reason `os_watch_limit` and 200 unwatched directories
- **AND** a file added to an unwatched directory is cataloged at that directory's next scheduled pass

#### Scenario: Network filesystem is polled
- **WHEN** a source lies on a network filesystem
- **THEN** none of its directories is watched, and index health shows `polling_only` with reason `network_filesystem`
