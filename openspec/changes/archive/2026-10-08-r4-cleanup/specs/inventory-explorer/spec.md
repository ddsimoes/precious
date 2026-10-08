# Spec Delta

## ADDED Requirements

### Requirement: Quarantine is left out of every view
Quarantined entries SHALL appear only on the Cleanup screen and in their own detail panels. The following SHALL leave the quarantine folder and everything below it out:
- the Map's table and treemap;
- Search results, counts, and selections;
- the source and Home totals and breakdowns;
- decision totals;
- copy counts;
- review lists and cards.

Home's decision progress SHALL show the bytes in quarantine beside kept, discarded, later, and undecided (§11.1). A quarantined entry's detail panel SHALL say it is in quarantine, show no decision, tag, or organizing controls, and link to the Cleanup screen.

#### Scenario: Home after quarantining a folder
- **WHEN** a discarded 50 MB folder is quarantined
- **THEN** Home's totals drop by 50 MB, its decision progress shows 50 MB in quarantine and 50 MB less discarded, and neither the Map nor Search lists the folder
