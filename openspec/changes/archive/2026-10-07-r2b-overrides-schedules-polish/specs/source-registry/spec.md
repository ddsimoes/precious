# Spec Delta

## ADDED Requirements

### Requirement: A source has an optional rescan schedule
- **Setting the schedule.** `set-source-schedule` SHALL set a source's rescan schedule. A schedule is off, daily at a time of day, or weekly on a day of the week at a time of day, in a named time zone. The UI sends the browser's zone.
- **What a source reports.** Each source in `GET /api/sources` SHALL carry:
  - its schedule;
  - the time of its next scheduled scan;
  - the outcome of the last time it came due, when that time was skipped.
- **Refused requests.**
  - An unknown source SHALL fail with 404 `unknown_source`.
  - A malformed schedule (a bad time, day, or zone) SHALL fail with 400 `invalid_request`.
- **Audit.** Each accepted request SHALL write one `source_schedule_set` audit event with the old and new schedules.
- **The Sources screen** SHALL show each source's schedule and next scan, and SHALL let the owner change the schedule.

#### Scenario: Scheduling a source
- **WHEN** the owner schedules the source `fotos` weekly on Sunday at 03:00 in `America/Sao_Paulo`
- **THEN** the response is 200 with the source, its schedule, and its next scan at the coming Sunday 03:00 in that zone, and a `source_schedule_set` audit event is written

#### Scenario: Turning a schedule off
- **WHEN** the owner sets the schedule of a scheduled source to off
- **THEN** the source has no next scheduled scan, and no scan starts by schedule

#### Scenario: A bad time is refused
- **WHEN** a `set-source-schedule` request names the time `25:00`
- **THEN** the response is 400 `invalid_request`, and the schedule is unchanged
