# Spec Delta

## ADDED Requirements

### Requirement: Archives that were not opened say so
The detail panel of an archive file SHALL say when its members were not read, and why:
- its format is one Precious does not open, such as 7z or rar;
- it is an archive inside an archive;
- it was not listed yet.

It SHALL also say that its content is not checked for copies. For an archive that was listed completely, opening it as a folder SHALL be the main action.

#### Scenario: A 7z file
- **WHEN** the owner selects `backup.7z`
- **THEN** the panel says that 7z archives are not opened, so what is inside is not checked for copies

#### Scenario: A listed zip
- **WHEN** the owner selects `Downloads/eMule0.47c-Installer.zip`, which was listed completely
- **THEN** the panel's main action opens it as a folder in the Map
