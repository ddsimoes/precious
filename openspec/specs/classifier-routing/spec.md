# classifier-routing Specification

## Purpose

Decides whether, where, and at what cost model classification may run for each item. It covers operator-configured profiles and routing policies, owner permission per source, eligibility re-checked before every send, bounded fallback and escalation, budgets and usage, the semantic cache, and how results are applied.

## Requirements

### Requirement: Provider switching is configuration only
Provider profiles and routing policies SHALL come only from the operator's configuration file. A profile names an adapter, endpoint, model, deployment revision, secret reference, locality, limits, and price schedule. A policy names one task, a primary profile, at most two ordered fallbacks, the transient outcomes that permit fallback, an optional escalation profile, and an application policy. Changing a policy's profiles SHALL need only a configuration edit and a restart.

#### Scenario: A21 switch from Jev to a structured-chat endpoint
- **WHEN** the operator changes a policy's primary profile from a `typesafe` profile to a `structured_chat` profile and restarts the server
- **THEN** new suggestions carry the new profile's provenance, earlier suggestions keep theirs, owner overrides and dispositions are unchanged, and no migration or code change was needed

### Requirement: Remote classification needs owner permission per source
No classification request SHALL be sent for a source until the owner grants it a routing policy, the subset of that policy's profiles permitted for it, and the evidence scope `metadata`. An inbox SHALL instead need its own grant for each task, `directory_category` and `file_kind`. The source's grant SHALL never cover the inbox's open arrivals. Before granting, the owner SHALL see the exact request body that each permitted profile would receive, without credentials. The sample is a directory for a source and an arrival for an inbox. Grants and revocations SHALL be audited and carry a revision.

#### Scenario: Off by default
- **WHEN** profiles and policies are configured but the owner has granted nothing
- **THEN** no classification request is sent for any source, and every directory is classified by rules and owner decisions only

#### Scenario: Payload preview before granting
- **WHEN** the owner opens the grant form for a source
- **THEN** it shows, for a sample directory of that source, the exact request body each permitted profile would receive, with no credential in it

#### Scenario: An inbox needs its own grant
- **WHEN** a source has a `directory_category` grant, its inbox has none, and an `unknown` directory arrives in the inbox and settles
- **THEN** no request is sent for the arrival, and it is triaged by the rules only

### Requirement: Outbound payload is metadata only
A request SHALL carry only the privacy-transformed semantic descriptor: source-relative names of the directory and its ancestors, the observed entries with their kinds, markers, signals, file-kind counts, and coverage. It SHALL NOT carry the source root, the source label, node IDs, revisions, timestamps, or file content. Its instructions SHALL treat every filename as data and SHALL state that descendants were not inspected.

#### Scenario: Absolute root and source label stay local
- **WHEN** a directory at `/srv/disk/Backup/GetRight` in a source labeled "the owner's old laptop" is classified
- **THEN** the payload names `GetRight`, its ancestor `Backup`, and its observed entries, and contains neither `/srv/disk` nor the source label

### Requirement: Eligibility is re-checked before every send
When work is enqueued, and again immediately before each attempt, the server SHALL check that the source, or for an inbox arrival its inbox, still permits the profile. It SHALL also check that the profile is enabled and supports the task, the profile's rate limit, and the remaining budget. A failed check SHALL send nothing. Revoking a permission, or pausing an inbox, SHALL cancel the pending requests it covered. Data already sent SHALL be reported as sent, never as recalled.

#### Scenario: A43 permission revoked after enqueue
- **WHEN** the owner revokes a source's permission while 20 of its requests are queued and 3 were already sent
- **THEN** no further request is sent, the 20 are cancelled, and the response and settings page state that 3 requests were already sent and cannot be recalled

#### Scenario: A43 fallback to a new vendor
- **WHEN** the operator adds a fallback profile of a new vendor to a policy that a source uses
- **THEN** that profile receives nothing for the source until the owner explicitly permits it for that source

