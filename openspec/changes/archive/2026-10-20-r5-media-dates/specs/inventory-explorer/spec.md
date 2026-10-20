# Spec Delta

## ADDED Requirements

### Requirement: Dates screen
The interface SHALL offer Dates in its main navigation, for the chosen source. It SHALL show the totals by source of date, confidence, metadata state, and flag, the time zone in use, the `media` job's live progress, the cameras with their suggested shifts, and a server-paged list of media filtered by flag, source of date, camera, and folder, with multi-select on the page, bulk corrections, "Set file dates…", and "Organize by date…", each previewed before it runs.

#### Scenario: R5.2 A camera's suggested shift in one confirmation
- **WHEN** the owner chooses "Shift" on the Sony's suggestion, reviews the 12 photos it lists, and confirms
- **THEN** the correction is applied to that camera's photos in its two event folders, and the Sony's photos show their new dates without a reload

#### Scenario: R5.4 Setting file dates from the screen
- **WHEN** the owner selects the flagged photos and chooses "Set file dates…"
- **THEN** a preview lists each photo with its old and new time and the refused ones with their reason, and nothing changes until the owner runs it

### Requirement: The detail panel shows a file's dates
For a photo or video, the detail panel SHALL show a Dates section: the effective date with its source, precision, and confidence, its flags or its metadata state when not read, every date found (capture, GPS, container, name, folder, modification), the camera, the correction, and controls to correct it. It SHALL be absent for quarantined entries and archive members.

#### Scenario: A photo with a disagreeing modification time
- **WHEN** the owner opens a photo copied years after it was taken
- **THEN** the Dates section shows its capture date as effective, with its source and confidence, the flag that the modification time disagrees, and both dates
