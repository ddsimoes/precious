# Spec Delta

## ADDED Requirements

### Requirement: Compare shows where each copy is
Each Compare item SHALL show the path of its file on each side, inside that side, whenever the two differ. An identical item with a file on one side only SHALL say that it is an extra copy of content found on both sides, and SHALL name the file on the other side that holds that content.

#### Scenario: Copies filed under different folders
- **WHEN** the owner compares `fotos-b` with `fotos`, and `fotos-b/2002/12/img_0001.jpg` is identical to `fotos/2014/celular/IMG_0001.jpg`
- **THEN** the item shows `2002/12/img_0001.jpg` on the left and `2014/celular/IMG_0001.jpg` on the right

#### Scenario: An extra copy on one side
- **WHEN** the right side holds two copies of a photo that the left side holds once
- **THEN** the identical group lists the pair, and lists the second right copy as an extra copy of the same content, naming the left file
