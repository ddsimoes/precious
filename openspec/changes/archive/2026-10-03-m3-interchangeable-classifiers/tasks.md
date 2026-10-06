# Tasks

## 1. Foundation: schema, shared signatures, and primitives (coordinator, on `master`)

- [x] 1.1 Write `migrations/0004_model_classification.sql` per design D22. Paths: `migrations/`, `internal/store/*_test.go`. Verify with store tests that:
  - existing rule rows read `applied = 1` after migrating an M2a-shaped database;
  - a second open `inference_requests` row for the same node and task is rejected;
  - invalid `result_status`, `outcome`, ledger `kind`, and `evidence_scope` values are rejected.
- [x] 1.2 Add `domain.Money`, `ParseMoney`, and `String`, plus `DirtyClassificationChanged` and the three new error codes with their HTTP statuses in `web/apierr` (D13, Interfaces). Paths: `internal/domain`, `internal/web/apierr`. Verify with table tests for exact 9-digit parsing, rejection of floats and exponents, and the status mapping.
- [x] 1.3 Add the classifier configuration (D8, Interfaces):
  - `config.Decimal`, `[classifier]`, `[[classifier_profiles]]`, and `[[classifier_policies]]`, with per-element defaults;
  - every validation rule of **Classifier configuration is validated**;
  - `Profile.Revision` and `ResolveSecrets`;
  - secret redaction and resolution status in `check-config`;
  - the docs-test extension: the `decimal` type, per-element defaults, and loading every `deploy/examples/*.toml`;
  - the three new example files and the configuration-reference rows.

  Paths: `internal/config`, `cmd/curator/checkconfig*.go`, `deploy/examples`, the `docs/operator.md` configuration reference. Verify with tests for **Metered profile without caps**, **Plain HTTP to a cloud endpoint**, **Task not supported by the profile**, **Secret reference redacted**, and **Missing secret** (an env var set and unset in the test; a secret file with mode 0644 rejected), plus `TestExampleConfig` and `TestOperatorDocsConfigReference`.
- [x] 1.4 Create `internal/classify/contract` with the Interfaces types, `Classifier` (with `Render`), `Error`, and `Validate` (D2), and `contract/contracttest.Run`, which replays recorded HTTP fixtures from an `httptest` server (D21). Paths: `internal/classify/contract/...`. Verify with `Validate` table tests for **A23 invalid distribution**, **A23 label outside the task**, **Evidence references stay within the descriptor**, non-finite numbers, and a label on a non-answered status, and with a contracttest self-test over a toy adapter.
- [x] 1.5 Create `internal/classify/tasks`: `policies/tasks/directory_category-v1.toml` and `file_kind-v1.toml`, `Load`, `Outbound` under `privacy-v1` with evidence renumbering, `OutboundFile`, and `CacheKey` (D3, D4, D14). Paths: `internal/classify/tasks`, `policies/tasks`, `policies/embed.go`. Verify with tests for:
  - **Absolute root and source label stay local**;
  - canonical bytes identical for two descriptors that differ only in listing order, entry sizes, IDs, and times;
  - `CacheKey` changing with each of task revision, deployment revision, prompt revision, privacy revision, profile revision, and source, and not with price.
- [x] 1.6 Create `internal/classify/providers/transport` (D7). Paths: that package. Verify with `httptest` tests for **Redirect not followed** (the `Authorization` header never reaches the second server), **Oversized response**, deadline → `timeout`, an ignored `HTTP_PROXY`, `RetryAfter` in seconds and date form, and `Backoff` never below `Retry-After` and capped at 5 min.
- [x] 1.7 Create `internal/classify/providers/registry` with the `disabled` and `fake` adapters (fixture rules per D23). Paths: `internal/classify/providers/{registry,disabled,fake}`. Verify with tests: `disabled` sends nothing and reports no task, the `fake` fixture outcomes (answered, abstained, refused, each error outcome, delay), and `registry.New` rejecting an unknown adapter.
- [x] 1.8 Add `jobs.Defer` and `Runner.RegisterPool` (D11). Paths: `internal/jobs`. Verify with runner tests for **Slow provider, running scan** (a pool job blocked in `Run` while a scan of the same device completes; pool capacity respected) and **Restart during backoff** (deferred state, `available_at`, and the unchanged attempt count survive a runner restart; no claim before the time).
- [x] 1.9 Extend `classify`: add `Suggestion.Model`, write the new columns in `Apply`, and change `Recompute` (D15). The precedence becomes owner, then a rule category other than `unknown`, then an applied model category, then `unknown`. Risk is OR-ed with `adds_risk`, and model rows add no traits. Paths: `internal/classify/apply.go`, its tests, `inventorytest` seeders for model rows. Verify with tests for:
  - **Applied model suggestion fills a rule unknown**, **Rule beats model**, **Model sees user material in an apparent cache**;
  - a current but not applied model row leaving the category `unknown`;
  - both A7 scenarios still passing.
