# Spec Delta

## ADDED Requirements

### Requirement: Owner overrides take precedence over rules
An entry's category and group flag SHALL come from the owner when the owner has set them, otherwise from the rules (§6.6: owner, then rule, then model). An owner category SHALL be one of the sixteen fixed categories. The family, triage suggestion, and veto SHALL follow from the effective category, exactly as for a rule category. A folder whose triage would be `discard` and whose subtree holds user-material indicators SHALL get `review` and `veto` true whoever set the category. Traits SHALL stay what the rules observe. Composition, notable entries, review lists, and Gems SHALL use the effective category and group flag.

#### Scenario: The owner corrects a folder's category
- **WHEN** the rules classify `Projetos/site_antigo` as `source_project` and the owner sets its category to `documents`
- **THEN** after the source's next scan, `site_antigo` reads category `documents`, family `personal`, triage `keep`, and `group` false, as a folder the rules classify `documents` does, so the folders above it count its files by their own families
- **AND** when the owner also marks it as a group, the folders above it count it whole as `personal`

#### Scenario: An owner discard category is still vetoed
- **WHEN** the owner sets the category of `Arquivos de programas/Microsoft Office` to `cache`, and its subtree holds `OFFICE11/Meu orcamento casamento.xls`
- **THEN** `Microsoft Office` reads category `cache` with triage `review` and `veto` true

#### Scenario: Back to the rules
- **WHEN** the owner returns an overridden folder's category to the rules
- **THEN** after the next scan its category is again what the rules give, and nothing of the override remains

## MODIFIED Requirements

### Requirement: Groups never hide their contents
A folder classified application_installation, os_installation, source_project, application_user_data, cache, generated_artifacts, or backup SHALL be marked as a group (`group` true), unless the owner unmarks it. The owner SHALL be able to mark any folder as a group, and to unmark a group the rules made (§6.5). Every entry inside a group SHALL still be indexed, listed by its parent, counted in totals, and found by search.

#### Scenario: An application is a group and stays browsable
- **WHEN** `Arquivos de programas/WinZip` is classified `application_installation`
- **THEN** it has `group` true
- **AND** `GET /api/entries/{id}/children` lists its files, and a search by one file's name finds it

#### Scenario: The owner marks a photo event as a group
- **WHEN** the owner marks `Fotos/2006/Praia` as a group
- **THEN** after the next scan it has `group` true, it counts whole under its family in the composition of every folder above it, and its photos are still listed and found by search

#### Scenario: The owner unmarks a rule group
- **WHEN** the owner unmarks the group `Arquivos de programas/WinZip`
- **THEN** after the next scan it has `group` false, and its parent's composition counts its files one by one

### Requirement: Every classification is explained
`GET /api/entries/{id}` SHALL return, under `classification.rules`, each rule that produced the entry's category or traits with its explanation sentence citing the names that matched. Explanations SHALL carry no coverage disclaimer, because the index is complete. When the owner set the category or the group flag, the classification SHALL say so and SHALL also give what the rules would set.

#### Scenario: Explanation cites the marker
- **WHEN** a folder is classified `source_project` because it holds `.git`
- **THEN** its classification lists that rule with an explanation naming `.git`
- **AND** no explanation says that part of the folder was not looked at

#### Scenario: An owner category is explained as the owner's
- **WHEN** the owner set the category of a folder that the rules classify `source_project` to `documents`
- **THEN** its classification gives category `documents` set by the owner, and `source_project` as what the rules would set, with the rule that matched
