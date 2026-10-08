# Spec Delta

## ADDED Requirements

### Requirement: Date corrections are owner decisions
A date correction SHALL be owner intent (I4): no scan, rule, or job SHALL set, change, or remove it. It SHALL survive rescans, moves, and a file's return after going missing, go only with its entry's index row, and, like an own decision, keep a missing entry's row from being reused at its path. Setting and clearing SHALL be audited as `date_correction_set` and `date_correction_cleared`, with targets, correction, and counts. Keep SHALL not limit them: they change no file.

#### Scenario: Corrections survive a rescan
- **WHEN** the owner shifts a camera's photos and the source is rescanned, one photo having been edited on disk
- **THEN** every photo keeps its correction, and the edited one's effective date applies the shift to its newly read capture date

#### Scenario: A missing corrected photo keeps its name
- **WHEN** a photo with a date correction goes missing and a date organize plans another file into its path
- **THEN** that item is a `conflict` with reason `name_taken_by_missing`, and the correction stays

#### Scenario: A correction is audited
- **WHEN** the owner sets a date on three photos
- **THEN** one `date_correction_set` event records the targets, the date, and three applied
