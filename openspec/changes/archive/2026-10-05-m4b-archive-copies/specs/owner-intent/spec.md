# Spec Delta

## MODIFIED Requirements

### Requirement: Cleanup marks can carry copy evidence
An item of `set-disposition` with disposition `cleanup_candidate` MAY name a copy result. The result SHALL name that node as its copy: the inside folder or archive, either side of a `same` result, or either file of a file result, and that side SHALL NOT lie inside an archive. The result SHALL be `current`. The evidence SHALL stay attached until the node's disposition is next set. Protection and inbox locks SHALL refuse the mark as without evidence.

#### Scenario: Mark a copy with its evidence
- **WHEN** the owner marks `fotos-b` as `cleanup_candidate` naming the current result "`fotos-b` inside `fotos`"
- **THEN** the disposition is set, the inspector shows "inside `fotos`" with the matched size and the search time, and the audit event names the result

#### Scenario: Mark an archive with its evidence
- **WHEN** the owner marks the file `bkp.tar.gz` as `cleanup_candidate` naming the current result "`bkp.tar.gz` same as `bkp`"
- **THEN** the disposition is set with that evidence, and `bkp.tar.gz` appears in the `marked` queue with "same as `bkp`"

#### Scenario: Stale evidence refused
- **WHEN** the named result reads `changed`
- **THEN** a single-item request fails with HTTP 409 code `stale_evidence`, and nothing changes

#### Scenario: Stale evidence in a bulk request
- **WHEN** a bulk request names three current results and one `changed` result
- **THEN** three items are applied, and the fourth is reported with outcome `stale_evidence`

#### Scenario: Protection still refuses
- **WHEN** `fotos-b` is pinned and the owner marks it with a current result
- **THEN** the response is HTTP 409 code `protected` naming the pin, and no evidence is stored

#### Scenario: Evidence for the wrong node or disposition
- **WHEN** a request names a result for a node the result does not name as its copy, or names a result with disposition `preserve`
- **THEN** the response is HTTP 400 code `invalid_request`, and nothing changes

#### Scenario: A copy inside an atomic unit
- **WHEN** the owner names the result "`Elements/bkp-old-laptop` inside `bkp`" for the atomic unit `Elements`
- **THEN** the response is HTTP 400 code `invalid_request` stating that the copy lies inside `Elements` and must be refined before it can be marked

#### Scenario: A copy inside an archive
- **WHEN** the owner marks `bkp.tar.gz` naming the result "`home/fotos` in `bkp.tar.gz` inside `fotos`"
- **THEN** the response is HTTP 400 code `invalid_request` stating that the copy is a part of `bkp.tar.gz` and cannot be marked on its own

#### Scenario: Evidence ends with the mark
- **WHEN** the owner later sets `fotos-b` to `preserve`
- **THEN** its copy evidence is no longer shown as attached
