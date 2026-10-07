# Spec Delta

## MODIFIED Requirements

### Requirement: Uniqueness claims state the checked share
Every "no other copy" or "duplicate" claim SHALL be shown together with the share of the content that could have a copy that was checked, and a file not yet checked SHALL be shown as not checked, never as unique (I7).

#### Scenario: R2.7 No other copy names the checked share
- **WHEN** hashing has checked 80% of the bytes that could have a copy, and the owner opens a photo whose size no other file shares
- **THEN** the panel says it has no other copy, and that 80% of the content that could have a copy was checked

#### Scenario: Not checked is not unique
- **WHEN** a photo's size is shared and the photo has not been hashed yet
- **THEN** it is shown as not checked yet, and a search for files with no other copy does not list it
