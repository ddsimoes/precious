# Spec Delta

## Purpose

Turns a directory the owner chooses into an inbox. Each new immediate child of an inbox becomes one durable intake item: it is enrolled once, watched until it is quiet, and triaged within the shallow budget. Intake never moves, renames, or deletes anything.

## ADDED Requirements

### Requirement: The owner sets the inbox role
`set-inbox-role` SHALL give the `inbox` role to an active expanded directory, a source root included, and SHALL remove it. No other operation SHALL change a role. Each change SHALL carry the node's expected intent revision, increment it, and be audited. The role SHALL be refused with HTTP 409 `invalid_node_state` for anything that is not an active expanded directory, a node inside an atomic unit, and a directory inside or above another inbox.

#### Scenario: A40 inbox role inside an atomic ancestor
- **WHEN** the owner sets the inbox role, through a peek token, on `Downloads/Incoming` inside the atomic unit `Downloads`
- **THEN** the response is HTTP 409 `invalid_node_state` naming `Downloads` as the unit to refine, nothing is cataloged beneath `Downloads`, and once the owner has refined `Downloads` and then `Incoming`, setting the role on `Incoming` succeeds

#### Scenario: Nested inboxes refused
- **WHEN** `data/Incoming` is an inbox, and the owner sets the role on `data` and on `data/Incoming/Batch`
- **THEN** both responses are HTTP 409 `invalid_node_state`, and `data/Incoming` remains the only inbox

#### Scenario: Role changes are audited
- **WHEN** the owner sets the role on a directory and later removes it
- **THEN** two audit events record the directory, the old and new role, the time, and the client address, and the directory's intent revision rose by two

### Requirement: Activation lets the owner choose what to enroll
The request that sets the role SHALL state whether the directory's current children become intake items (`enroll_existing`). Children left out SHALL stay ordinary cataloged nodes. Every child first observed after activation SHALL be enrolled, whatever its category.

#### Scenario: Existing children left cataloged
- **WHEN** the owner activates an inbox that holds 3 children with `enroll_existing: false`, and a fourth child arrives later
- **THEN** only the fourth child is an intake item, and the other 3 stay ordinary nodes

### Requirement: The inbox boundary is locked
While a directory is an inbox, owner commands SHALL NOT collapse it or any ancestor, and SHALL NOT mark either `cleanup_candidate`. Policy SHALL NOT collapse them either. Removing the role SHALL end every open item with state `ignored` and reason `inbox_removed`, and cancel their pending classification requests. It SHALL then lift the lock.

#### Scenario: A40 attempted collapse of an inbox and its ancestor
- **WHEN** the owner collapses the inbox `data/Incoming`, and then its parent `data`
- **THEN** both responses are HTTP 409 `invalid_node_state` naming the inbox, and the arrivals stay active
- **AND WHEN** the owner removes the role and collapses `data` again
- **THEN** the collapse succeeds, and the former open items have ended with state `ignored`

### Requirement: One open intake item per arrival
Each enrolled immediate child of an inbox SHALL have at most one open intake item. Each kind of child SHALL be handled as follows:
- a directory is one package item;
- a regular file is one item;
- a symlink is a review-only item;
- a special entry gets no item: it is shown as an issue of the inbox and is never opened.

Repeated observations of the same occurrence SHALL update its open item. A different occurrence at the same name SHALL start a new item and end the old one with state `superseded`. An item whose occurrence is verified missing SHALL end with state `missing`.

#### Scenario: A32 repeated events for one copy
- **WHEN** a file grows across 6 polls while 40 notifications arrive for it
- **THEN** it has exactly one intake item, and the item's generation advanced only on the polls that saw a new size or modification time

#### Scenario: Replacement at the same name
- **WHEN** the arrival `report.pdf` is replaced on disk by a different file with the same name
- **THEN** the old item ends with state `superseded`, a new item starts for the new file, and none of the old item's triage carries over

#### Scenario: A41 the same arrival is not duplicated across a restart
- **WHEN** the server is killed while 50 arrivals are settling, is started again, and the inbox is polled
- **THEN** each arrival still has exactly one open item, and the restart changed no item's arrival time or generation

### Requirement: Generations change only with evidence
An item's generation SHALL advance only when its observed identity, size, or modification time changes, or, for a directory, its shallow descriptor changes. A poll that sees no change SHALL NOT advance the generation. It SHALL NOT start any triage or classification request either.

#### Scenario: Unchanged polls cost nothing
- **WHEN** a triaged arrival is polled 100 more times without change
- **THEN** its generation, its suggestions, and the number of classification requests are all unchanged

### Requirement: Heuristic readiness for files
A regular-file item SHALL become ready with basis `heuristic_stable` only at an observation that is at least the quiet interval after the observation that last saw a change (default 10 s, never below 5 s), and that finds the same identity, size, and modification time. Those are two unchanged observations at least 5 s apart. Until then the item SHALL stay `settling`, and no triage or classification request SHALL run for it.

#### Scenario: A32 slow file copy
- **WHEN** a file is written in steps 3 s apart for 30 s and then left alone
- **THEN** it stays settling with no suggestion during the copy, and becomes ready at the first poll at least 10 s after its last change

