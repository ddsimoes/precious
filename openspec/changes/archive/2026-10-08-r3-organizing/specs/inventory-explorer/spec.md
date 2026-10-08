# Spec Delta

## ADDED Requirements

### Requirement: The children listing can be limited to folders
`GET /api/entries/{id}/children` SHALL accept `kind=directory`. With it, only the folder's child folders are listed, with the usual sorts, pages, and cursors, and archives are left out. The destination chooser of a move SHALL browse the index this way, from the top folder of the source being organized, and SHALL never send a typed path.

#### Scenario: Folders only
- **WHEN** a client lists the children of the corpus root with `kind=directory&sort=name`
- **THEN** every row is a folder, every folder child of the root is listed in name order, and files and archives are not listed
