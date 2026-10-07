# Tasks

## 1. Decision record

- [x] 1.1 Write `docs/adr/0010-clearer-figures.md`, recording the owner's three decisions of 2026-10-07 and what each amends: §11.4 (the installers card), §11.2 (the label of the duplicated share), and §11.6 and §11.8 (where files only on one side are counted). Verify with `openspec validate --strict`.

## 2. Installers card

- [x] 2.1 In `internal/review`, remove `download_collection` from the installers card (design D1), and rename the card in `en.ts` and `docs/operator.md`. Verify with a review test and an API test of the scenario "A downloads folder is not an installer": `Downloads/Setup.exe` and the disk images are rows, and `Downloads` is not. R2.5 still holds over the card, and the docs tests pass.

## 3. Labels and counts

- [x] 3.1 Rename the duplicated share to "Has copies" in the Map, Search, the treemap's color mode and legend, and the detail panel, and add the hint (design D2). Verify with:
  - Vitest: the column header and the panel line read "Has copies", with the hint;
  - a catalog test that no string labels the share "Duplicated";
  - the docs tests, after the Map section of `docs/operator.md` is updated.
- [x] 3.2 Remove the "only here / only there" figures from the panel's relations and the Similar folders rows (design D3). Verify with:
  - Vitest: a relation row and a Similar folders row show the bytes in common and the Compare link, and no only-side count;
  - the docs tests, after the Compare, Similar folders, and detail panel sections of `docs/operator.md` say that Compare is where those files are counted.

## 4. Integration

- [x] 4.1 Update the Playwright tests that read the removed figures (Similar folders) and the installers rows. Verify with `npx playwright test` passing headless.
- [x] 4.2 Run the full verification:
  - gofmt and both vet runs;
  - `go test -race ./...`;
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --strict`.

  `make test-slow` is not needed: no query on a slow path changes.
- [x] 4.3 Smoke-check the built binary with a throwaway script on a copy of an r2c database. After a scan, verify:
  - the installers card holds file rows only;
  - the relation JSON still carries `only_here`.

  Then delete the script.
- [ ] 4.4 Deploy on the reference server and have the owner look at the installers card, the Map's "Has copies", and a folder's relations. Verify with his sign-off, recorded in the design addendum.
