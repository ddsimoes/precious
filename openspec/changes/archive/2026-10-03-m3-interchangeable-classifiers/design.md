# Design

## Context

See proposal.md for the motivation and the specs for the required behavior. These code facts on `master` at `3c2193d` shape the approach:

- **Rules and owners only.**
  - `classify.Apply` is the only writer of `classifications` and of the effective columns on `nodes`.
  - `domain.SuggestionModel` and the `model` CHECK values already exist, but nothing produces a model suggestion outside tests.
  - `Recompute` takes the current rule category first. A rule result of `unknown` is stored as the non-NULL category `'unknown'`, so today a model suggestion could never become effective.
  - Only rule traits count.
- **Discovery commits in a plain `sql.Tx`.** `commitDecision` runs in `store.Write`, not in a `jobs.Tx`, so it cannot enqueue a job. Its mode decision (`decide`) reads the effective category.
- **One device pool for source-less jobs.**
  - The runner keys claims by device: `''` for jobs without a source, otherwise `source:<id>` or `dev:<n>`.
  - Each key gets `workers_per_device` slots (default 1).
  - There are no priorities and no backoff. A handler can only succeed, pause, or fail.
- **No outbound HTTP, secrets, money, or floats in config.**
  - No production code makes an `http.Client` call.
  - The config docs test allows only string, bool, int, int64, duration, and `[]string` leaves, and single-segment table names.
  - M1 reserved `*_env` and `*_file` keys for secrets.
- **The descriptor digest is already semantic.** `discovery.Digest` excludes IDs, revisions, and timestamps, and M1 noted that the M3 cache would build on it.
- **A second process can share the database.** `store.Open` uses `_txlock=immediate` so that CLI commands can write while `serve` runs.
- **Latest migration:** `0003`. `nodes` and `classifications` cannot be rebuilt, so all changes are `ADD COLUMN` or new tables.

## Goals / Non-Goals

**Goals:**
- No outbound request is sent unless the owner permitted it, and none is sent past a cap, to an unpermitted profile, or with a non-metadata payload. Every check that can change after enqueue is repeated inside the transaction that reserves the attempt.
- Every model result is compared and swapped against the state it observed, exactly like probes and walks.
- Adapters are leaves: they import only the contract, the transport, and `config`, and they know nothing about nodes, jobs, or SQL.
- The schema, contract, tasks, ledger, cache, permission, and request primitives land in the foundation, so wave slices only add code.

**Non-Goals:**
- Model classification of loose inventory files (M3a). The `file_kind` task exists for the adapters and the evaluation only.
- Owner-written scoped rules (M3a).
- Priority classes and fair scheduling (M3a).
- Content evidence scopes, shadow sampling, and raw-body diagnostics.
- Automatic re-runs after a profile change. A profile change affects only future requests (§7.5); `reclassify-scope` is the explicit re-run.

## Decisions

### D1. Packages and import direction (§8.2, §7.1)

```text
domain ← classify/contract ← classify/providers/transport
classify/contract ← classify/providers/{disabled,fake,typesafe,structuredchat} ← classify/providers/registry
domain, contract ← classify/tasks
domain, contract, config ← classify/{permission,requests,ledger,cache}   (SQL over *sql.Tx; no network code)
contract ← classify                                                        (Suggestion.Model)
classify, tasks, registry, permission, requests, ledger, cache, jobs, reconcile ← classify/inference
requests ← discovery            (automatic eligibility hook)
tasks, registry, ledger, cache ← eval ← cmd/curator
```

- Adapters never import `classify`, `store`, `jobs`, or `domain` node types.
- `permission`, `requests`, `ledger`, and `cache` take a `*sql.Tx` or a reader and import no network code. Discovery and the commands can therefore call them without depending on adapters.
- Rejected:
  - Putting the contract in `domain`. It would make the leaf package carry an interface and transport-shaped error types.
  - Putting the orchestration in `internal/classify`. Five slices would edit one package, and the rule engine would import `net/http` through the registry.

### D2. The contract adds `Render` to §7.2's sketch (§7.2, §7.7)

`Classifier` has three methods:
- `Capabilities()`;
- `Classify(ctx, req)`;
- `Render(req) ([]byte, error)`, which returns the exact request body without credentials.

`Render` serves the owner's payload preview (§7.7). It also gives the request size that bounds the reservation (D13).

Errors are a single type, `*contract.Error{Outcome, RetryAfter, HTTPStatus, Detail}`. Its `Outcome` is one of the spec's attempt outcomes.

`contract.Validate(task, item, result)` is shared by every adapter. It checks:
- labels, finite numbers, and declared ranges;
- that a native distribution covers every option and sums to 1 within 1e-3;
- that evidence references are in the item;
- that a status other than `answered` has no label.

- Rejected: a separate preview service that re-encodes requests. The preview could then differ from what is actually sent.

### D3. Tasks and prompts are versioned embedded policy files (§7.2, §7.4)

- `policies/tasks/directory_category-v1.toml` defines the 13 labels with definitions, plus two yes/no questions: `coherent_unit` and `preservation_indicators`.
- `policies/tasks/file_kind-v1.toml` defines the 9 file kinds and no question.
- Each adapter embeds its own prompt templates, for example `typesafe/directory_category-v1`, each with its own revision. Task revision and prompt revision are recorded separately.
- The wording treats every filename as data and states that descendants were not inspected.
- Rejected: one combined task-and-prompt file. Changing a prompt would then change the task revision, and with it every cache key and evaluation.

### D4. Privacy transform `privacy-v1` and the outbound descriptor (§6, §7.6, §7.7)

`tasks.Outbound(d)` builds the semantic outbound descriptor from a `DirectoryDescriptor`.

It keeps:
- the display-escaped name and ancestor names;
- coverage: entries examined, whether the listing is complete, the budget, descendants not inspected, and zero content bytes;
- observed entries sorted by raw name, with their kind and mount-boundary flag;
- marker outcomes, signal IDs, file-kind counts, and error outcomes.

It drops:
- the source label and root;
- node IDs, revisions, and times;
- entry sizes. Sizes are not material to a category under the first taxonomy, and dropping them lets `touch`-style changes hit the cache.

Evidence IDs are renumbered in canonical order (`e1…`, `m1…`). The adapter result is mapped back to the descriptor's own IDs. Without this, two identical descriptors listed in different directory orders would miss the cache.

The canonical form is `"curator/outbound/privacy-v1\n"` followed by sorted-key JSON.

- Rejected:
  - Hashing names. A hashed name loses the very semantics the model classifies.
  - Sending the source label. It is operator prose and often personal.

### D5. `typesafe` mapping (§7.4, J4–J10)

- **Request.** One `POST` per item. Its `state` is the outbound descriptor as a JSON object, and it carries three questions:
  - `category`: a Choice over the task labels, with the definitions as criteria;
  - `coherent_unit`: a Noul;
  - `preservation_indicators`: a Noul.
  The file task sends one Choice, `file_kind`. Score is not used, because no domain question is a rubric level.
- **Result.**
  - The category is the native `choice`.
  - Measures are `native_probability` per label (scale `typesafe/choice-probability-v1`) and the returned `confidence` as `derived_confidence` (scale `typesafe/choice-confidence-v1`).
  - Each Noul becomes an assessment with a `native_probability` measure and an empty categorical answer.
  - Jev never abstains.
- **Status mapping.**

