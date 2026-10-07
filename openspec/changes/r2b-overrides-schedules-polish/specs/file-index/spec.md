# Spec Delta

## ADDED Requirements

### Requirement: Rescans follow an optional schedule
A source with a rescan schedule SHALL be scanned when its schedule comes due, as if the owner had asked for the scan (§7).
- **What follows the scan.** Hashing and folder relations SHALL follow a scheduled scan exactly as they follow any other scan.
- **Offline.** A source that is not `online` when its scan comes due SHALL be skipped until its next due time, and that skip SHALL be recorded on the source.
- **A scan already exists.** When a scan of the source is already queued, running, or paused, the due scan SHALL coalesce into it.
- **Server down.** When the server was down at a due time, it SHALL start that scan once after it starts, not once per missed time.
- **Owner intent.** A scheduled scan SHALL change no decision, tag, override, or group mark (I4).

#### Scenario: A daily scan runs at its time
- **WHEN** a source is scheduled daily at 03:00 and is online at 03:00
- **THEN** a scan of the source starts within a minute after 03:00, and hashing and relations follow it

#### Scenario: Offline at the due time
- **WHEN** a source is scheduled daily at 03:00 and its disk is unplugged at that time
- **THEN** no scan starts, the source shows that its 03:00 scan was skipped because it was offline, and its next scan is the next day at 03:00

#### Scenario: The server was down
- **WHEN** a source is scheduled daily at 03:00, and the server was stopped from 02:00 to 05:00 on two consecutive days
- **THEN** each time the server starts, one scan of the source starts, and the next scan is the next day at 03:00

#### Scenario: A due scan joins a running scan
- **WHEN** a source's scan comes due while the owner's own scan of that source is running
- **THEN** no second scan starts
