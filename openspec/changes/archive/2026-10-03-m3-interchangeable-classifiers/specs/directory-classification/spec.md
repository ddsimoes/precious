# Spec Delta

## MODIFIED Requirements

### Requirement: Classification precedence
A node's effective category SHALL be taken from the first available source in this order: owner override, a rule suggestion with a category other than `unknown`, an applied model suggestion, `unknown`. A model suggestion is applied only when its routing policy allows its category. The node SHALL record which source supplied the category. Every other current suggestion SHALL remain visible as an alternative.

#### Scenario: Owner override beats rule
- **WHEN** the owner sets `documents` on a directory the rules suggest is `application_installation`
- **THEN** the effective category is `documents` with source `owner`, and the rule suggestion is still listed as an alternative

#### Scenario: Applied model suggestion fills a rule unknown
- **WHEN** no rule matches a directory, and an applied model suggestion says `personal_media`
- **THEN** the effective category is `personal_media` with source `model`, and the rule's `unknown` suggestion is still listed

#### Scenario: Rule beats model
- **WHEN** the rules say `cache` and an applied model suggestion says `documents`
- **THEN** the effective category is `cache` with source `rule`, and the model suggestion is listed as a disagreeing alternative

### Requirement: Append-only suggestion history
Every rule or model suggestion SHALL be appended, never overwritten. It records its source kind, policy version, the descriptor and aggregate measurement it used, and the source epoch, observation revision, and intent revision it observed. A model suggestion SHALL also record its result status, typed measures, assessments, whether it was applied, and its provenance: profile and revision, adapter version, requested and returned model, task, prompt and privacy revisions, and whether it came from the cache. A newer current suggestion of the same kind SHALL mark the previous one superseded.

#### Scenario: Rescan appends history
- **WHEN** an unchanged directory is probed again by a later scan
- **THEN** a new rule suggestion is appended, and the earlier one remains in history marked superseded

#### Scenario: A21 model provenance survives a provider switch
- **WHEN** a directory has a model suggestion from a `typesafe` profile, and after a provider switch it gets one from a `structured_chat` profile
- **THEN** the older suggestion is superseded but still shows its `typesafe` profile, requested and returned model, and measures

### Requirement: Late results cannot win
A suggestion SHALL become current only if, when it is committed, the source epoch and the node's observation and intent revisions equal those the suggestion observed. A model suggestion SHALL also require an unchanged scope dirty version, the same latest descriptor, and an unchanged permission. Otherwise it SHALL be recorded as stale, and it SHALL NOT change the node's effective category, traits, preservation risk, suggested triage, or inventory mode.

#### Scenario: A7 owner override followed by a stale model response
- **WHEN** a model-kind suggestion is computed against a node's intent revision, the owner then sets a category override, and the suggestion is committed afterwards
- **THEN** the suggestion is recorded as stale, the effective category remains the owner's, and the node's traits, triage, and mode are unchanged

#### Scenario: A7 override outranks fresh suggestions
- **WHEN** after an owner override a current model-kind suggestion and a rescan's rule suggestion both propose a different category
- **THEN** both are recorded as current alternatives, and the effective category remains the owner's

#### Scenario: Model answer arrives after a new probe
- **WHEN** a directory is probed again with changed entries while its classification request is in flight
- **THEN** the late answer is recorded as stale against the old descriptor, and a new request is created for the new descriptor if the directory is still eligible

## ADDED Requirements

### Requirement: Model assessments can only add risk
A current model suggestion whose preservation-indicator assessment is `supported`, or whose native probability meets the policy's risk threshold, SHALL add preservation risk to the directory, even when its category is not applied. No model answer SHALL remove a trait, preservation risk, or protection; a directory with preservation risk or protection on or beneath it SHALL keep a triage other than `cleanup_candidate` whatever its category's source.

#### Scenario: Model sees user material in an apparent cache
- **WHEN** the rules say `cache`, and the model's preservation-indicator assessment has a native probability of 0.9 against a policy risk threshold of 0.7
- **THEN** the directory carries preservation risk citing the model suggestion, and its suggested triage is `review`, not `cleanup_candidate`