#### Scenario: Inbox grant revoked after enqueue
- **WHEN** the owner revokes an inbox's `file_kind` grant while 4 of its file requests are queued
- **THEN** the 4 are cancelled, nothing more is sent for them, and the source's own grant is unchanged

### Requirement: Fallback and escalation are bounded
Fallback SHALL happen only after a configured transient outcome, only to profiles the source permits, only within budget, in configured order, and at most twice per request. `auth_failed` and `misconfigured` SHALL never fall back; they SHALL be shown as operator errors. An ambiguous answer SHALL go to review without another call unless the policy names an escalation profile, which is called at most once. Escalation is off by default.

#### Scenario: A24 only a disallowed or over-budget fallback exists
- **WHEN** the primary profile fails with `rate_limited` and the only fallback profile is not permitted for the source, or would exceed the budget
- **THEN** nothing is sent to the fallback, the request ends with a visible failure, and the directory keeps its rule classification and stays in review if `unknown`

#### Scenario: Authentication failure is an operator error
- **WHEN** the primary profile answers `auth_failed`
- **THEN** no fallback or retry is attempted, and the settings page shows the profile's authentication error

### Requirement: Spending is reserved and capped
A profile with any price SHALL require non-zero per-job and daily caps. Before each attempt, the server SHALL reserve a conservative amount covering the input, the output limit, and the per-call charge, and SHALL refuse the attempt if the reservation would exceed a cap. Amounts SHALL be fixed-point in the schedule's currency. Unknown usage SHALL remain counted at its reservation, never as zero.

#### Scenario: A6 budget exhaustion
- **WHEN** the daily cap is reached while requests are still pending
- **THEN** no further metered request is sent that day, the pending requests wait for the next day, the usage page shows the cap as reached, and rules, scans, and pages keep working

#### Scenario: Unknown usage stays reserved
- **WHEN** a response carries no usage figures
- **THEN** the attempt's usage is recorded as unknown, and its full reservation counts toward the caps

### Requirement: Transient failures retry within bounds
A request that ends `rate_limited`, `overloaded`, `timeout`, `unavailable`, or `invalid_response` SHALL be retried at most the profile's configured number of times. Retries SHALL use jittered exponential backoff, wait at least any `Retry-After` value, and reserve budget like any attempt. After the last retry the request SHALL fail visibly. Other requests and all filesystem work SHALL continue meanwhile, and nothing on disk SHALL change.

#### Scenario: A6 rate limited, then answered
- **WHEN** Jev answers 429 with `Retry-After: 30` twice and then answers, with 2 retries configured
- **THEN** each retry is sent no earlier than 30 seconds after the previous answer, there are exactly 3 attempts, and the result is applied

#### Scenario: A6 malformed every time
- **WHEN** an endpoint returns malformed JSON on every attempt, with 2 retries configured
- **THEN** after 3 attempts the request ends `invalid_response`, no suggestion is stored, the directory is unchanged, and scans and pages are unaffected

### Requirement: Usage is accounted per attempt
Each attempt SHALL record its profile, task, outcome, latency, input and output usage or that they are unknown, its reservation, its estimated charge with the price-schedule revision, any provider-reported charge, and whether it was a cache hit. Totals SHALL be available by profile, task, and day, including error and abstention rates and estimated cache savings.

#### Scenario: Usage by profile
- **WHEN** the owner opens the classifier usage view after a day with 40 answered, 3 abstained, and 2 rate-limited attempts and 10 cache hits
- **THEN** it shows those counts, latencies, input and output tokens, the estimated charge, and the estimated savings from the cache, for that profile and task

### Requirement: Semantic results are cached
Validated results SHALL be cached under a key built from the canonical outbound descriptor and from the task revision, adapter version, profile deployment identity, prompt revision, generation settings, privacy-transform revision, and source. The key SHALL exclude node IDs, revisions, and observation times. A price change SHALL NOT invalidate the cache. A result from a model alias SHALL expire after the profile's alias lifetime.

