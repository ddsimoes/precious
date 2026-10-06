# Spec Delta

## MODIFIED Requirements

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

### Requirement: Which directories are classified automatically
In a source with a permission for `directory_category`, a directory that is not an open inbox arrival SHALL get one automatic request when:
- its final rule result is `unknown`, because no rule matched or equal-priority rules conflict;
- it has no owner override;
- no current model suggestion exists for its latest descriptor under the current policy.

No other directory SHALL be classified automatically under the source's permission.

#### Scenario: Only rule-unknown directories are sent
- **WHEN** a scan probes an installation the rules recognize, a directory no rule matches, and an owner-overridden directory no rule matches
- **THEN** exactly one request is created, for the directory no rule matched

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

## ADDED Requirements

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