| HTTP status | Outcome |
|---|---|
| 401, 403 | `auth_failed` |
| 422 | `misconfigured` |
| 429 | `rate_limited`, honoring `Retry-After` |
| 529 | `overloaded` |
| other 5xx | `unavailable` |
| deadline exceeded | `timeout` |

- **Usage.** `input_tokens` and `output_tokens` come from the response. No provider charge is reported, so the charge is estimated (D13).
- **Model identity.** The response's `model` is the returned model. If the requested model differs from it, the request used an alias (D14).
- Rejected: one request batching several items. §7.4 says to start with one item per request, and batching couples their failures and their cache entries.

### D6. `structured_chat` mapping (§7.3, P1, P2)

- **Request.** `POST <endpoint>` (the full `/v1/chat/completions` URL) with:
  - `model`, `temperature: 0`, `seed: 0`, `max_tokens`;
  - a system message holding the task definition and the data-only instructions;
  - one user message holding the outbound descriptor as JSON.
- **Modes.**
  - `json_schema` sends a strict schema: `status` in answered/abstained/refused, `label` as an enum of the task labels or null, the assessments as an enum of `supported`, `unsupported`, or `unknown`, an optional `confidence` from 0 to 1, and `evidence`.
  - `json_object` sends `{"type":"json_object"}` with the schema in the prompt and validates the same way locally.
- **Outcomes.**
  - `finish_reason: "length"` is `truncated`.
  - A non-empty `message.refusal`, or `status: "refused"`, is `refused`.
  - Any parse or schema failure is `invalid_response`.
  - The model's `confidence` becomes a `self_reported_score` (scale `chat/self-reported-v1`). No `native_probability` is ever produced, because logprobs are not part of the tested subset (P2).
- **Usage.** `prompt_tokens` and `completion_tokens` when present, otherwise unknown.
- **Status mapping.** 401 and 403 are `auth_failed`; 400, 404, and 422 are `misconfigured`; 429 is `rate_limited`; 503 is `overloaded`; other 5xx are `unavailable`.
- The reference endpoint is a local Ollama with `qwen2.5:7b-instruct` in `json_schema` mode.
- Rejected:
  - Asking the model for per-label probabilities. They would be self-reported numbers dressed as a distribution.
  - A repair loop on invalid JSON. Retries are bounded by D12 instead.

### D7. Transport (§7.7)

`transport.Client` uses its own `http.Transport` with:
- `Proxy: nil`;
- `CheckRedirect` returning `http.ErrUseLastResponse`, so any 3xx is `misconfigured`;
- the profile deadline;
- `io.LimitReader(max+1)` on the response, so exceeding `max_response_bytes` is `invalid_response`.

Each client is pinned to its endpoint's origin. The `Authorization` header is set only by the adapter, on that origin.

`transport.Backoff(attempt, retryAfter, rnd)` computes full-jitter exponential backoff from 2 s, capped at 5 min. It never returns less than `Retry-After`.

- Rejected: honoring `HTTP_PROXY`. A proxy in the environment would route a `local` profile's traffic off-host without the owner seeing it (§7.7: "a localhost proxy is not proof that processing stays local").

### D8. Configuration (§7.5, §7.7, §12.3)

The new sections and keys are listed under Interfaces.

- **Profile revision.** A profile's revision is the SHA-256 of its semantic fields: adapter, endpoint, model, deployment revision, mode, output limit, and tasks. Prices, caps, limits, and secrets are excluded.
- **Money.** `config.Decimal` is a decimal string with at most 9 fraction digits, parsed exactly into nanos (D13). The docs test documents its type as `decimal`.
- **Secrets.** `*_env` and `*_file` are resolved by `config.ResolveSecrets`. `serve` calls it and fails on any problem. `check-config` reports every reference and whether it resolves, and exits 1 when one does not. `Load` does not read secrets, so the shipped example files load without them.
- **Fallback classes.** `fallback_on` defaults to `["rate_limited","overloaded","timeout","unavailable"]`.
- **Examples.** New example files `deploy/examples/jev-opt-in.toml`, `local-structured-output.toml`, and `switch-classifier-profiles.toml` are loaded by the docs test.
- Rejected:
  - Float prices. Float sums drift, and the docs test cannot express them.
  - Inline secrets.
  - Nested TOML tables. The docs test and the `check-config` round-trip support only single-segment table names.

### D9. Permission lives in the database and is set by the owner (§7.5, §7.7, §9.3)

