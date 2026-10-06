# inventory-accounting Specification

## Purpose

Computes inventory totals from the non-overlapping active frontier. Expansion, collapse, measurement, and overlapping selections therefore never double count, and unknown, partial, and stale sizes stay visibly separate from measured bytes.

## Requirements

### Requirement: Totals use the non-overlapping frontier
Totals for a source or an expanded directory SHALL be summed over the active frontier beneath it. Loose files and symlinks count by their own record, and atomic directories count once, at their boundary. An expanded directory SHALL contribute only through its descendants. A measurement of a directory SHALL never be added to the contributions of that directory's descendants.

#### Scenario: Measured directory later refined
- **WHEN** atomic `Backup`, measured complete at 10 GiB, is refined into its children
- **THEN** totals count `Backup`'s children, and `Backup`'s 10 GiB measurement contributes nothing while it is expanded

### Requirement: Size classes stay separate
Totals SHALL report separately:
- measured bytes: regular files plus complete, current measurements;
- lower-bound bytes: partial, current measurements;
- stale bytes;
- the count of units of unknown size.

A single figure SHALL be presented as a scope's size only when the lower-bound, stale, and unknown parts are all zero.

#### Scenario: Mixed size classes
- **WHEN** a source holds files of 2 GiB, one unit measured complete at 5 GiB, one partial measurement of 1 GiB, one stale measurement of 3 GiB, and four unmeasured atomic directories
- **THEN** totals show 7 GiB measured, at least 1 GiB more as a lower bound, 3 GiB stale, and 4 units of unknown size, and no single total as the source's size

### Requirement: Totals stable across expansion and collapse
Expanding and later collapsing a directory SHALL leave the totals of every enclosing scope as they were before the expansion, as long as no observation or measurement changed in between. No intermediate state SHALL count a directory and its descendants together.

#### Scenario: A9 totals after expansion and collapse
- **WHEN** a measured atomic unit is refined and then collapsed, with no rescan in between
- **THEN** the source totals after the collapse equal the totals before the refine, and the totals taken while it was expanded never include both the unit's measurement and its children

### Requirement: Overlapping selections are normalized
A selection summary SHALL remove duplicate node IDs, report every selected node that has a selected ancestor as covered by that ancestor, and sum totals over the remaining nodes once. A bulk command SHALL apply its change at most once per distinct listed node, however often the node is listed.

#### Scenario: A9 overlapping selection
- **WHEN** a selection contains expanded `Backup` and its child `Backup/Photos`
- **THEN** the summary lists `Backup/Photos` as covered by `Backup`, and `Backup/Photos`'s bytes are counted once

#### Scenario: Duplicate IDs in a bulk command
- **WHEN** a bulk command lists the same node twice
- **THEN** the change is applied once, and the node's intent revision increases by one
