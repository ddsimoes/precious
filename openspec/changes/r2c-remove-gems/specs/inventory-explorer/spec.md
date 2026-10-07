# Spec Delta

## RENAMED Requirements

- FROM: `### Requirement: Opportunities, Gems, and Compare screens`
- TO: `### Requirement: Opportunities and Compare screens`

## MODIFIED Requirements

### Requirement: Opportunities and Compare screens
The interface SHALL offer Opportunities in its main navigation, and SHALL NOT offer Gems. Each opportunity card SHALL open its review list. Compare SHALL open from a relation in the detail panel or in a review list, and from a folder's detail panel by choosing a second folder or archive, with the two sides named in the address so it can be bookmarked (§11.4, §11.6).

#### Scenario: From a card to its list
- **WHEN** the owner chooses the system junk card on Home
- **THEN** the system junk review list opens

#### Scenario: Compare two chosen folders
- **WHEN** the owner chooses "Compare with…" on `Fotos`, then opens `Fotos - Copia` and chooses to compare it with `Fotos`
- **THEN** Compare opens with `Fotos` on the left and `Fotos - Copia` on the right, and reloading the page shows the same comparison

#### Scenario: Deciding from Compare
- **WHEN** the owner sets discard on a file listed only on the right in Compare
- **THEN** the file's decision is discard, and the list shows it

#### Scenario: No Gems
- **WHEN** the owner looks at the main navigation
- **THEN** it has no Gems entry

### Requirement: Read-only workflow without models
The complete R2 workflow SHALL work with every source mounted read-only and no network egress: logging in, adding a source, scanning, hashing, following progress, Home, Map, Search, the detail panel, the viewer, decisions, tags, Opportunities and review lists, and Compare.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and the server has no network egress
- **THEN** the owner can log in, add the source, scan and hash it, follow its progress, use Home, Map, Search, Opportunities, and Compare, open the detail panel and the viewer, and set decisions and tags, and no write to the source or outbound request is attempted

### Requirement: Screens fit the window
At a window of 1366×768 or larger, every screen SHALL keep its content inside its own area. No table column, header, or control SHALL extend past its card or the window, and no two controls SHALL overlap. A table too wide for its card SHALL drop its lowest-priority columns or scroll inside the card, with header and rows aligned. Opening the detail panel SHALL NOT squeeze the Map table below a usable width.

#### Scenario: Map with the detail panel at 1366×768
- **WHEN** the owner opens a folder's details on the Map in a 1366×768 window
- **THEN** the table's name, size, and category columns stay visible inside the table's card, and nothing spills past the window

#### Scenario: Search filters
- **WHEN** the owner opens Search in a 1366×768 window
- **THEN** no filter control overlaps another or its label

#### Scenario: New screens at 1366×768
- **WHEN** the owner opens Opportunities, a review list, and Compare in a 1366×768 window
- **THEN** nothing spills past its card or the window, and no two controls overlap

### Requirement: The chosen source is remembered
The source the owner last chose on Home, Opportunities, Search, or the Map SHALL be used by those screens, and by the Map's starting folder, until the owner chooses another source or all sources. It SHALL be remembered per browser.

#### Scenario: Home's choice carries to the Map
- **WHEN** the owner chooses the source `archive` on Home and then opens the Map from the main menu
- **THEN** the Map starts on `archive`, and Opportunities shows `archive` too

### Requirement: Index read API
The server SHALL provide `GET /api/sources`, `GET /api/picker`, `GET /api/home`, `GET /api/entries/{id}`, `GET /api/entries/{id}/children`, `GET /api/entries/{id}/treemap`, `GET /api/search`, `GET /api/tags`, `GET /api/entries/{id}/content`, `GET /api/entries/{id}/text`, `GET /api/opportunities`, `GET /api/opportunities/{card}`, `GET /api/compare`, `GET /api/jobs/{id}`, and `GET /api/events`. Entry names and paths SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

#### Scenario: A17 lossless name in API
- **WHEN** a client fetches an entry whose name contains invalid UTF-8 bytes
- **THEN** the response carries the exact raw bytes of its name and path in base64, and an unambiguous escaped display form of each

#### Scenario: Unknown entry
- **WHEN** a client requests an entry ID that does not exist, or one that is not a valid ID
- **THEN** the response is HTTP 404 with code `not_found`

#### Scenario: Bad query value
- **WHEN** a client requests children with `sort=color` or with a cursor it did not receive from the server
- **THEN** the response is HTTP 400 with code `invalid_request`

#### Scenario: Entry carries classification and intent fields
- **WHEN** a client fetches `GET /api/entries/{id}` for a folder
- **THEN** the response carries its ancestors, its category, family, traits, triage, group and veto flags, the matched rules with their explanations and any veto indicators, its own and effective decision with the entry it comes from, its own and inherited tags with their origins, and its breakdowns by kind and by year

#### Scenario: Entry carries content fields
- **WHEN** a client fetches `GET /api/entries/{id}` for a hashed file with two other copies
- **THEN** the response carries its content state, its SHA-256, the two copies with their paths and decisions, and the checked share of the content that could have a copy

#### Scenario: Unknown card
- **WHEN** a client requests `GET /api/opportunities/colors`
- **THEN** the response is HTTP 404 with code `not_found`

#### Scenario: Gems is gone
- **WHEN** a client requests `GET /api/gems?section=unique`
- **THEN** the response is HTTP 404
