# directory-classification Specification

## Purpose

Assigns each probed directory a primary category and orthogonal traits using deterministic, versioned rules over recorded evidence. Owner and suggestion sources are ranked by a fixed precedence, and every suggestion is kept with the revisions it observed, so labels stay explainable and late work can never override newer owner intent.

## Requirements

### Requirement: Category taxonomy
Each classified directory SHALL have exactly one effective primary category from: `personal_media`, `source_project`, `documents`, `application_installation`, `application_user_data`, `application_configuration`, `cache`, `temporary_data`, `generated_artifacts`, `os_installation`, `download_collection`, `mixed`, `unknown`. A directory not yet classified SHALL report no category, which is distinct from `unknown`. Category SHALL be independent of inventory mode, disposition, and protection.

#### Scenario: Category independent of inventory mode
- **WHEN** a directory classified `source_project` is refined by the owner
- **THEN** its category stays `source_project` while its inventory mode becomes `expanded`

#### Scenario: Node not yet classified
- **WHEN** a directory discovered before this release has not been probed since the upgrade
- **THEN** it reports no category and is labeled "not yet classified", never `unknown`

### Requirement: Deterministic versioned rules
Rule suggestions SHALL be computed only from a directory's latest descriptor and its current aggregate measurement, by an embedded rule set whose version is recorded with each suggestion. Each suggestion SHALL list the matched rule IDs and the evidence IDs each rule cited. A rule match SHALL NOT carry a probability, confidence, or score.

#### Scenario: Installation recognized
- **WHEN** a probed directory's examined entries include `setup.exe`, `uninstall.exe`, and `GetRight.hlp`, and no user-data indicator
- **THEN** the rule suggestion is `application_installation`, citing the evidence IDs of the matched entries and the rule-set version, with no numeric confidence

#### Scenario: Identical evidence gives identical result
- **WHEN** two directories have descriptors with the same digest and no aggregate measurement
- **THEN** both receive the same rule category and traits

#### Scenario: No rule matches
- **WHEN** no rule matches a directory's evidence
- **THEN** the rule suggestion is `unknown` with reason `no_rule_matched`

### Requirement: Orthogonal traits
Rules SHALL also assign zero or more traits from: `backup_container`, `contains_vcs`, `contains_database`, `contains_credentials`, `contains_user_material`, `possible_generated_content`. Each trait SHALL cite its evidence. Traits SHALL NOT be presented as proof of ownership, recoverability, or reproducibility.

#### Scenario: Source project with version control
- **WHEN** a directory's evidence shows `.git` present and `go.mod` among its entries
- **THEN** it is suggested `source_project` with trait `contains_vcs`, citing the `.git` marker result

### Requirement: Rule conflicts produce review
When the matching rules of highest priority suggest different categories, the suggestion SHALL be `unknown` with reason `conflicting_rules`, and SHALL list every competing rule and category. A matching rule of strictly higher priority SHALL win over lower-priority rules.

#### Scenario: Equal-priority conflict
- **WHEN** two rules of equal priority match one directory, suggesting `cache` and `documents`
- **THEN** the suggestion is `unknown` with reason `conflicting_rules`, and both competing rules are shown

#### Scenario: Priority resolves overlap
- **WHEN** a priority-20 rule suggests `application_user_data` and a priority-10 rule suggests `application_installation` for one directory
- **THEN** the suggestion is `application_user_data`, and the lower-priority match is still listed

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

### Requirement: Preservation risk
A directory SHALL be flagged with preservation risk when its effective category is `application_user_data` or when it carries `contains_user_material`, `contains_database`, or `contains_credentials`. The flag SHALL cite the evidence behind it. Preservation risk SHALL apply whatever other category the directory has.

#### Scenario: A4 saves inside an apparent installation
- **WHEN** a directory's examined entries include `game.exe`, `uninstall.exe`, and a directory named `saves`
- **THEN** it carries `contains_user_material` and preservation risk citing the `saves` entry, and its suggested triage is `review`, never `cleanup_candidate`

#### Scenario: A4 profile database inside an installation
- **WHEN** a directory's examined entries include `app.exe`, `unins000.exe`, and `profile.sqlite`
- **THEN** it carries `contains_database` and preservation risk, and no cleanup suggestion is made

### Requirement: Suggested triage
Each classified directory SHALL get a suggested triage of `preserve`, `cleanup_candidate`, `review`, or `expand`, derived from its effective category's §4.3 default. Preservation risk, or effective protection on or beneath the node, SHALL turn a `cleanup_candidate` suggestion into `review`. A suggestion SHALL NOT change disposition, protection, or any file.

#### Scenario: Cache suggested as a candidate
- **WHEN** a directory's evidence shows a `CACHEDIR.TAG` marker and no preservation indicator
- **THEN** its suggested triage is `cleanup_candidate`, and its disposition stays `unreviewed`

#### Scenario: Protected cache is not a candidate
- **WHEN** the same cache directory lies inside a protected path
- **THEN** its suggested triage is `review`

### Requirement: Evidence-based explanation
Each effective classification SHALL carry an explanation built only from matched rules, cited evidence, and coverage. It SHALL state how many entries were examined, whether the listing was complete, and that content was not inspected. It SHALL NOT claim that a unit contains no personal files, can be obtained again, or holds no unique content.

#### Scenario: Limited evidence explained
- **WHEN** an installation was classified from a descriptor that examined four immediate entries
- **THEN** the explanation names the matched evidence, states that only four immediate entries were examined, and states that nested user data has not been ruled out

### Requirement: Model assessments can only add risk
A current model suggestion whose preservation-indicator assessment is `supported`, or whose native probability meets the policy's risk threshold, SHALL add preservation risk to the directory, even when its category is not applied. No model answer SHALL remove a trait, preservation risk, or protection; a directory with preservation risk or protection on or beneath it SHALL keep a triage other than `cleanup_candidate` whatever its category's source.

#### Scenario: Model sees user material in an apparent cache
- **WHEN** the rules say `cache`, and the model's preservation-indicator assessment has a native probability of 0.9 against a policy risk threshold of 0.7
- **THEN** the directory carries preservation risk citing the model suggestion, and its suggested triage is `review`, not `cleanup_candidate`