- **Who sets what.** The operator file defines what is possible: profiles, endpoints, secrets, policies, and caps. The owner's `set-classifier-policy` command decides what is used for each source.
- **Storage.** `classifier_permissions` has one row per source: policy, permitted profiles (a subset of the policy's profiles), evidence scope (`metadata`, the only scope in M3), and a revision that increments on every change.
- **Revocation** sets `policy_id` to NULL in the same transaction that cancels the source's open requests (D10).
- Rejected: permission in the configuration file. Revoking would need a restart, so A43's "revoked after enqueue" could not be honored promptly. The owner also could not consent after seeing the payload.

### D10. Durable requests, per-source jobs, and a dispatch tick (§8.3, §8.4)

- **Request rows.** `inference_requests` holds one row per node and task, with:
  - `state`: `pending`, `running`, `done`, `failed`, or `cancelled`;
  - `trigger`: `automatic` or `owner`;
  - `job_id`, `not_before`, and an attempts count;
  - the captured CAS values;
  - the final outcome.
  A partial unique index allows one open row per node and task.
- **Automatic requests.**
  - `requests.Want(tx, node, task, now)` is called by discovery inside `commitDecision`. It inserts a pending row with no job when D16 says the node is eligible.
  - `inference.Service.Tick` runs every 5 s in `serve`. For each source with unassigned pending rows, it calls `EnqueueOnce(KindClassify, scope "classify:auto:<source>")` and assigns the rows to that job.
- **Owner requests.** They are created by `reclassify-scope` together with their own job (`classify:owner:<source>`). That job processes only its own rows.
- **Finishing.** The handler implements `PendingWork`, so a job that finishes while rows were assigned to it is re-queued.
- Rejected:
  - Enqueuing a job from `commitDecision`. It holds a plain `sql.Tx`, and jobs need a `jobs.Tx` to publish events.
  - One job per node. A rescan with many unknown directories would create thousands of job rows.

### D11. Worker pools and deferral in the runner (§8.4, §7.7)

- **Worker pools.** `Runner.RegisterPool(kind, handler, pool, capacity)` claims jobs of that kind under the key `pool:<name>` with its own capacity. Such jobs never take part in device exclusivity. `classify` jobs use pool `classifier`, sized by `classifier.workers` (default 2).
- **Deferral.** A handler that returns `*jobs.Defer{Until, Reason}` is stored `queued` with `available_at = Until`, without using an attempt. The job's `progress` shows the reason.
- Rejected:
  - Sleeping inside the handler. It holds a worker slot and is lost on restart.
  - Using `SourceID == ""`. It would serialize classification with nothing else, but still at `workers_per_device`, and the job could no longer be linked to its source.

### D12. One request's attempts: retries, fallback, escalation (§7.5, §7.7, A6, A24)

The candidates are the policy's primary and fallback profiles, in order, filtered by the grant, `enabled`, and task support. The attempts then follow these rules:

1. An outcome listed in `fallback_on` moves to the next candidate at once, without waiting. This happens only if the next candidate passes the dispatch checks (D13). If no candidate passes, the outcome is handled as in rule 2.
2. Another transient outcome (`rate_limited`, `overloaded`, `timeout`, `unavailable`, or `invalid_response`) retries the same profile. The request is set to `not_before = now + Backoff(...)`, at most `retries` times per profile.
3. `auth_failed`, `misconfigured`, or `unsupported` ends the request `failed`. The profile's latest operator error is recorded. There is no fallback or retry.
4. `answered`, `abstained`, or `refused` ends the attempts.
5. **Escalation.** If the policy has an escalation profile, and the result is `abstained`, or `answered` with a native probability of its label below `escalate_below_bp`, that profile is called once if permitted and affordable. The escalation result becomes the current suggestion. The first result is stored superseded and stays visible as a disagreement.

- **Deferral.** The handler processes rows that are ready. When none is ready but some are deferred, it returns `Defer{Until: min(not_before)}`.
- Rejected:
  - Retrying the primary before falling back. A rate-limited primary would hold the request for minutes while a permitted fallback idles.
  - Escalating whenever the providers disagree. §7.5 forbids resolving disagreement by comparing confidences.

### D13. Ledger: worst-case reservation per request (§7.7)

- **Fixed point.** `domain.Money` is int64 nanos plus an ISO currency. Every profile price must use `classifier.currency`.
- **Per-attempt bound.** An attempt's bound is `ceil(body_bytes × input_price ÷ 10⁶) + ceil(max_output_tokens × output_price ÷ 10⁶) + request_price`. Byte count bounds token count, because no tokenizer emits more tokens than input bytes.
- **Per-request reservation.** Before a request's first attempt, the server reserves the worst case: (1 + `retries`) × the bound for every candidate, plus the escalation bound.
- **Ledger rows.** `usage_ledger` is append-only:
  - `reserve` (+ the reservation);
  - `settle` (charge − reservation), written when the request ends;
  - unknown usage settles to 0, so the reservation stands.
- **Caps.** Each row carries its UTC day and job. A cap check is `SUM(amount) + new ≤ cap` over the day, and over the job.
- **Daily cap reached.** The request stays pending with `not_before` set to the next UTC midnight.
- **Job cap reached.** Automatic rows go back to unassigned pending. Owner rows end `failed` with `budget_exhausted`. The job finishes with terminal code `budget_exhausted`.
- Rejected:
  - Reserving per attempt. §7.7 says reservations include retries, fallback, and escalation.
  - Estimating tokens with a vendor tokenizer. That needs a new dependency per provider and is still only an estimate.

### D14. Semantic cache (§7.6, A25)

- **Key.** The key is SHA-256 of the canonical outbound descriptor (D4), followed by: task ID and revision, adapter and adapter version, profile ID and revision (D8), deployment revision, prompt revision, generation settings (part of the adapter version), privacy revision, and source.
- **Entries.** `classification_cache` stores validated results whose status is `answered`, `abstained`, or `refused`, with their provenance.
- **Aliases.** An entry expires after `alias_cache_ttl` when the returned model differed from the requested one (an alias). It never expires when they are equal. A deployment-revision change is a different key.
- **Hits.** A hit records an attempt with `cache_hit = 1`, no charge, and savings equal to the original estimated charge. It is applied through D15 like a fresh result.
- Rejected: keying by descriptor digest. That digest includes sizes and evidence IDs, which do not change the answer (D4), and it lacks the privacy revision.

### D15. Applying a result (§5.3, §7.5, §7.6)

The handler commits the result in one write transaction:

1. **Compare and swap.** It compares the values captured when the request was built (epoch, observation and intent revisions, scope dirty version, latest descriptor ID, permission revision) with their current values. Any difference sets `Suggestion.Stale`.
2. **Append.** `classify.Apply` stores the row with its new columns: `applied`, `adds_risk`, `result_status`, `measures`, `assessments`, `provenance`, `request_id`, and `cache_hit`. For a stale result whose node is still eligible, `requests.Want` runs again in the same transaction.
3. **Applied.** A row is applied when `status = answered`, its label is in `apply_categories`, and, if `min_probability_bp > 0`, its native probability for that label is at least that value. A label-only answer therefore needs `min_probability_bp = 0`.
4. **Adds risk.** `adds_risk` is set when the `preservation_indicators` assessment is `supported`, or its native probability is at least `risk_threshold_bp` (when that is above 0).
5. **Recompute.**
   - The precedence is owner override, then a rule category other than `unknown`, then an applied model category, then `unknown`.
   - Preservation risk is `PreservationRisk(category, rule traits)` OR any current model row with `adds_risk`.
   - Model rows add no traits.
6. **Hint.** If the effective category changed and the mode was set by policy, `reconcile.Hint(classification_changed)` runs. The next pass's unchanged probe then re-decides the mode through `decide`, so an applied `mixed` expands within the existing limits.

- Rejected:
  - Changing the inventory mode in the inference transaction. That would duplicate D5 of M2 outside discovery and race the frontier.
  - Model traits. Assessments are not rule matches with cited evidence (§7.5); risk may only be added, as with walk indicators.

### D16. Automatic eligibility (§5.3; owner choice)

`requests.Want` inserts an automatic `directory_category` row when all of these hold:
- the node is an active non-boundary directory;
- its current rule suggestion is `unknown` (`no_rule_matched` or `conflicting_rules`), and the decision is final (no escalated probe pending);
- it has no category override;
- its source has a grant for a `directory_category` policy;
- there is no open row for it;
- no current model row exists whose `descriptor_id` is the node's latest and whose provenance names the policy's current primary profile revision.

- Rejected: classifying every probed directory. The owner chose rule-unknown only.

### D17. `reclassify-scope` (§7.5)

- **Selection.**
  - Either `node_ids` (at most 1,000 directory IDs), or one expanded `node_id` with `scope: "subtree"`. A subtree means its active non-boundary directories, never descending into atomic units (the same traversal as M2a's `RequestSubtree`).
  - The selection is resolved when the command runs and stored as rows, so a "select all" is never re-evaluated later.
- **Per-item outcome.** Each item is one of `eligible`, `cached`, `not_permitted`, `not_directory`, `inactive`, or `no_descriptor`.
- **Dry run.** It returns the counts, the sum of D13 reservations for the eligible items that are not cached, and the per-item outcomes. It writes nothing.
- **Real run.** It creates the rows and one owner job per source, and returns 202 with the job of the first source and a `jobs` list.
- **Single item versus list.** With a single `node_ids` entry, an outcome other than eligible or cached is an error (409 `classifier_not_permitted` or `invalid_node_state`). For a list it is reported per item, and the call fails only if nothing is eligible.

### D18. Rate limits and concurrency are in-process (§7.7, §8.4)

Each profile has a token bucket of `requests_per_minute` and a semaphore of `max_concurrent`, shared by every job in the process. When no token is available, the row gets `not_before` set to the next token, rather than blocking a worker.

- Rejected: durable rate-limit state. Limits refill within seconds, and a restart only makes the first minute's rate conservative.

### D19. Evaluation (§7.8, §13.2)

- **Corpus.** A directory holding `corpus.toml` (`revision`, `description`) and `examples.jsonl`. Each example has `id`, `task`, `family`, `split`, `descriptor` (a `DirectoryDescriptor` or a `FileDescriptor`), `label`, and `preservation_risk`.
- **Seed corpus.** M3 ships a synthetic seed corpus, `deploy/eval/seed-corpus-v1` (about 60 examples). It covers Portuguese and English names, mixed directories, misleading application folders, unfinished arrivals, and private-looking metadata.
- **Run.** `eval run` calls the adapters directly. It uses the same ledger and caps, attributed to a pseudo-job ID of 0 with trigger `eval`, and the same retry rules without fallback. It sleeps between retries on the context.
- **Report metrics.**
  - per-category precision and recall, a confusion matrix, and coverage (completed / total);
  - preservation-risk false negatives: examples labeled at risk whose result would neither yield a non-`cleanup_candidate` triage nor add risk;
  - latency p50, p90, and max over all attempts;
  - cost per 1,000 completed decisions;
  - calibration as 10-bin reliability tables, only for `native_probability` measures and only for bins with at least 10 samples.
- **Summary.** Each run stores its summary in `evaluation_runs`.
- **Export.** `eval export` writes `examples.jsonl` with mode 0600. The family is `<source>/<first ancestor>`, and the split is `evaluation` when `fnv32(family) % 5 == 0`, otherwise `tuning`.
- Rejected:
  - Running the evaluation through the server's job queue. The command must work offline and on a copy of the database.
  - Converting scores to a common scale. §7.5 forbids it.

### D20. UI (§9.1)

- **Classifier page.** A new page `GET /classifier` with sections for profiles, policies, source permissions, usage, pending requests, and evaluations.
- **Permissions.** Each source row offers a grant form (policy, profile checkboxes, and a preview link per profile) and a revoke button. Both post `set-classifier-policy` from `app.js`, as the owner controls do.
- **Overview.** A spend line and a classifier line per source.
- **Inspector.** The suggestion table gains provenance, outcome, measure, and assessment columns for model rows. It also gets an "accept as override" button (an existing `set-classification`) and a "Reclassify…" control (dry run, then confirm).
- **Unchanged.** The CSP and the no-inline-script rule.

### D21. Contract fixtures and live recording (§13.1, project rule)

- **Shared suite.** `contract/contracttest.Run(t, factory, fixtures)` replays recorded HTTP exchanges from an `httptest` server and checks the domain results and outcomes. Both wire adapters pass the same suite.
- **Recording.** A `//go:build live` test in each adapter records fixtures with `-update`:
  - `typesafe` reads `TYPESAFE_API_KEY` and sends at most 12 synthetic descriptors;
  - `structuredchat` reads `CURATOR_LIVE_CHAT_ENDPOINT` and `CURATOR_LIVE_CHAT_MODEL`.
- **Edge-case fixtures.** Error and invalid-output fixtures (429, 529, malformed, refusal, truncation, bad distribution) are hand-written from the documented contract and marked as synthetic.
- **CI.** CI runs only the replays.

### D22. Migration `0004_model_classification.sql` (§8.3)

The migration is additive.

New tables:
- `classifier_permissions`;
- `inference_requests`;
- `inference_attempts`;
- `usage_ledger`;
- `classification_cache`;
- `evaluation_runs`.

New `classifications` columns:
- `applied` (`NOT NULL DEFAULT 1`; existing rule rows are applied);
- `adds_risk` (default 0);
- `result_status`, `measures` (default `'[]'`), `assessments` (default `'[]'`), and `provenance`;
- `request_id`, `cache_hit`;
- `observed_dirty_version`, `observed_permission_revision`.

`dirty_scopes.reasons` gains the value `classification_changed`. It is a JSON set with no CHECK, so no DDL change is needed.

- Rejected: a separate `model_suggestions` table. The append-only history, the stale logic, the inspector, and the A7 path all already read `classifications`.

### D23. Scope choices the spec leaves open

- Inventory loose-file classification is deferred to M3a. Routing policies already carry a task, so the `file_kind` policy slot exists, but only `eval` uses it in M3.
- `disabled` is an adapter. A policy whose primary is `disabled` is valid, and makes the "off" state explicit for a source.
- The `fake` adapter reads `fixture = "<path>"`, a JSON list of `{match, outcome, label, measures, assessments, delay}` rules. It ships for tests, the evaluation demo, and the smoke check.

## Interfaces

### Go signatures (foundation unless marked)

```go
// internal/domain
type Money struct{ Nanos int64; Currency string }
func ParseMoney(decimal, currency string) (Money, error) // ≤ 9 fraction digits, exact
func (m Money) String() string                           // "0.000012600"-trimmed decimal
const DirtyClassificationChanged DirtyReason = "classification_changed"
const ( // new ErrorCodes
	CodeClassifierNotPermitted  ErrorCode = "classifier_not_permitted"  // 409
	CodeClassifierMisconfigured ErrorCode = "classifier_misconfigured"  // 409
	CodeClassifierUnavailable   ErrorCode = "classifier_unavailable"    // 503
)

// internal/config
type Decimal struct{ Text string; Nanos int64 } // TextUnmarshaler; docs type "decimal"
type Classifier struct {
	Currency string  `toml:"currency"`  // default "USD"
	DailyCap Decimal `toml:"daily_cap"` // default "0"
	JobCap   Decimal `toml:"job_cap"`   // default "0"
	Workers  int     `toml:"workers"`   // default 2
}
type ClassifierProfile struct { // [[classifier_profiles]]
	ID, Adapter, Endpoint, Model, DeploymentRevision, Locality, Mode, Fixture string
	APIKeyEnv, APIKeyFile string
	Disabled bool
	Tasks []string
	Timeout, AliasCacheTTL Duration
	MaxResponseBytes int64
	MaxOutputTokens, Retries, RequestsPerMinute, MaxConcurrent int
	InputPricePerMTok, OutputPricePerMTok, RequestPrice Decimal
	PriceRevision string
}
type ClassifierPolicy struct { // [[classifier_policies]]
	ID, Task, Primary, Escalation string
	Fallback, FallbackOn, ApplyCategories []string
	MinProbabilityBP, RiskThresholdBP, EscalateBelowBP int
}
// Config gains: Classifier Classifier; ClassifierProfiles []ClassifierProfile; ClassifierPolicies []ClassifierPolicy
func (p ClassifierProfile) Revision() string // D8
type SecretStatus struct{ Profile, Ref string; Resolved bool; Problem string }
func ResolveSecrets(c Config) (map[string]string, []SecretStatus, error) // by profile ID

// internal/jobs
type Defer struct{ Until time.Time; Reason string } // error; handler outcome
func (d *Defer) Error() string
func (r *Runner) RegisterPool(kind Kind, h Handler, pool string, capacity int)

// internal/classify (Suggestion gains; Apply/Recompute signatures unchanged)
type ModelDetail struct {
	RequestID int64
	Status contract.ResultStatus
	Measures, Assessments, Provenance json.RawMessage // shapes below
	CacheHit, Applied, AddsRisk bool
	ObservedDirtyVersion, ObservedPermissionRevision int64
}
// Suggestion gains: Model *ModelDetail // required iff Kind == model
const ReasonModelAnswered = "model_answered"; ReasonModelAbstained = "model_abstained"; ReasonModelRefused = "model_refused"

// internal/classify/contract
type TaskID string // "directory_category", "file_kind"
type Task struct{ ID TaskID; Revision string; Labels []Label; Questions []Question }
type Label struct{ ID, Definition string }
type Question struct{ ID, Text string } // yes/no
type ItemDescriptor struct {
	Kind string // "directory" | "file"
	PrivacyRevision string
	Directory *OutboundDirectory
	File *OutboundFile
	Evidence []string // valid evidence IDs
}
func (d ItemDescriptor) Canonical() []byte
type Request struct{ Task Task; Item ItemDescriptor }
type ResultStatus string // "answered" | "abstained" | "refused"
type MeasureKind string  // "native_probability" | "derived_confidence" | "self_reported_score" | "uncalibrated_score"
type Measure struct{ Target string; Kind MeasureKind; Value float64; Scale string }
type Assessment struct{ Question, Answer string; Measures []Measure } // Answer: supported|unsupported|unknown|""
type Usage struct{ InputTokens, OutputTokens *int64; ProviderCharge *domain.Money; ProviderRequestID string }
type Provenance struct{ Adapter, AdapterVersion, RequestedModel, ReturnedModel, PromptRevision string; Alias bool; Latency time.Duration }
type Result struct {
	Status ResultStatus; Label string
	Assessments []Assessment; Measures []Measure; EvidenceRefs []string
	Provenance Provenance; Usage Usage
}
type Capabilities struct{ Tasks []TaskID; StructuredOutput string; NativeDistributions bool }
type Outcome string // the 12 spec outcomes
type Error struct{ Outcome Outcome; RetryAfter time.Duration; HTTPStatus int; Detail string }
type Classifier interface {
	Capabilities() Capabilities
	Render(req Request) ([]byte, error)
	Classify(ctx context.Context, req Request) (Result, error) // non-nil error is *Error
}
func Validate(task Task, item ItemDescriptor, r Result) error // *Error{Outcome: invalid_response}
// contract/contracttest
func Run(t *testing.T, newClassifier func(endpoint string) contract.Classifier, fixturesDir string)

// internal/classify/tasks
func Load() (*Set, error)                                   // embedded policies/tasks/*.toml
func (s *Set) Get(id contract.TaskID) (contract.Task, bool)
const PrivacyRevision = "privacy-v1"
func Outbound(d *domain.DirectoryDescriptor) (contract.ItemDescriptor, map[string]string, error) // map: outbound→descriptor evidence ID
type FileDescriptor struct{ Name domain.NameRef; Ancestors []domain.NameRef; Size *int64; KindHint domain.FileKind }
func OutboundFile(f FileDescriptor) contract.ItemDescriptor
type KeyParts struct{ Item []byte; Task contract.Task; Adapter, AdapterVersion, ProfileID, ProfileRevision, Deployment, Prompt, Privacy string; Source domain.SourceID }
func CacheKey(k KeyParts) [32]byte

// internal/classify/providers/transport
type Options struct{ Endpoint string; Timeout time.Duration; MaxResponseBytes int64 }
func New(o Options) (*Client, error) // rejects a non-absolute or non-http(s) endpoint
type Response struct{ Status int; Header http.Header; Body []byte }
func (c *Client) Post(ctx context.Context, header http.Header, body []byte) (Response, error) // *contract.Error on transport outcomes
func RetryAfter(h http.Header, now time.Time) time.Duration
func Backoff(attempt int, retryAfter time.Duration, rnd *rand.Rand) time.Duration

// internal/classify/providers/registry
type Deps struct{ Secret string; Clock clock.Clock; Logger *slog.Logger }
type Factory func(p config.ClassifierProfile, d Deps) (contract.Classifier, error)
func Register(adapter string, f Factory) // init(); panics on duplicate
func New(p config.ClassifierProfile, d Deps) (contract.Classifier, error)
func Adapters() []string // "disabled", "fake", "structured_chat", "typesafe"

// internal/classify/permission
type Grant struct{ Source domain.SourceID; Policy string; Profiles []string; Scope string; Revision int64; At time.Time }
func Current(ctx context.Context, q Queryer, source domain.SourceID) (Grant, error) // Revision 0 = never granted; Policy "" = revoked
func Set(ctx context.Context, tx *sql.Tx, g Grant, expected int64, now time.Time) (Grant, error) // revision_conflict
func Sent(ctx context.Context, q Queryer, source domain.SourceID, since int64) (int64, error) // attempts sent since the grant revision

// internal/classify/requests
type Trigger string // "automatic" | "owner" | "eval"
func Want(ctx context.Context, tx *sql.Tx, node domain.NodeID, now time.Time) (bool, error) // D16
func Create(ctx context.Context, tx *sql.Tx, node domain.NodeID, task contract.TaskID, t Trigger, job domain.JobID, now time.Time) (int64, error)
func CancelSource(ctx context.Context, tx *sql.Tx, source domain.SourceID, now time.Time) (int64, error) // open rows → cancelled; also cancel_requested on the source's classify jobs
func Assign(ctx context.Context, tx *sql.Tx, source domain.SourceID, job domain.JobID) (int64, error)
func Pending(ctx context.Context, tx *sql.Tx, job domain.JobID) (bool, error)

// internal/classify/ledger
type Reservation struct{ ID int64; Amount domain.Money }
func Reserve(ctx context.Context, tx *sql.Tx, req int64, job domain.JobID, amount domain.Money, caps Caps, now time.Time) (Reservation, error) // budget_exhausted
func Settle(ctx context.Context, tx *sql.Tx, r Reservation, charged *domain.Money, now time.Time) error // nil charged = unknown: settles to 0
type Caps struct{ Daily, Job domain.Money }
func Spent(ctx context.Context, q Queryer, day time.Time) (charged, reserved domain.Money, unknown int64, err error)
func Bound(bodyBytes int, p config.ClassifierProfile) domain.Money // D13 per-attempt bound

// internal/classify/cache
func Get(ctx context.Context, q Queryer, key [32]byte, now time.Time) (contract.Result, bool, error)
func Put(ctx context.Context, tx *sql.Tx, key [32]byte, r contract.Result, expires *time.Time, now time.Time) error

// internal/classify/inference (slice C)
const KindClassify jobs.Kind = "classify"
type Deps struct {
	Store *store.Store; Jobs *jobs.Runner; Config config.Config
	Classifiers map[string]contract.Classifier // by profile ID
	Tasks *tasks.Set; Clock clock.Clock; Logger *slog.Logger; Rand *rand.Rand
}
func New(d Deps) (*Service, error)
func (s *Service) Handler() jobs.Handler            // also jobs.PendingWork
func (s *Service) Tick(ctx context.Context) error   // D10
type Selection struct{ NodeIDs []domain.NodeID; Subtree domain.NodeID }
type Estimate struct{ Eligible, Cached, Blocked int; Reserve domain.Money; Items []ItemOutcome }
type ItemOutcome struct{ Node domain.NodeID; Outcome string }
func (s *Service) Estimate(ctx context.Context, sel Selection) (Estimate, error)
func (s *Service) Enqueue(ctx context.Context, tx *jobs.Tx, sel Selection) (Estimate, []jobs.Record, error)
func (s *Service) Preview(ctx context.Context, node domain.NodeID, profile string) (Preview, error)
type Preview struct{ Profile, Task, PrivacyRevision, Endpoint string; Body json.RawMessage }

// internal/eval (slice F)
type RunOptions struct{ Config config.Config; Store *store.Store; Corpus string; Profiles []string; Classifiers map[string]contract.Classifier; Tasks *tasks.Set; Clock clock.Clock; Sleep func(context.Context, time.Duration) error }
func Run(ctx context.Context, o RunOptions) (Report, error)
func Export(ctx context.Context, st *store.Store, w io.Writer) (int, error)

// internal/discovery (slice C): commitDecision and unchanged() call requests.Want after the decision is final.
// internal/commands (slices C, D): const ReclassifyScope = "reclassify-scope" (C); const SetClassifierPolicy = "set-classifier-policy" (D)
// internal/commands Deps gains: Classifier interface{ Estimate(...); Enqueue(...) } (integration wires it)
```

### Configuration keys

| Key | Type | Default |
|---|---|---|
| `classifier.currency` | string | `"USD"` |
| `classifier.daily_cap`, `classifier.job_cap` | decimal | `"0"` |
| `classifier.workers` | integer | `2` |
| `classifier_profiles[].id`, `.adapter`, `.endpoint`, `.model`, `.deployment_revision`, `.locality` (`local`, `lan`, `cloud`), `.mode` (`json_schema`, `json_object`, or empty), `.fixture`, `.api_key_env`, `.api_key_file`, `.price_revision` | string | required (`id`, `adapter`, `locality`), else `""` |
| `classifier_profiles[].disabled` | boolean | `false` |
| `classifier_profiles[].tasks` | array of strings | `[]` (all tasks the adapter supports) |
| `classifier_profiles[].timeout`, `.alias_cache_ttl` | duration | `"30s"`, `"24h"` |
| `classifier_profiles[].max_response_bytes` | integer | `262144` |
| `classifier_profiles[].max_output_tokens`, `.retries`, `.requests_per_minute`, `.max_concurrent` | integer | `512`, `2`, `60`, `1` |
| `classifier_profiles[].input_price_per_mtok`, `.output_price_per_mtok`, `.request_price` | decimal | `"0"` |
| `classifier_policies[].id`, `.task`, `.primary`, `.escalation` | string | required (`id`, `task`, `primary`) |
| `classifier_policies[].fallback`, `.apply_categories` | array of strings | `[]` |
| `classifier_policies[].fallback_on` | array of strings | `["rate_limited","overloaded","timeout","unavailable"]` |
| `classifier_policies[].min_probability_bp`, `.risk_threshold_bp`, `.escalate_below_bp` | integer | `0` |

Defaults of `[[…]]` entries are applied by `Load` per element. The docs test documents them as the per-element defaults.

### JSON stored in columns that another slice reads

```jsonc
// classifications.measures
[{"target": "category:documents", "kind": "native_probability", "value": 0.81, "scale": "typesafe/choice-probability-v1"}]
// classifications.assessments
[{"question": "preservation_indicators", "answer": "", "measures": [{"target": "question:preservation_indicators", "kind": "native_probability", "value": 0.12, "scale": "typesafe/noul-v1"}]}]
// classifications.provenance (also classification_cache.provenance)
{"profile": "jev", "profile_revision": "9f…", "policy": "dirs", "adapter": "typesafe", "adapter_version": "typesafe-v1",
 "endpoint": "https://api.typesafe.ai/v1/systemone", "requested_model": "jev-1.13.0", "returned_model": "jev-1.13.0",
 "deployment_revision": "jev-1.13.0", "alias": false, "task": "directory_category", "task_revision": "directory_category-v1",
 "prompt_revision": "typesafe/directory_category-v1", "descriptor_schema": 2, "descriptor_digest": "hex",
 "privacy_revision": "privacy-v1", "latency_ms": 412, "provider_request_id": null, "escalated_from": null}
// classifier_permissions.profiles
["jev", "ollama"]
// evaluation_runs.summary
{"corpus": "seed-v1", "examples": 60, "evaluated": 48, "complete": true,
 "profiles": [{"profile": "jev", "task": "directory_category", "completed": 48, "accuracy": 0.81, "macro_recall": 0.77,
   "review_rate": 0.1, "abstain_rate": 0, "validity": 1, "risk_false_negatives": 0,
   "latency_ms": {"p50": 300, "p90": 700, "max": 1200}, "cost_per_1000": "0.012", "measures": ["native_probability", "derived_confidence"]}]}
```

`inference_attempts` columns read by the UI: `profile_id`, `task`, `outcome`, `http_status`, `latency_ms`, `input_tokens` and `output_tokens` (NULL = unknown), `charge_nanos` (NULL = unknown), `provider_charge_nanos`, `price_revision`, `cache_hit`, `savings_nanos`, `started_at`, `finished_at`, `request_id`.

### Endpoints

**`POST /api/commands/set-classifier-policy`** (slice D, single item only)

Request to grant:

```json
{"source_id": "disk", "policy_id": "dirs", "profiles": ["jev"], "evidence_scope": "metadata", "expected_revision": 3}
```

Request to revoke:

```json
{"source_id": "disk", "policy_id": "", "expected_revision": 4}
```

Response 200:

```json
{"source_id": "disk", "policy_id": "dirs", "profiles": ["jev"], "evidence_scope": "metadata", "revision": 4,
 "cancelled_requests": 20, "already_sent": 3, "recall": "data already sent cannot be recalled"}
```

Errors:
- 404 `unknown_source`;
- 400 `invalid_request`: unknown policy, profiles not a subset of the policy's profiles, a scope other than `metadata`, or a policy whose task the source cannot serve;
- 409 `revision_conflict`.

The command writes an audit event `classifier_permission` with the old and new policy and profiles.

**`POST /api/commands/reclassify-scope`** (slice C)

Request:

```json
{"node_ids": ["12", "13"], "dry_run": true}
```

or

```json
{"node_id": "12", "scope": "subtree", "dry_run": false}
```

Response 200 for a dry run:

```json
{"eligible": 110, "cached": 30, "blocked": 10, "reserve": "0.000480000", "currency": "USD",
 "items": [{"node_id": "12", "outcome": "eligible"}]}
```

Response 202 for a real run:

```json
{"job_id": "41", "state": "queued", "coalesced": false, "jobs": ["41", "42"], "requests": 80, "cached": 30}
```

Errors:
- 404 `not_found`;
- 400 `invalid_request`: both or neither of `node_ids` and `node_id`, more than 1,000 IDs, or `subtree` on a non-expanded node;
- 409 `classifier_not_permitted` or `invalid_node_state`: a single item, or a list with nothing eligible;
- 429 `budget_exhausted`: a real run whose reservation exceeds the daily cap.

**`GET /api/classifier-profiles`** (slice E)

```jsonc
{"profiles": [{"id", "adapter", "endpoint", "model", "deployment_revision", "locality", "disabled", "tasks",
   "mode", "capabilities": {"tasks", "structured_output", "native_distributions"},
   "secret": {"ref": "env:TYPESAFE_API_KEY", "resolved": true},
   "price": {"currency", "input_per_mtok", "output_per_mtok", "request", "revision"},
   "last_error": {"outcome", "at"} | null}],
 "policies": [{"id", "task", "primary", "fallback", "fallback_on", "escalation", "apply_categories",
   "min_probability_bp", "risk_threshold_bp"}],
 "permissions": [{"source_id", "policy_id", "profiles", "evidence_scope", "revision", "granted_at", "already_sent"}]}
```

**`GET /api/classifier-usage?days=7`** (slice E)

```jsonc
{"currency": "USD", "daily_cap": "0.10", "job_cap": "0.02",
 "today": {"charged", "reserved", "unknown_attempts"},
 "rows": [{"day", "profile", "task", "attempts", "outcomes": {"answered": 40},
   "cache_hits", "input_tokens", "output_tokens", "unknown_usage", "charged", "provider_charged", "savings",
   "latency_ms": {"p50", "p90", "max"}}],
 "pending": {"requests", "deferred", "next_at"}}
```

**`GET /api/nodes/{id}/classifier-preview?profile=jev`** (slice E, through `inference.Service.Preview`)

```json
{"profile": "jev", "task": "directory_category", "privacy_revision": "privacy-v1",
 "endpoint": "https://api.typesafe.ai/v1/systemone", "body": {}}
```

`body` is the exact request body. Errors:
- 404 `not_found`;
- 400 `invalid_request`: unknown profile;
- 409 `invalid_node_state`: not a directory, or no descriptor.

**`GET /classifier`** (slice E) is the settings page.

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | Another command commits just before | …just after |
|---|---|---|---|
| Probe commit with `requests.Want` (D16) | everything M2a checks, plus D16 on the committed rows (override, grant, open row, current model row) | an override: not eligible. A revocation: no grant, no row | an override: the row is created, and at dispatch the stale CAS (intent revision) makes its result stale. A revocation: `CancelSource` cancels it |
| Dispatch tick (`Tick`, one `jobs.Tx` per source) | grant still exists; rows still pending and unassigned; `EnqueueOnce` | a revocation: rows already cancelled, no job | a revocation cancels the rows and the job; the job's dispatch check (next row) sends nothing |
| Build and reserve (one write per attempt group) | row still open and assigned to this job; node active; grant revision and profile in the grant; profile enabled; daily and job caps (`ledger.Reserve`); captures epoch, revisions, dirty version, descriptor ID, grant revision | a revocation: the row is cancelled, nothing sent. A new probe: captures the new descriptor | a revocation: the attempt in flight is sent (reported in `already_sent`); its result is stale (permission revision) and is not applied |
| Attempt record (after the HTTP call) | the attempt row exists; settles usage | — | — |
| Apply (D15) | the captured CAS values against current; policy applicability; `Recompute`; `Hint` when the effective category changed; cache put; `ledger.Settle`; row `done` | an override or probe: stale, no effect on the effective state; `Want` re-runs | an override: intent recomputes. A hint: the scope pass re-decides the mode |
| `set-classifier-policy` (D9) | source configured; policy and profiles valid; expected revision; `CancelSource` on revoke or narrowing; audit | another grant: `revision_conflict` | queued dispatches find a different grant revision and send nothing |
| `reclassify-scope` (D17) | each node's state and grant at resolution time; `Create` rows; `EnqueueOnce(classify:owner:<source>)`; daily cap against the summed reservation | a revocation: those items are `not_permitted` | a revocation cancels the rows |
| Runner claim of `classify` (D11) | pool capacity under `r.mu`; `available_at ≤ now` | — | — |
| `eval run` reservation (D19) | daily cap through `ledger.Reserve` | serve's spend counts | — |

## Risks / Trade-offs

- [The byte-count bound over-reserves roughly 3–4× the true token cost] → Amounts are tiny for Jev ($0.042 per Mtok), and the unused part is released on settle. The caps stay honest.
- [A rule-unknown directory that stays unknown after the model answered is never asked again until its descriptor changes] → This is intended. `reclassify-scope` is the explicit re-run, and a profile change re-asks through D16's profile-revision check only when the node is probed again.
- [An alias profile (`jev-latest`) can silently change model] → The returned model is recorded, alias entries expire after `alias_cache_ttl`, and the example configuration pins `jev-1.13.0`.
- [A 5 s tick delays the first automatic request] → It does not affect the scan, and the request latency stays visible.
- [In-process rate limits reset on restart] → They are conservative for one minute (D18).
- [Ollama output varies across versions] → Contract fixtures are replayed, and the live recording names the Ollama and model versions in the fixture header.

## Migration Plan

1. Back up with `curator backup`.
2. On first start, migration `0004` runs. It is additive, and existing rule rows become `applied = 1`. The M2a binary refuses the newer schema.
3. With no `[[classifier_profiles]]`, behavior is exactly that of M2a.
4. To enable classification:
   - The operator adds profiles, a policy, and caps, and sets the secret environment variable.
   - `check-config` must pass, and the operator restarts.
   - The owner grants each source on the classifier page, after reading the payload preview.
5. **Rollback:** stop the server, restore the backup, and start the M2a binary.

## Addendum: decisions made during apply

Added at archive (2026-10-03). These decisions were made or confirmed while M3 was implemented, and the code on `master` follows them. None deviates from `directory-first-curator-spec-v0.2.md`, so M3 adds no ADR. D36 corrects this change's own spec delta, not the source spec: §7.5 lets a model qualify for non-destructive labeling, and a suggested triage changes nothing on disk. D32 and D48 depart from the wording of D6 and D11 above and are recorded here.

### Configuration and storage

**D24. `config.Decimal` holds only `Nanos`.** Its text is derived with `FormatNanos`. It unmarshals TOML strings only.
- Rejected: accepting TOML floats. The decoder would format a float's binary approximation into the amount.

**D25. The grant's change time is `classifier_permissions.changed_at` (D9 says `granted_at`).** It records grants, narrowings, and revocations alike. The API presents it as `granted_at`.

**D26. `0004` indexes open requests per source.** `inference_requests_open_source` is a partial index on `source_id` for `pending` and `running` rows. Revocation (`CancelSource`) and the dispatch tick read through it instead of scanning the table.

**D27. Configuration details.** `config` imports `classify/contract` for task IDs and outcome names; `contract` imports only `domain`. Profile IDs match `[a-z0-9_-]+`. "Non-disabled" means `disabled = false`, so a priced profile on the `disabled` adapter still needs caps. The `jev-opt-in` example caps spending at 0.10 a day and 0.05 a job.

### Contract and adapters

**D28. Measure targets and ranges.** Targets are `category:<label>` or `file_kind:<label>`, `question:<id>`, and `answer` for a number about the chosen answer as a whole. Native probabilities, derived confidences, and self-reported scores lie in [0, 1]; an uncalibrated score may be any finite number. A native distribution covers every label once and sums to 1 within 1e-3.
- Risk: Jev's recorded distributions carry two decimals and summed exactly to 1. A rounding-skewed sum beyond 1e-3 would end `invalid_response`.

**D29. Capabilities carry `AdapterVersion` and `PromptRevisions` per task.** The router computes cache keys before sending (D14), so these must equal the provenance of the adapter's results.

**D30. Adapters are registered in an explicit table in the registry's `init()`.** Adapters never import `registry`. Each exports `New(p config.ClassifierProfile, secret string, clk clock.Clock) (contract.Classifier, error)`.
- Rejected: self-registration from each adapter's `init()` with blank imports in the registry, which forms an import cycle.

**D31. Jev (`typesafe`) mapping details (D5).**
- Each question's instructions use the documented structured form `{"context", "question"}`.
- `Retry-After` is honored on any transient status that carries it, not only on 429 and 529.
- The provider request ID comes from the `X-Typesafe-Request-Id` response header.
- Choice options follow the task's label order.

**D32. The `structured_chat` answer has no `status` field (departs from D6's text).** `label: null` is the abstention, and a refusal comes only from the message's `refusal` field. The model cannot label an abstention or contradict its own status.
- The prompt revision includes the mode, as in `chat/directory_category-v1/json_schema`, because `json_object` appends the schema to the prompt.
- Statuses the design leaves unlisted: another 4xx is `misconfigured`; a non-200 2xx or a 1xx is `invalid_response`; 503 honors `Retry-After`.
- An evidence ID cited twice is listed once.

**D33. Contract fixtures store no request body.** The suite asserts that the sent body equals `Render` output, so the payload preview is what is sent.

**D34. Transport and `fake` strictness.**
- When the caller's own context ends, `Post` returns `ctx.Err()`; only the profile deadline becomes `timeout`.
- Dial and TLS failures and connection resets are `unavailable`.
- Errors never repeat the endpoint or a header value, including a redirect's `Location`.
- The `fake` fixture rejects unknown fields, invalid patterns, tasks, or outcomes, and negative delays. An undeclared task answers `unsupported`, and a delay runs before a scripted error.

### Classification

**D35. An `unknown` keeps source `rule` when a rule suggestion exists.** It has no source only when the current suggestions are model-only. With no classifier, results are byte-identical to M2a (A20).
- Rejected: `unknown` never has a source. It would change every M2a `unknown`.

**D36. An applied model category follows the ordinary triage.** A model `cache` on a directory without risk or protection may suggest `cleanup_candidate`, while risk or protection on or beneath it keeps the triage off `cleanup_candidate` whatever the category's source. The spec delta "Model assessments can only add risk" was narrowed to match: no model answer removes a trait, a preservation risk, or a protection.
- Rejected: model categories never suggest cleanup. Such a directory would sit in no review queue, although the owner chose its category for `apply_categories` and dispositions stay the owner's.

**D37. Model rows.** The policy version is `<profile>@<profile revision>`. Cited evidence is one `matches` entry, `{"rule_id": "model:<profile>", "category", "evidence": [descriptor IDs]}`. Provenance carries `escalated_from` (the superseded first result's profile, else null) and `primary_revision` (the policy primary's revision when the request was built, whichever profile answered).

### Permission, routing, budgets, and the cache

**D38. `set-classifier-policy` details (D9).**
- A `file_kind` policy is refused with `invalid_request`: automatic eligibility serves `directory_category` only, so the grant would silently do nothing.
- A body without `policy_id` is refused, so a malformed client cannot revoke; `""` revokes.
- `already_sent` is reported on every accepted grant: the attempts sent under the revision being replaced.
- The audit detail keys are `source_id`, `old_policy`, `old_profiles`, `new_policy`, `new_profiles`, `old_revision`, `revision`, `cancelled_requests`, and `already_sent`.

**D39. An answer counts under the primary it was asked for (D16).** `requests.Want` treats a node as answered when its current model row's `primary_revision` (else `profile_revision`) equals the policy primary's current revision.
- Rejected: matching only the answering profile's revision. Every probe re-asked nodes a fallback or escalation had answered, and escalation then looped scans and jobs.

**D40. A fallback's cache answers only after a fallback outcome (D12, D14).** Before any attempt, only the cache of the candidate the request starts or resumes with is consulted. When a `fallback_on` outcome moves the request to the next candidate, that candidate's cache is consulted before anything is sent to it. Found by the A21 scenario.
- Rejected: consulting every candidate's cache up front. After a provider switch, the old provider's cached answers kept answering, and the new primary received nothing.

**D41. Affordability (D12, D13).**
- When the full worst case does not fit a cap, the longest affordable prefix of candidates, primary first, is reserved; candidates beyond it are never asked (A24).
- A request whose primary worst case exceeds a whole cap ends `failed` with `budget_exhausted` instead of deferring every day.
- A newer descriptor with a larger worst case gets a top-up reservation. At settle, the oldest reservation takes the charge and top-ups settle to 0.

**D42. Request and attempt endings.**
- `not_eligible` ends `cancelled`. `not_permitted`, `budget_exhausted`, and `misconfigured` end `failed`. `truncated` ends `failed` without retry.
- An attempt abandoned mid-call, or left unrecorded by a lost worker, is closed as `unavailable` with unknown usage and counted as sent.
- A cache hit records charge 0 with the entry's charge as savings. An ambiguous cache hit escalates only from the escalation profile's own cache entry.

**D43. Rate limits (D18).** Each profile has a token bucket of `requests_per_minute` that starts with one token, plus a `max_concurrent` semaphore. A row without a token is deferred to the next token's time.

**D44. One hint per write transaction.** `classification_changed` is hinted at most once per transaction. The request then adopts its scope's new dirty version, because the hint is its own doing.

**D45. Ledger readings.** `DayTotals.Spent` is the net of each settled reservation: the charge, or the whole reservation when usage was unknown. Sums are not filtered by currency. Unknown-usage counts include evaluation attempts.

### Jobs and serving

**D46. A deferred job's reason is in `terminal_detail` (departs from D11's text).** It reads `deferred until <time>: <reason>`. Progress stays `map[string]int64` across the exported job types. A pool registered with two capacities panics.

**D47. Owner and automatic requests use separate jobs.** Their scope keys are `classify:owner:<source>` and `classify:auto:<source>`.

**D48. `serve` always runs the classify pool and the dispatch tick.** Requests left by an earlier configuration then end visibly instead of staying queued, since a kind without a handler is never claimed. Discovery routing, `reclassify-scope`, and the payload preview exist only when a routing policy is configured, which keeps the M2a path otherwise. Disabled profiles get no adapter. The explorer receives `serve`'s clock.

**D49. The dispatch tick reads only open work.**
- Requests of ended jobs are found from the open requests through their partial index, never from every ended job.
- Reservations are settled from an in-memory watermark: the lowest unsettled request reservation. The ledger is append-only with increasing IDs, and a settled reservation stays settled.
- Requests left open on cancelled or failed classifier jobs are released: automatic ones go back to unassigned, owner ones end `cancelled`.

### Commands and pages

**D50. `reclassify-scope` details (D17).**
- The command answers 404 `not_found` when the server has no classifier service.
- The single-item and nothing-eligible refusals apply to dry runs too. Unknown node IDs fail the whole call.
- Cached items get rows too; the owner job applies them from the cache.

**D51. Classifier page and usage figures (D20).**
- `attempts` counts attempts sent. Cache hits are counted apart, and latency is nearest-rank over attempts sent.
- "Cap reached" shows when spent plus held meets a positive daily cap, or when requests wait for the next UTC midnight.
- The grant form lists `directory_category` policies only, and ticks no profile by default.
- Accept is offered for a current, unapplied model suggestion whose category is not `unknown`.
- A model suggestion awaiting review is shown in the inspector and the evidence API, not on review-queue rows.

### Evaluation

**D52. Evaluation details (D19).**
- `RunOptions` gains `RiskThresholdBP` (0 means 5000) and `Rand`.
- The review rate counts abstentions, refusals, and the label `unknown`. Summaries carry each profile's revisions, and unmeasured figures are null, never 0.
- Disabled profiles are refused. Per-source permissions are not consulted, because corpus examples have no source: running the command with a profile is consent to send that corpus.
- An adapter error that is not a `contract.Error` aborts the run after recording the attempt.

### Tests

**D53. Tests copy one migrated database per process.** `internal/store/storetest` migrates a template once per test process and copies it per test. Migration tests still open empty directories. The property test's seeds run in parallel.
- Why: the SQLite driver's allocator lock is process-wide, and migrating costs about a second per database under `-race`, so per-test migrations serialized whole packages. Discovery went from 69 s to 14 s.

**D54. The scenario harness wires classifiers as `serve` does.** It adds a scripted provider endpoint. Its quiesce counts a queued job as claimable only once its `available_at` has passed on the test clock.

Found during apply, outside M3: pages open the job event stream without the rendered snapshot's event ID. A job that ends between a page's render and the stream's start therefore stays shown in its old state until reload. The server already accepts `last_event_id` for this; the fix belongs to the explorer's live updates.
