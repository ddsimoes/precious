# Spec Delta

## MODIFIED Requirements

### Requirement: Owner overrides take precedence over rules
An entry's category and group flag SHALL come from the owner when the owner has set them, otherwise from the rules (§6.6: owner, then rule, then model). An owner category SHALL be one of the sixteen fixed categories. The family, triage suggestion, and veto SHALL follow from the effective category, exactly as for a rule category. A folder whose triage would be `discard` and whose subtree holds user-material indicators SHALL get `review` and `veto` true whoever set the category. Traits SHALL stay what the rules observe. Composition, notable entries, and review lists SHALL use the effective category and group flag.

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
