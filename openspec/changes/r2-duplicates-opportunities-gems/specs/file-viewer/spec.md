# Spec Delta

## ADDED Requirements

### Requirement: Archive members open without extraction
The content and text of a member of a completely read archive SHALL be served under the same type table and safety headers as a file, read from the archive in memory. Nothing SHALL be written to disk, not even a temporary file. When the archive's file no longer matches its indexed row, the response SHALL be 409 `invalid_entry_state` (§11.12, ADR 0007).

#### Scenario: R2.8 A photo inside the corpus's zip
- **WHEN** the owner opens a photo inside `Downloads/fotos_2005_do_pendrive.zip` in the viewer
- **THEN** the photo is shown with the type `image/jpeg` and the sandbox CSP
- **AND** the state directory, the temporary directory, and the source hold exactly the same files before and after

#### Scenario: A member of a changed archive
- **WHEN** the zip was modified on disk after it was listed, and the owner opens one of its members before a rescan
- **THEN** the response is 409 `invalid_entry_state`

#### Scenario: HTML inside an archive never runs
- **WHEN** the owner opens an `.html` member of a zip
- **THEN** it is offered as a download with `Content-Disposition: attachment`, and no script runs in the application's origin

#### Scenario: Seeking in a stored member
- **WHEN** a video is stored uncompressed inside a zip and the browser requests a byte range of it
- **THEN** the response is 206 with that range
