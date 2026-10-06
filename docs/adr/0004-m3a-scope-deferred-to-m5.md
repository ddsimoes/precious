# ADR 0004: The staged-producer handoff, owner tags, and owner-written rules move to M5

- Status: accepted
- Date: 2026-10-03
- Context: `directory-first-curator-spec-v0.2.md` §5.3, §7.6, §9.4.2, §9.4.4, §14 (M3a and M5), A34; OpenSpec change `m3a-managed-inbox-intake`, proposal "Non-goals" and design D12 and D22.

## Context

§14 gives M3a "directory roles, durable arrivals/readiness, prioritized metadata classification, destination proposals", with A32-A33, A36-A37, and A39-A41. M5 gets "inbox organization" with A34-A35, A38, and A44-A45. Several features that the spec describes inside the inbox workflow have no M3a acceptance scenario:

- §9.4.2: `handoff_confirmed` comes from Mark ready or from "a configured cooperative producer protocol" with an excluded same-filesystem staging directory. A34 ("Cooperative staged handoff or owner Mark ready, followed by approval") is its only test, and it is an M5 scenario.
- §9.4.4: routing rules match "effective categories, file kinds, owner tags, and inbox identity", and an owner correction "may offer a preview of a reusable routing rule".
- §5.3 places an "applicable user rule" in the classification precedence, and §7.6 lets corrections "create explicit scoped rules". No milestone before M3a built owner-written rules or owner tags.

The owner chose the scope at proposal time (design D22).

## Decision

- M3a confirms handoff only through `mark-intake-ready`. The cooperative producer protocol, its configuration, and the staging-area exclusion arrive in M5 with A34.
- M3a routing rules match effective category, file kind, and inbox identity. Owner tags, which do not exist yet, and routing-rule previews from corrections arrive in M5.
- Owner-written classification rules and rules created from corrections arrive in M5. M3a triage uses the existing deterministic detectors, name hints, and the inbox's classifier grant.

## Alternatives rejected

- Building the producer protocol in M3a: its configuration and staging exclusion would ship with no consumer and no acceptance scenario until M5 approves moves.
- Adding owner tags only as a routing input: a match field with no way to set it would be dead configuration.

## Consequences

- An arrival reaches `handoff_confirmed` only when the owner marks it ready. Other arrivals become ready through the heuristic of §9.4.2, and directories stay provisional.
- The M5 change must add the producer protocol and A34, owner tags as a routing-rule input, rule previews from corrections, and owner-written classification rules at the "applicable user rule" level of §5.3.
- `routing_rules.match_kind` allows only `category` and `file_kind`, so adding an owner-tag match in M5 needs a migration.
