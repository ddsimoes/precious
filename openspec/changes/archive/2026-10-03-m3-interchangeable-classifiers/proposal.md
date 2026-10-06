# Proposal

## Why

After M2a, every category comes from deterministic rules or the owner. A directory the rules cannot place stays `unknown`, and nothing can ask a model about it. `model` already exists as a suggestion kind, but nothing produces one. This change is milestone **M3, interchangeable classifiers** (§14, §7). It adds a provider-neutral classifier boundary with native Jev and structured-chat adapters, routing, privacy, budgets, a semantic cache, and an evaluation harness, so that **A6, A21, A22, A23, A24, A25, A42, and A43** pass and switching providers needs configuration only. All earlier acceptance scenarios keep passing, and the application stays fully usable with no classifier configured.

## What Changes

- **Classifier contract (§7.1–§7.3, A22, A23):**
  - Domain-level `Classifier`, request, result, assessment, typed-measure, provenance, and usage types that never mention a vendor.
  - Versioned task definitions for `directory_category` and `file_kind`.
  - A canonical semantic outbound descriptor under a versioned privacy transform.
- **Adapters (§7.3, §7.4):**
  - `disabled`; `fake` (deterministic fixtures);
  - `typesafe`, which maps native Jev Choice and Noul answers to domain labels and typed measures;
  - `structured_chat`, an OpenAI-compatible chat endpoint in JSON-schema or JSON-only mode with strict local validation.
  - Both wire protocols pass one shared contract suite over fixtures recorded from Jev and from a local Ollama.
- **Routing and privacy (§7.5, §7.7, A24, A43):**
  - The operator configures named provider profiles and routing policies.
  - The owner grants each source a policy and its permitted profiles, after seeing the exact outbound payload, and can revoke it.
  - Eligibility is checked when work is enqueued and again before every send.
  - Fallback is bounded, explicit, and never reaches a profile the source does not permit. Escalation goes to at most one profile and is off by default.
- **Budgets and usage (§7.7, A6):**
  - Fixed-point price schedules; a conservative reservation before each attempt; per-job and daily caps.
  - Known, estimated, and unknown usage are kept apart.
  - Bounded retries with backoff for transient provider errors. Authentication and configuration failures are shown as operator errors.
- **Semantic cache (§7.6, A25):** results are keyed by semantic evidence and by every relevant revision. A repeated poll reuses the cached result; a model, prompt, privacy, or task change does not.
- **Applying results (§5.3, §7.6, A21, A22):**
  - Model suggestions are appended with provenance and typed measures, and bound to the epoch, revisions, dirty version, and descriptor they observed.
  - A model suggestion becomes effective only below owner overrides and rules, and only where the routing policy's evaluated per-category policy allows it. Anything else goes to review.
  - Directories that the rules leave `unknown` are classified automatically in permitted sources. `reclassify-scope` covers explicit selections and gives a dry-run estimate first.
- **Evaluation (§7.8, §13.2, A42):**
  - `curator eval run` scores profiles on a frozen descriptor corpus. It reports quality, abstention, validity, latency, and cost per 1,000 decisions, and never compares score scales across profiles.
  - `curator eval export` turns owner corrections into owner-reviewed labeled examples.
- **UI and API (§9.1, §9.3):**
  - A classifier settings page (profiles, policies, permissions with payload preview, spend, evaluation summaries) and API spend on the overview.
  - Model provenance and a reclassify control in the inspector.
  - New endpoints: `GET /api/classifier-profiles`, `GET /api/classifier-usage`, `POST /api/commands/set-classifier-policy`, and `POST /api/commands/reclassify-scope`.
- **BREAKING (internal):** migration `0004`. Classifier jobs run in their own worker pool instead of a filesystem device slot.

Deferred, with owning milestone:
- Owner-written scoped rules (§5.3 "applicable user rule") and creating a rule from a correction → **M3a**, next to its routing rules.
- Automatic model classification of loose inventory files, and the inbox fast-triage policy → **M3a**. The `file_kind` task already ships in the adapters and the evaluation harness.
- Priority classes and fairness between interactive, reconciliation, and bulk work (§8.4) → **M3a**, as deferred by M2a.
- Content, image, and document-excerpt evidence scopes (§7.7) are outside the first release's model input. Shadow sampling (§7.8) is optional and not scheduled. Raw request/response diagnostic retention is not built; only normalized evidence is stored.

## Capabilities

### New Capabilities
- `classifier-providers`: the provider-neutral contract, the four adapters, capability claims, typed measures, output validation, and explicit transport outcomes.
- `classifier-routing`: profiles and routing policies, per-source permission, dispatch eligibility, fallback and escalation, budgets and usage, the semantic cache, automatic and owner-requested classification, and how results are applied.
- `classifier-evaluation`: the evaluation command, its report, and corrections exported as labeled examples.

### Modified Capabilities
- `directory-classification`: a model suggestion becomes effective only where the routing policy allows it; it carries provenance and typed measures; late model results are also bound to the dirty version and the descriptor they used.
- `job-runner`: classifier work never holds a filesystem worker slot; transient provider failures defer a job to a later time instead of failing it.
- `server-config`: classifier sections, secret references by environment variable or file, and secret redaction in `check-config`.
- `inventory-explorer`: classifier settings page, API spend, model provenance in the inspector, and the new read endpoints.

## Impact

- **New packages:**
  - `internal/classify/contract` and `internal/classify/tasks`;
  - `internal/classify/providers/{registry,transport,disabled,fake,typesafe,structuredchat}`;
  - `internal/classify/inference` (router, ledger, cache, job handler);
  - `internal/classify/permission`;
  - `internal/eval`.
- **Changed packages:** `classify`, `discovery` (eligibility hook), `jobs` (worker pools, deferral), `config`, `domain`, `commands`, `inventory`, `web/explorer`, and `cmd/curator` (wiring, `eval` subcommand).
- **Configuration:** new `[classifier]`, `[[classifier_profiles]]`, and `[[classifier_policies]]` sections, and new example files.
- **Database:** migration `0004`.
- **Docs:** `docs/operator.md`.
- **Tests:** a `live` build tag for manually run fixture recording against Jev and a local Ollama. CI uses saved responses only.
- **Dependencies:** none new; outbound HTTP uses `net/http`.
- **External:** Jev is used through the operator's API key with a hard spend cap. A local Ollama with a small instruct model is the structured-chat reference endpoint.
