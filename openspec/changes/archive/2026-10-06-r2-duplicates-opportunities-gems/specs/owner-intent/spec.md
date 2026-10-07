# Spec Delta

## ADDED Requirements

### Requirement: A copy is decided like any other entry
A decision on one copy of a duplicate group or on one side of a relation SHALL change only that entry and its subtree, exactly as any `set-decision` does. It SHALL NOT change any other copy's decision, suggestion, or tags. No duplicate or relation SHALL ever set a decision, a suggestion, or a tag by itself (§6.7, I4, I5).

#### Scenario: R2.4 Deciding one copy changes nothing on the others
- **WHEN** the owner discards `Documentos/curriculo (1).doc` from its duplicate group, where `Documentos/curriculo.doc` carries the own tag `documento`
- **THEN** `curriculo (1).doc` reads discard, and every other copy keeps its decision, triage, and tags, `documento` included, and gains none

#### Scenario: R2.4 Deciding a side in Compare
- **WHEN** the owner keeps `Fotos` from Compare against `Fotos - Copia`
- **THEN** `Fotos` reads keep, and `Fotos - Copia` and its files keep their decisions and suggestions

#### Scenario: Hashing writes no decision
- **WHEN** hashing finds that every file of `Fotos - Copia` but one has a copy in `Fotos`
- **THEN** no entry's own decision, triage suggestion, or tag changes

### Requirement: Archive members are decided with their archive
An archive member SHALL have no decision or tags of its own; its effective decision SHALL be the archive's. A `set-decision` or `set-tags` that names a member SHALL fail with HTTP 400 `invalid_request` and change nothing (§6.4).

#### Scenario: A member follows its archive
- **WHEN** the owner discards `Downloads/fotos_2005_do_pendrive.zip`
- **THEN** each of its members reads effective decision discard, coming from the archive

#### Scenario: Deciding a member is refused
- **WHEN** a `set-decision` request names a member of a zip
- **THEN** the response is HTTP 400 `invalid_request`, and no decision changes
