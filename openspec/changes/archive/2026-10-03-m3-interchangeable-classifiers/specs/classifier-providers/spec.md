# Spec Delta

## Purpose

Defines the provider-neutral boundary between curator's domain questions and classifier backends, the shipped adapters, and how each backend's answers are validated and typed, so that providers can be swapped without changing discovery, storage, or the UI.

## ADDED Requirements

### Requirement: Provider-neutral classification contract
Every classifier SHALL answer one versioned task (`directory_category` or `file_kind`, with its labels and semantic questions) for one scoped item descriptor. It SHALL return a status of `answered`, `abstained`, or `refused`; a label from the task only when answered; assessments keyed by domain question IDs; optional typed measures; evidence references to the supplied descriptor only; provenance; and usage with explicit unknowns. No stored, displayed, or API field SHALL be a vendor primitive.

#### Scenario: Native choice becomes a domain category
- **WHEN** the `typesafe` adapter receives a native choice of `application_installation` with a complete option distribution
- **THEN** the result has category `application_installation`, one native-probability measure per label, and one derived-confidence measure, and no stored or API field is named after the native primitive

#### Scenario: Evidence references stay within the descriptor
- **WHEN** a result cites an evidence ID that the supplied descriptor does not contain
- **THEN** the result is rejected as `invalid_response`, and no suggestion is stored

### Requirement: Shipped adapters
Profiles SHALL select one of four adapters by configuration: `disabled` sends nothing; `fake` answers from deterministic fixtures, including abstention, missing measures, errors, and delays; `typesafe` speaks the native Jev protocol; `structured_chat` speaks an OpenAI-compatible chat-completion protocol in `json_schema` or `json_object` mode. Choosing among them SHALL NOT require recompiling or migrating.

#### Scenario: Disabled profile
- **WHEN** a routing policy's primary profile uses the `disabled` adapter
- **THEN** no request is sent, directories keep their rule classification, and the inspector states that model classification is disabled for the source

### Requirement: Typed measures are never fabricated
Each measure SHALL record its kind (`native_probability`, `derived_confidence`, `self_reported_score`, or `uncalibrated_score`), target, value, and a named scale. A result without measures SHALL be stored without measures. A number that a model wrote into generated JSON SHALL be a `self_reported_score`. A native yes-probability SHALL stay a probability, without a categorical verdict or a confidence derived from it.

#### Scenario: A22 label-only backend
- **WHEN** a `structured_chat` profile without native probabilities answers `documents` for a directory
- **THEN** the suggestion is stored with category `documents` and no measure, and no probability or confidence of 1 appears anywhere

#### Scenario: A23 yes-probability kept as a probability
- **WHEN** Jev answers the coherent-unit question with a yes-probability of 0.62
- **THEN** the assessment carries a `native_probability` of 0.62, no categorical answer, and no confidence measure

#### Scenario: A23 self-reported score keeps its kind
- **WHEN** a chat model includes `"confidence": 0.9` in its JSON answer
- **THEN** it is stored as a `self_reported_score` on its own scale, and it is never shown or used as a native probability

### Requirement: Invalid output is rejected
An adapter SHALL reject, as `invalid_response` with a recorded reason, any answer with malformed JSON, a label outside the task, a missing required answer, a non-finite number, a value outside its declared range, or a native distribution that is incomplete or does not sum to 1 within tolerance. A rejected answer SHALL NOT become a suggestion and SHALL NOT be replaced by a zero or a default label.

#### Scenario: A23 invalid distribution
- **WHEN** a native distribution omits one option or sums to 1.4
- **THEN** the attempt ends `invalid_response` naming the distribution, and no suggestion or measure is stored

#### Scenario: A23 label outside the task
- **WHEN** a chat model answers category `games`, which is not in the task
- **THEN** the attempt ends `invalid_response`, and the directory's classification is unchanged

### Requirement: Explicit attempt outcomes
Every attempt SHALL end in exactly one recorded outcome: `answered`, `abstained`, `refused`, `invalid_response`, `truncated`, `rate_limited`, `overloaded`, `timeout`, `unavailable`, `auth_failed`, `misconfigured`, or `unsupported`. Each adapter maps its own protocol's statuses; those mappings SHALL NOT be presented as universal provider semantics. A truncated answer SHALL NOT be parsed as a partial result.

#### Scenario: A23 refusal
- **WHEN** a chat endpoint returns a refusal instead of an answer
- **THEN** the attempt ends `refused`, the refusal is recorded as a model outcome, and no category is stored

#### Scenario: A23 truncation
- **WHEN** a chat endpoint stops at its output-token limit
- **THEN** the attempt ends `truncated`, and no part of the output is parsed

#### Scenario: Jev rate limit mapped by its adapter
- **WHEN** Jev answers HTTP 429 or 529
- **THEN** the attempt ends `rate_limited` or `overloaded`, honoring any `Retry-After` value, and HTTP status codes from other adapters are mapped by those adapters' own rules

### Requirement: Bounded outbound transport
Requests SHALL go only to the operator-configured endpoint of the selected profile. They SHALL carry a deadline and a response-size limit, SHALL NOT follow redirects, and SHALL NOT forward credentials to any other origin. Endpoints, models, and credentials SHALL NOT come from a browser request, a filename, or a model result.

#### Scenario: Redirect not followed
- **WHEN** a configured endpoint answers with a redirect to another host
- **THEN** the redirect is not followed, no credential reaches the other host, and the attempt ends `misconfigured`

#### Scenario: Oversized response
- **WHEN** a response exceeds the profile's response-size limit
- **THEN** reading stops at the limit and the attempt ends `invalid_response`

### Requirement: Capability claims are checked before use
Each profile SHALL declare the tasks it supports, its structured-output mode, and whether it returns native distributions. A request for an undeclared task SHALL NOT be sent: it SHALL end `unsupported` and go to review instead of producing output.

#### Scenario: Task not supported by the profile
- **WHEN** a policy for `file_kind` names a profile that declares only `directory_category`
- **THEN** `check-config` reports the policy and the profile, and the server does not start
