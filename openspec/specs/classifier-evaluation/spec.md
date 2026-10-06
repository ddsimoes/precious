# classifier-evaluation Specification

## Purpose

Lets the owner compare classifier profiles on a frozen, owner-reviewed descriptor corpus, with honest quality, cost, and latency figures, before switching providers. Switching remains an owner decision.

## Requirements

### Requirement: Evaluation report
`curator eval run` SHALL classify every example in a corpus's evaluation split through each selected profile. For each profile and task it SHALL report:
- per-category precision and recall, and a confusion matrix;
- abstention, refusal, and review rates, and response validity;
- preservation-risk false negatives;
- the latency distribution, including retries and failures;
- the estimated and observed cost per 1,000 completed decisions.

It SHALL record the corpus, task, prompt, privacy, profile and deployment, price, and adapter revisions.

#### Scenario: A42 label-only and probabilistic providers on one corpus
- **WHEN** one corpus is evaluated through a label-only profile and a profile with native probabilities
- **THEN** both get the same quality, cost, and latency figures, calibration appears only for the probabilistic profile with its sample counts, and no figure combines, compares, or ranks score values across the two profiles

#### Scenario: Failures count against quality
- **WHEN** 5 of 100 examples end `rate_limited` after their retries
- **THEN** they count as not completed in the validity rate and the latency distribution, and precision and recall state that they cover 95 decisions

### Requirement: Tuning and evaluation splits by source family
Each example SHALL belong to a source family and a split. Only the evaluation split SHALL be scored. All examples of one family SHALL be in the same split.

#### Scenario: Near-identical backups stay together
- **WHEN** a corpus holds two copies of the same backup under one family, and the family is assigned to the tuning split
- **THEN** neither copy is scored, and the report counts the excluded tuning examples

### Requirement: Evaluation respects budgets and privacy
An evaluation SHALL reserve and record spending like any classification, under the same caps, and SHALL stop before a reservation would exceed a cap. It SHALL send only the privacy-transformed descriptor. Automated tests SHALL use saved or fake responses only.

#### Scenario: Cap reached during evaluation
- **WHEN** the daily cap would be exceeded after 60 of 100 examples
- **THEN** the run stops before the 61st request, and the report is marked incomplete with the counts reached

### Requirement: Corrections become labeled examples
`curator eval export` SHALL write each owner category override, together with its directory's latest descriptor, as an owner-reviewed labeled example. Each example SHALL have a source family derived from its source and top-level ancestor, and a deterministic split per family. The export SHALL be a local file readable only by its owner. Nothing SHALL be sent anywhere.

#### Scenario: Override exported as an example
- **WHEN** the owner has overridden 12 directories
- **THEN** the export holds 12 examples labeled with the owner's categories, with their descriptors, families, and splits, in a file with mode 0600

### Requirement: Evaluation summaries are visible
Each evaluation run SHALL record a summary that the classifier settings page shows with its revisions and date. Summaries of different profiles SHALL be shown side by side without a combined ranking.

#### Scenario: Latest evaluation shown
- **WHEN** an evaluation run has finished
- **THEN** the settings page lists its profiles, corpus revision, date, and per-profile figures
