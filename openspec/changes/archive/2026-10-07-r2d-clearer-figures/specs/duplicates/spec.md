# Spec Delta

## ADDED Requirements

### Requirement: Compare alone counts what is only on one side
Files found only on one side of a relation SHALL be counted on screen by Compare alone, in its buckets: only on the left, only on the right, and same name with different content. The detail panel's relations SHALL show each relation's kind and the bytes in common, with a link that opens Compare on the two, and SHALL NOT show a count of files only on either side. The relation SHALL still record those counts (§6.4), and the read API SHALL still serve them (ADR 0010).

#### Scenario: A folder with a similar copy
- **WHEN** the owner selects `Fotos - Copia` after hashing
- **THEN** the panel shows its relation to `Fotos`, with the bytes in common and a link that opens Compare on the two, and shows no count of files only on either side
- **AND** Compare on the two counts the files only on each side, `2006/Praia/DSC_editada.JPG` among those only on the right