- [x] 1.10 Create `internal/classify/{permission,requests,ledger,cache}` with the Interfaces signatures (D9, D10, D13, D14, D16). Paths: those packages. Verify with tests over a real store for:
  - `Want`'s eligibility table (**Only rule-unknown directories are sent** at unit level: matched, unknown, overridden, no grant, open row, current model row for the latest descriptor);
  - `CancelSource` cancelling open rows and requesting cancel on the source's classify jobs;
  - `permission.Set` revision conflicts;
  - `ledger.Reserve` refusing at the daily and job caps, and **Unknown usage stays reserved**;
  - `ledger.Bound` arithmetic, rounding up;
  - `cache` expiry for alias entries.
- [x] 1.11 Add the interfaces that later slices fill in: `commands.Deps.Classifier` and `explorer.Deps.Classifier`, with nil meaning "no classifier" (the existing behavior), and the `docs/operator.md` section skeletons with owner markers for slices A–F. Paths: `internal/commands/commands.go`, `internal/web/explorer/explorer.go`, `docs/operator.md`. Verify with the full race suite passing unchanged.
- [x] 1.12 Install a local Ollama with `qwen2.5:7b-instruct` as the reference structured-chat endpoint (D6). Record the Ollama and model versions in `~/.cache/curator-m3/state.md`. Verify with one `curl` to `/v1/chat/completions` in `json_schema` mode that returns schema-valid JSON.

## 2. Slice A: `typesafe` adapter

- [x] 2.1 Implement the `typesafe` adapter (D5): `Capabilities`, `Render`, `Classify`, the question mapping per task, the measures and assessments, usage, alias detection, the status mapping, and embedded prompt templates. Paths: `internal/classify/providers/typesafe`. Verify with unit tests for **Native choice becomes a domain category**, **A23 yes-probability kept as a probability**, and **Jev rate limit mapped by its adapter** (429 with `Retry-After`, 529, 401, 422).
- [x] 2.2 Record the contract fixtures. A `//go:build live` test reads `TYPESAFE_API_KEY` and sends at most 12 synthetic descriptors to `jev-1.13.0`, writing `testdata/contract/*.json` with `-update`. Hand-written fixtures for errors and invalid output are marked synthetic (D21). Paths: `internal/classify/providers/typesafe/testdata`, `live_test.go`. Verify that the live run's reported spend is below $0.01 and that the fixtures replay deterministically.
- [x] 2.3 Pass `contracttest.Run` over the recorded fixtures, and add a test for **A23 Jev invalid distribution** (a missing option and a sum of 1.4, both `invalid_response`). Paths: `internal/classify/providers/typesafe`. Verify with `go test -race`.
- [x] 2.4 Fill the Jev section of `docs/operator.md` (profile keys, pinned model versus alias, price schedule, and what is sent) and `deploy/examples/jev-opt-in.toml`. Verify with the docs and example tests.

## 3. Slice B: `structured_chat` adapter

- [x] 3.1 Implement the `structured_chat` adapter (D6): `json_schema` and `json_object` modes, a schema generated from the task, the system prompt, refusal, truncation, usage, the self-reported score, and the status mapping. Paths: `internal/classify/providers/structuredchat`. Verify with unit tests for **A22 label-only backend**, **A23 self-reported score keeps its kind**, **A23 refusal**, **A23 truncation**, and **A23 label outside the task**.
- [x] 3.2 Record the contract fixtures against the local Ollama from 1.12, with a `//go:build live` test that reads `CURATOR_LIVE_CHAT_ENDPOINT` and `CURATOR_LIVE_CHAT_MODEL` and writes the Ollama and model versions into the fixture header. Hand-written fixtures for 429, 503, malformed output, refusal, and truncation are marked synthetic. Paths: `internal/classify/providers/structuredchat/testdata`, `live_test.go`. Verify that the fixtures replay deterministically.
- [x] 3.3 Pass `contracttest.Run` over the same task fixtures as slice A (the shared descriptors in `contract/contracttest/descriptors`), so that both wire protocols pass one suite. Paths: `internal/classify/providers/structuredchat`. Verify with `go test -race`.
- [x] 3.4 Fill the local structured-output section of `docs/operator.md` (Ollama setup, modes, locality and trust, and why no probabilities appear) and `deploy/examples/local-structured-output.toml`. Verify with the docs and example tests.

## 4. Slice C: inference, routing, and automatic classification

