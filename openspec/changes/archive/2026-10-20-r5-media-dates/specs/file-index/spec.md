# Spec Delta

## ADDED Requirements

### Requirement: Media metadata follows the file's identity
A rescan that finds a file's size, modification time, change time, or inode changed SHALL drop its media metadata in the transaction that updates its row, as it drops its digest, so the `media` job reads it again. A move by Precious SHALL keep it, and a `set_mtime` step SHALL carry it to the new times. Date corrections SHALL be kept in every case (I4).

#### Scenario: A photo edited on disk
- **WHEN** a read photo is edited in place and the source is rescanned
- **THEN** its metadata is gone in the same transaction, its date correction stays, and the next `media` job reads it again
