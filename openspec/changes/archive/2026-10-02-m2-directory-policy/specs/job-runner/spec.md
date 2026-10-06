# Spec Delta

## MODIFIED Requirements

### Requirement: One active scan per source
While a scan job for a source is queued, running, or paused, a new `start-scan` for that source with a different idempotency key SHALL NOT create a second job. It SHALL resume the paused job, or else return the existing active job ID. A `refine-node` for a node in that source SHALL add the node to the active job's frontier and return that job's ID. With no active job, it SHALL start a job that processes only the frontier.

#### Scenario: Concurrent scan request
- **WHEN** `start-scan` is issued for a source whose scan is running
- **THEN** the response carries the running job's ID, and no second scan job is created

#### Scenario: Refine joins a running scan
- **WHEN** the owner refines a directory while that source's scan is running
- **THEN** the response carries the running scan's job ID, and that job lists and decides the refined directory before finishing

#### Scenario: Refine without an active scan
- **WHEN** the owner refines a directory in a source with no active scan
- **THEN** a job is created that lists the refined directory and decides its children, without re-listing the rest of the source

### Requirement: Machine-readable command errors
Command failures SHALL return a JSON body with a stable error code. The codes SHALL distinguish at least: `unknown_source`, `source_unconfigured`, `source_unavailable`, `source_identity_changed`, `permission_denied`, `not_found`, `budget_exhausted`, `idempotency_key_reused`, `revision_conflict` (HTTP 409), `protected` (HTTP 409), `invalid_node_state` (HTTP 409), and `stale_evidence` (HTTP 409).

#### Scenario: Scan of unavailable source
- **WHEN** `start-scan` targets a source whose root is missing
- **THEN** the job ends with code `source_unavailable`, or the command is rejected with it, and nothing is recorded as absent

#### Scenario: Collapse of a source root
- **WHEN** `collapse-node` targets a source root
- **THEN** the response is HTTP 409 with code `invalid_node_state`, and nothing changes

## ADDED Requirements

### Requirement: One active aggregate walk per node
While an aggregate walk for a node is queued, running, or paused, another `inspect-node` aggregate request for that node SHALL NOT create a second job; it SHALL return the active job's ID. Aggregate walks SHALL run under the per-device filesystem worker limit and SHALL be cancellable with `cancel-job`.

#### Scenario: Repeated aggregate request
- **WHEN** the owner requests an aggregate walk twice for the same node with different idempotency keys while the first is running
- **THEN** both responses carry the same job ID, and only one walk runs