- [x] 4.1 Implement `internal/classify/inference`:
  - the `KindClassify` handler with `PendingWork`;
  - the build, reserve, send, and apply loop, with the dispatch re-checks (Transaction boundaries);
  - D12 retries, fallback, and escalation;
  - D18 rate limits;
  - `Defer`;
  - D15 apply, with the hint and the stale re-`Want`.

  Paths: `internal/classify/inference`. Verify with tests using the `fake` adapter for:
  - **A6 rate limited, then answered** (3 attempts; none before `Retry-After`);
  - **A6 malformed every time**;
  - **Authentication failure is an operator error**;
  - escalation called once and only when configured;
  - **Model answer arrives after a new probe**.
- [x] 4.2 Add `Tick` (D10) and the discovery hook: `requests.Want` in `commitDecision` and `unchanged()` once the decision is final. Paths: `internal/classify/inference/tick.go`, `internal/discovery/decision.go` (hook calls only). Verify with discovery tests for **Only rule-unknown directories are sent** (one request among the three directories) and no request for an unchanged directory that already has a current model row.
- [x] 4.3 Add a test task for **A24 only a disallowed or over-budget fallback exists**: a fallback `httptest` server receives zero requests in both variants, the request ends visibly failed, and the directory keeps its rule classification and stays in the ambiguous queue. Paths: `internal/classify/inference`. Verify with `go test -race`.
- [x] 4.4 Add a test task for **A25 repeated polls hit the cache** and **A25 a relevant version change misses the cache**: repeated passes over an unchanged unknown directory send no request, then a deployment-revision, prompt-revision, or privacy-revision change sends a new one. Count outbound calls on the `httptest` server. Paths: `internal/classify/inference`. Verify with `go test -race`.
- [x] 4.5 Add a test task for **A43 permission revoked after enqueue** at the dispatch level: revocation between `Want` and the reserve transaction sends nothing; an attempt already in flight is counted as sent and its result is stale. Also test **A6 budget exhaustion**: the daily cap defers the rows to the next UTC midnight through `Defer`, while scans and pages keep working. Paths: `internal/classify/inference`. Verify with `go test -race`.
- [x] 4.6 Implement `reclassify-scope` (D17), with `Estimate`, `Enqueue`, and `Preview`. Paths: `internal/commands/reclassify_scope.go`, `internal/classify/inference/select.go`. Verify with command tests for **Dry run before a bulk rerun**, single-item versus list errors, the 1,000-ID limit, and `subtree` never selecting beneath an atomic unit.
- [x] 4.7 Add a test for **Applied container category expands the directory**: an applied `mixed` hints the scope, and the next scheduled pass expands the directory one level. Paths: `internal/classify/inference` or `internal/discovery` tests. Verify with `go test -race`.
- [x] 4.8 Fill the routing, budget, cache, automatic-classification, and `reclassify-scope` sections of `docs/operator.md`, with a doc-tested `reclassify-scope` example. Verify with the docs tests.

## 5. Slice D: per-source permission command

- [x] 5.1 Implement `set-classifier-policy` (D9, Endpoints): validation, the expected revision, `CancelSource` on revocation or narrowing, `already_sent`, and the audit event. Paths: `internal/commands/set_classifier_policy.go`. Verify with command tests for grant, revoke, narrowing, `revision_conflict`, and unknown policy or profile.
- [x] 5.2 Add a test task for **Off by default** and **A43 fallback to a new vendor**: with no grant no request is created, and a profile added to a policy receives nothing until the owner grants it for the source. Paths: `internal/commands`. Verify with `go test -race`.
- [x] 5.3 Add a test task for **A43 permission revoked after enqueue** at the command level: 20 queued rows are cancelled, the response reports `already_sent: 3` and the recall statement, and the audit event names the old and new grant. Paths: `internal/commands`. Verify with `go test -race`.
- [x] 5.4 Fill the permission section of `docs/operator.md`, with doc-tested `set-classifier-policy` examples for grant and revoke. Verify with the docs tests.

## 6. Slice E: classifier UI and read APIs

- [x] 6.1 Add `inventory` readers: usage per profile, task, and day; the latest operator error per profile; grants with `already_sent`; model fields on suggestions; evaluation summaries. Paths: `internal/inventory`, `inventorytest` seeders. Verify with reader tests over seeded rows, including **Usage by profile**.
- [x] 6.2 Add `GET /api/classifier-profiles`, `GET /api/classifier-usage`, and `GET /api/nodes/{id}/classifier-preview` (Endpoints). Paths: `internal/web/explorer`. Verify with API tests for **Profiles listed without secrets** (a set secret value appears in no response) and the preview through a fake `Deps.Classifier`.
- [x] 6.3 Build the `GET /classifier` page, the overview spend and permission lines, and the inspector's model rows with the accept and reclassify controls (D20), with the grant and revoke and reclassify handlers in `app.js`. Paths: `web/templates`, `web/static/app.js`, `internal/web/explorer`. Verify with page tests for **API spend on the overview**, **Model suggestion awaiting review**, **Payload preview before granting**, **Revocation explained on the page**, and **Latest evaluation shown**, and with the CSP unchanged.
- [x] 6.4 Fill the classifier-page and inspector sections of `docs/operator.md`. Verify with the docs tests.

