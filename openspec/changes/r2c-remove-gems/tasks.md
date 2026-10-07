# Tasks

## 1. Decision record

- [x] 1.1 Write `docs/adr/0009-remove-gems.md`: what it removes, what amends §11.7, R2.4, and R2.6, and what stays. Also update the delivery order line in `openspec/config.yaml`'s context, and the Purpose of `openspec/specs/review-lists/spec.md`, which still promises "what is valuable?". Verify with `openspec validate --specs --strict`.

## 2. Storage

- [x] 2.1 Write `migrations/0005_rescue.sql` (design D1). Verify with an upgrade test on a v4 database that holds duplicates rows with source rows, `gems_rescue` rows, and Gems rows. Check:
  - the rescue rows read `rescue`;
  - the Gems rows are gone;
  - the duplicates rows keep their source rows;
  - `PRAGMA foreign_key_check` is empty;
  - the seven indexes exist;
  - the `CHECK` rejects `gems_unique`;
  - the version is 5.

  Add the 0005 upgrade notes to `docs/operator.md`, and verify with the docs tests.

## 3. Review lists

- [x] 3.1 In `internal/review`:
  - add `ListRescue` and remove the Gems lists, their refresh code, and the ascending paging;
  - rename `rescueGems` to `rescueRows`, with `sort_key` the file's bytes (D4);
  - add the open rule of D2;
  - add the card order of D3.

  Verify with tests:
  - the rule's five cases, the inherited discard among them;
  - the order, with and without open rescue rows;
  - R2.5 extended to the rescue card;
  - `review_slow_test.go` updated and compiling under `-tags slow`.
- [x] 3.2 In `internal/web/api`:
  - remove `GET /api/gems`;
  - add `group` to review rows, and the rescue card to `GET /api/opportunities`.

  Verify with API tests:
  - **R2.6:** the rescue list holds `OFFICE11/Meu orcamento casamento.xls` with group `Microsoft Office`;
  - the inherited-discard scenario;
  - `GET /api/gems` is 404;
  - the updated R2 and `cmd/precious` serve tests.
- [x] 3.3 In `internal/corpus` and `tools/gencorpus`, replace the Gems truth with `rescue` (D6) and remove the declarations only Gems used. Verify with `go test ./internal/corpus ./tools/gencorpus`.
- [x] 3.4 Update the Opportunities and Gems sections of `docs/operator.md`, the API table, and the README features. Describe the rescue card, and where "no other copy" now lives (the panel and Search). Verify with the docs tests.

## 4. Interface

- [x] 4.1 Remove from `web/ui/src`:
  - the `gems/` module, `api/gems.ts`, the route, and the menu entry;
  - the `gems` query root, the i18n keys, and the Gems mentions in events, the source choice, and bulk selection.

  Verify with `npm run -s lint` and a Vitest test that the main menu has no Gems.
- [x] 4.2 Add the rescue card:
  - its title and description;
  - its count headline;
  - its place first while open;
  - its rows naming "inside <group>" with a link to the group on the Map.

  Verify with Vitest (card order and headline, row group link, select-all present).

## 5. Integration

- [x] 5.1 Update the Playwright suite:
  - **R2.6** becomes the rescue card test, including the inherited-discard scenario;
  - `layout.spec.ts` drops Gems;
  - `env.ts` follows D6.

  Verify with `npx playwright test` passing headless, with no console error.
- [ ] 5.2 Run the full verification:
  - gofmt and both vet runs;
  - `go test -race ./...`;
  - `make test-slow`;
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --strict`.

  Verify that every step passes.
- [ ] 5.3 Smoke-check the built binary on a copy of an r2b (v4) database with a throwaway script:
  - the 0005 upgrade;
  - Opportunities with the rescue card first;
  - `GET /api/gems` is 404;
  - a backup with integrity checked.

  Verify that it reports success, then delete the script.
- [ ] 5.4 Back up the reference server's database, deploy, and have the owner look at Opportunities on his dataset. Verify with his sign-off, recorded in the design addendum.