#### Scenario: A25 repeated polls hit the cache
- **WHEN** scheduled passes probe an unchanged `unknown` directory again with new observation times
- **THEN** its classification reuses the cached result, and no request is sent

#### Scenario: A25 a relevant version change misses the cache
- **WHEN** the profile's deployment revision, the prompt revision, or the privacy-transform revision changes
- **THEN** the next classification of the same descriptor sends a new request, and the old cached result is not applied

### Requirement: Which directories are classified automatically
In a source with a permission for `directory_category`, a directory that is not an open inbox arrival SHALL get one automatic request when:
- its final rule result is `unknown`, because no rule matched or equal-priority rules conflict;
- it has no owner override;
- no current model suggestion exists for its latest descriptor under the current policy.

No other directory SHALL be classified automatically under the source's permission.

#### Scenario: Only rule-unknown directories are sent
- **WHEN** a scan probes an installation the rules recognize, a directory no rule matches, and an owner-overridden directory no rule matches
- **THEN** exactly one request is created, for the directory no rule matched

### Requirement: Owner-requested reclassification
`reclassify-scope` SHALL take an exact list of directory IDs, or one expanded directory with scope `subtree`, which covers its active non-boundary directories. A dry run SHALL report how many are eligible, how many would come from the cache, how many are blocked by permission, and an upper bound on cost. A real run SHALL return HTTP 202 with a job ID.

#### Scenario: Dry run before a bulk rerun
- **WHEN** the owner asks for a dry run over 120 directories, 30 of which are cached and 10 of which are in a source without permission
- **THEN** the response reports 110 eligible, 30 from the cache, 10 blocked, and the reserved cost of the other 80, and nothing is enqueued

### Requirement: Applying a model result
A result SHALL be appended as a model suggestion. It SHALL be current only if everything it was built from is unchanged since the request was built: the source epoch, the observation and intent revisions, the scope's dirty version, the latest descriptor, the permission, and, for an inbox arrival, the item's generation. Otherwise it SHALL be stale. A current suggestion SHALL become effective only if its label is in the policy's applied categories and meets any minimum native probability. Otherwise it SHALL await review. For a file arrival, an effective label becomes the item's file kind.

#### Scenario: A22 result awaiting review
- **WHEN** a label-only profile answers `documents` for an `unknown` directory, and the policy applies only `cache` automatically
- **THEN** the suggestion is current but not applied, the effective category stays `unknown`, and the directory is in the ambiguous queue with the suggestion shown

#### Scenario: Applied container category expands the directory
- **WHEN** an applied model suggestion makes an atomic `unknown` directory that is not an inbox arrival `mixed`, below the automatic refinement depth
- **THEN** its scope is hinted, and its next pass expands it one level within the node budget, as for a rule-classified container

#### Scenario: Arrival changed while its request was in flight
- **WHEN** an arrival grows after its request was sent, and the answer arrives afterwards
- **THEN** the answer is stored as a stale suggestion, the item is settling at its new generation, and its triage is unchanged by the answer

### Requirement: Which inbox arrivals are classified automatically
An inbox arrival SHALL get one automatic request for each settled generation when all of these hold:
- it is ready;
- its inbox has a grant for its task;
- its rule result is `unknown`: for a directory, no rule matched or equal-priority rules conflict; for a file, its name-derived kind is `unknown`.

An owner retry SHALL add one more request. No other arrival SHALL be sent.

#### Scenario: Only unknown, ready arrivals are sent
- **WHEN** an inbox with a `file_kind` grant receives `manual.pdf` and `data.bin`, which both settle, and `notes.bin`, which keeps changing
- **THEN** exactly one request is created, for `data.bin`, after it settles

### Requirement: Inbox requests go first
Within one source's classifier work, the server SHALL send requests for inbox arrivals before automatic requests for other directories.

#### Scenario: Arrival ahead of a backlog
- **WHEN** a source has 100 automatic directory requests pending and an arrival's request is created
- **THEN** the arrival's request is the next request of that source to be sent