## 7. Slice F: evaluation

- [x] 7.1 Implement `internal/eval`: the corpus loader, `Run` (D19: same ledger and caps, retries without fallback, sleeping on the context), the report with every metric, calibration bins, and `evaluation_runs`, and `Export`. Paths: `internal/eval`. Verify with tests for **Failures count against quality**, **Near-identical backups stay together**, **Cap reached during evaluation**, and **Override exported as an example** (mode 0600).
- [x] 7.2 Add `curator eval run` and `curator eval export`. Paths: `cmd/curator/eval.go`, `main.go`. Verify with CLI tests for exit codes and a written report.
- [x] 7.3 Write the synthetic seed corpus `deploy/eval/seed-corpus-v1` (about 60 examples, §13.2 coverage), with `fake` fixtures for a label-only profile and a probabilistic profile. Verify that the loader accepts it and that every label is in its task.
- [x] 7.4 Add a test task for **A42 label-only and probabilistic providers on one corpus**: both profiles get the same quality, cost, and latency figures, calibration appears only for the probabilistic one with its sample counts, and the report JSON has no cross-profile score field. Paths: `internal/eval`. Verify with `go test -race`.
- [x] 7.5 Fill the evaluation section of `docs/operator.md` (the corpus format, splits, the report, and export privacy). Verify with the docs tests.

## 8. Integration (coordinator)

- [x] 8.1 Wire `serve`:
  - `ResolveSecrets`;
  - the registry from `[[classifier_profiles]]`;
  - `inference.New`;
  - `RegisterPool(KindClassify, …, "classifier", workers)`;
  - a 5 s `Tick` ticker (injectable);
  - `commands.Deps.Classifier` and `explorer.Deps.Classifier`.

  Verify with a `cmd/curator` test in which a fake tick dispatches an automatic request to a `fake` profile.
- [x] 8.2 Add the scenario test **A21 switch from Jev to a structured-chat endpoint**. The stack classifies through a `typesafe` profile against an `httptest` replay of the recorded Jev fixtures, then restarts with the policy's primary switched to a `structured_chat` profile replaying the Ollama fixtures. Assert new provenance on new rows, the old rows unchanged, owner overrides intact, and the same binary and schema. Paths: `internal/scenario`. Verify with `go test -race`.
- [x] 8.3 Add the scenario test **A6** end to end: while the provider answers 429, 529, malformed output, or nothing, and while the budget is exhausted, a scan and a refresh complete, every page answers 200, no filesystem mutation is recorded, and retries stay within bounds. Paths: `internal/scenario`. Verify with `go test -race`.
- [x] 8.4 Add the scenario tests **A24** and **A43** end to end (grant, enqueue, revoke, a new-vendor fallback), and **A25** across scheduler passes. Paths: `internal/scenario`. Verify with `go test -race`.
- [x] 8.5 Add the scenario tests **A22** (a label-only answer applied by policy, and another left for review) and **A23** (an invalid distribution and a refusal: no suggestion, review, no zero). Paths: `internal/scenario`. Verify with `go test -race`.
- [x] 8.6 Finish `docs/operator.md`: remove the owner markers, add "Upgrading from M2a to M3", and update the intro and precedence prose that says no classifier exists. Verify with `grep -n 'owner:' docs/operator.md` returning nothing and the docs tests passing.
- [x] 8.7 Run the final verification:
  - `gofmt -l .` empty;
  - `go vet ./...` and `go vet -tags e2e,slow,live ./...`;
  - `go test -race ./...` with every package under 60 s;
  - `go test -race -tags slow ./...`;
  - the `e2e` mount tests in the privileged `golang:1.27.1` container;
  - a headless-browser pass over `/classifier`, the overview, and an inspector with model rows, plus the grant, revoke, and reclassify controls, under the production CSP.

  Verify that all are clean.
- [x] 8.8 Smoke-check the built binary with a throwaway script:
  - `check-config` on each example, with the secret redacted;
  - `serve` with a `typesafe` profile (`TYPESAFE_API_KEY`, pinned `jev-1.13.0`) and a `structured_chat` profile (the local Ollama), with `daily_cap = "0.10"`;
  - `start-scan` on a synthetic tree with rule-unknown directories;
  - grant, observe the automatic requests, the applied or review outcomes, and the usage and spend;
  - switch the policy's primary to Ollama and restart; `reclassify-scope` with a dry run;
  - revoke;
  - `curator eval run` on the seed corpus through both profiles;
  - `curator backup` passing its integrity check.

  Verify that the script reports success with total Jev spend under $0.10, then delete it.
