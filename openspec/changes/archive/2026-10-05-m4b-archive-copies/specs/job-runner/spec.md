# Spec Delta

## ADDED Requirements

### Requirement: A job holds one device at a time
A job that reads several sources SHALL hold a slot of at most one device at a time. It SHALL move to another source's device only between work units, by releasing its slot and then waiting for a slot of the other device as a waiting job of its class does. Moving between two sources on one device SHALL keep the slot.

#### Scenario: M4b-7 Inbox work on both devices during a cross-source search
- **WHEN** a copy search of `disk` and `old-disk`, which are on different devices with one worker each, reads `old-disk`, and a file arrives in an inbox on each device
- **THEN** the arrival on `disk` is enrolled and triaged without waiting for the search, and the arrival on `old-disk` is triaged before the search finishes

#### Scenario: Two sources on one device
- **WHEN** a copy search moves from `disk` to `photos`, which is on the same device
- **THEN** it keeps its slot and does not wait
