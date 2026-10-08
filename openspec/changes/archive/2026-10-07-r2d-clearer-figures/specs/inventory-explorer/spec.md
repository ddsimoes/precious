# Spec Delta

## ADDED Requirements

### Requirement: The duplicated share is named "Has copies"
The Map, Search, the treemap legend, and the detail panel SHALL name a folder's percent duplicated "Has copies". Wherever the figure is a column or a panel line, a hint SHALL say that it counts the files that also exist elsewhere, every copy included, and that the space that can be freed is on Opportunities (ADR 0010). The figure itself SHALL stay as the duplicates capability defines it.

#### Scenario: The root of a copied archive
- **WHEN** the owner opens the Map on a source whose root reads 49% (379 GiB) duplicated, while the duplicates card holds 125 GiB
- **THEN** the root's column reads "Has copies" with 49% (379 GiB), and its hint says this is not the space that can be freed and points to Opportunities

#### Scenario: No "Duplicated" label left
- **WHEN** every string of the English translation catalog is checked
- **THEN** none labels the duplicated share "Duplicated"