### Requirement: Temporary names never settle on their own
An item whose name matches a configured temporary-name pattern SHALL stay settling with blocker `temporary_name`, however long it is quiet. Only `mark-intake-ready` SHALL make it ready.

#### Scenario: A32 temporary-name arrival
- **WHEN** `download.zip.part` stays unchanged for an hour, and is then renamed to `download.zip`
- **THEN** during that hour its item was settling with blocker `temporary_name` and nothing was triaged or sent; after the rename that item ends with state `missing`, and a new item for `download.zip` settles normally

### Requirement: Directory readiness is provisional
A directory item SHALL become ready with basis `heuristic_stable` at a probe that is at least the quiet interval after the probe that last saw a change, and that finds the same shallow descriptor. Its triage SHALL be marked provisional and SHALL rest on that bounded probe alone. It SHALL never claim that copying finished or that nested content was seen.

#### Scenario: A33 quiet top level while nested writes continue
- **WHEN** an arriving directory's immediate entries stop changing while files keep being written two levels below
- **THEN** it becomes ready and is triaged provisionally from its shallow probe
- **AND** each poll reads it within the shallow budget, no node is cataloged beneath it, its basis never becomes `handoff_confirmed` without the owner, and nothing is moved

### Requirement: Owner handoff confirmation
`mark-intake-ready` SHALL record basis `handoff_confirmed` for an open item's current generation, given its expected generation. A later change to the item SHALL clear that basis and return the item to settling. A partial listing, an unavailable source, or a permission error SHALL never make an item ready.

#### Scenario: Change after mark ready
- **WHEN** the owner marks an item ready at generation 3, and the file then grows
- **THEN** the item is at generation 4, settling, with no handoff basis
- **AND** repeating the command with expected generation 3 returns HTTP 409 `revision_conflict`

### Requirement: Inboxes are polled
Each source SHALL have one durable intake job. It SHALL reconcile each unpaused inbox's immediate listing every inbox polling interval (default 5 s), and probe each settling directory arrival at the same interval. This SHALL happen whether or not notifications arrive. Polling SHALL resume after a restart without any open browser.

#### Scenario: A39 polling-only, read-only, without models
- **WHEN** three items arrive in an inbox on a read-only mount, with notifications off and no classifier configured
- **THEN** all three are enrolled, settle, are triaged by the rules, and can be reviewed, with no notification involved

#### Scenario: A41 rapid arrivals and hint storms across a restart
- **WHEN** 200 files arrive within one second, each producing several notifications, and the server restarts once while they settle
- **THEN** each file has one item
- **AND** the inbox is listed at most once per polling interval plus once per coalesced notification window
- **AND** each file gets at most one classification request per settled generation, within the reserved budget

#### Scenario: Inbox directory disappears
- **WHEN** the inbox directory is removed on disk, and its absence is verified
- **THEN** the inbox is shown as unavailable with its items in their last state, and no item is reported as organized

### Requirement: Arrivals are triaged once per settled generation
When an item becomes ready, the server SHALL decide that generation once. A directory is decided by the rules over its latest shallow probe, and a file by its name-derived kind. Policy SHALL never expand an arrival, and nothing SHALL be cataloged beneath it unless the owner refines it. An arrival's suggested triage SHALL never be `cleanup_candidate`; such a suggestion SHALL become `review`.

#### Scenario: Large installation is one fast item
- **WHEN** an installation directory with 100,000 descendants arrives and settles
- **THEN** it is one item decided from one shallow probe within the budget, nothing is cataloged beneath it, and its suggested triage is `review`, not `cleanup_candidate`

#### Scenario: Mixed arrival is not expanded
- **WHEN** the rules classify a ready arrival as `mixed`
- **THEN** it stays atomic and one item, in `needs_review` unless a rule proposes a destination for it

### Requirement: Pausing an inbox
`pause-inbox` SHALL pause and resume one inbox, given its expected revision. While an inbox is paused:
- it SHALL be reconciled only on the ordinary expanded schedule;
- its items SHALL NOT advance in readiness or triage;
- its pending classification requests SHALL be cancelled.

#### Scenario: A paused inbox sends nothing
- **WHEN** the owner pauses an inbox with 5 pending requests and 2 settling items, and new files arrive
- **THEN** the 5 requests are cancelled, nothing is sent, and no item becomes ready
- **AND WHEN** the owner resumes it
- **THEN** its items settle and are triaged

### Requirement: Ignoring and retrying an item
`ignore-intake` SHALL end an open item with state `ignored`, leaving its node as ordinary inventory. `retry-intake` SHALL decide again the current generation of an item in `needs_review` or `failed`, and SHALL send the classifier one more request under the inbox's grant. Both commands SHALL take the item's expected generation.

#### Scenario: Retry after a provider failure
- **WHEN** an item's classification failed because the provider was unavailable, and the owner retries it
- **THEN** one new request is created for the same generation, and the item returns to `classifying`

### Requirement: Stalled arrivals are surfaced
An item still settling after the stalled interval (default 24 h) SHALL be shown as stalled, with its last change time. No classification request SHALL be sent for an item that never became ready.

#### Scenario: Stalled copy
- **WHEN** a file keeps changing size every minute for 25 hours
- **THEN** it is shown as stalled with its last change time, and no request was ever created for it
