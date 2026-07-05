# 2. `search.v1` funnel-event contract

## Status

Accepted — one event per rendered search page. The event carries every priced offer shown to the user, each with its own `decision_id`, so a downstream `LEFT JOIN` from `search.v1.offers[].decision_id` to `booking.v1.decision_id` yields both selected and unselected offers cleanly.

## Context

A search-page render prices N offers via N independent `/decide` calls to markup-svc. The user may pick 0 or 1 of them for booking. The unselected offers carry the load-bearing price-elasticity signal ("we showed BR-DE at €49.99 in position 3 and the user did not click"). A schema that emitted only the clicked offer would throw away 87% of the elasticity information at the default click-through rate.

## Decision

### Schema

```jsonc
{
  "schema_version": "1.0.0",
  "event_type":     "search.viewed",
  "event_id":       "…32-char hex or ULID…",
  "ts":             "2024-09-14T…Z",
  "env":            "production",

  "journey_id":     "…",
  "search_id":      "…",

  "query": {
    "route":         "BR-DE",
    "departure":     "2024-09-14",
    "passengers":    1,
    "channel":       "web",
    "device":        "desktop",
    "country":       "DE",
    "customer_tier": "gold"
  },

  "offers": [
    {
      "offer_id":      "route-BR-DE-2024-09-14-t1",
      "decision_id":   "…",
      "position":      1,
      "price":         49.99,
      "markup_factor": 1.08,
      "experiment":    "BR_peak_uplift_v3",
      "variant":       "treatment"
    }
    // …
  ],

  "correlation_id": "c-…",
  "trace_id":       "…",
  "span_id":        "…"
}
```

### Contract semantics

- **One event per `/search` HTTP call**, regardless of how many offers survive the fan-out. A zero-offer search (every `/decide` failed) still emits an event — analytics needs to see "we tried to render and everything upstream broke" as a distinct row from "no search happened".
- **`offers[].decision_id` is the load-bearing join key.** funnel-sim never mints it — it comes verbatim from the markup-svc `/decide` response (ADR-0037). Consumers join on it to attribute a booking event back to the priced offer that led to it.
- **`experiment` + `variant` are stamped per-offer** so a single search event carries enough information to segment offers by experiment arm without re-joining to the decision event. Aggregate lift queries become single-table window functions per event.
- **`position` is 1-indexed.** Analytics ordinarily interprets position 1 as "top of the results page" (the ranking bias effect); the offer generation loop places the cheapest / most-relevant offer first.
- **Currency is implicit as EUR for v1.** Multi-currency support is a future extension and requires the decision to be reissued per currency — parked in Not-closed.

### Partition layout

The s3sink lands search events under a single Hive-partitioned prefix:

```
funnel-events/search-v1/dt=YYYY-MM-DD/hour=HH/env=<env>/instance=<hostname>/batch-<RFC3339>-<seq>.jsonl.gz
```

No per-event-type partition split because `event_type` is fixed at `search.viewed` in v1. Any future `search.filtered` or `search.paginated` extension either bumps to `search.v2` (breaking) or introduces its own partition segment (additive).

### Field additivity

`schema_version` follows SemVer applied to the contract:
- **Minor** bumps add optional fields. Consumers must ignore unknown fields (standard JSON contract). Never require a downstream code change.
- **Major** bumps break: rename, remove, or change a field type. Event name updates to `search.v2`. Parallel emission during the migration window per markup-svc ADR-0035's precedent.

## Consequences

### Positive
- One event per search page, but every offer's identity is preserved. Elasticity analysis has the full signal without a downstream fan-out reconstruction step.
- Consumers can slice by `experiment` / `variant` directly on the search-events table — no join to decisions required for arm-level segmentation.
- `LEFT JOIN` to `booking.v1` on `offers[].decision_id` gives both selected and unselected offers in one query, which is what conversion + elasticity math needs.

### Negative
- One event carries up to N offers. At N=50 (a busy long-haul search page) and 500 KB uncompressed per event, batch sizes grow fast. `--event-sink-batch-every=30s` (compose default) is honest at typical scenarios; operators driving large-N searches at sustained load may want a smaller flush interval so batches do not saturate a single object write.
- `experiment` and `variant` denormalized onto every offer. Some redundancy against the emitted decision event — accepted for query ergonomics.

### Not closed
- **Multi-currency.** v1 assumes EUR. A future minor bump adds `offers[].currency`; deeper multi-currency changes (per-currency A/B arms, per-currency conversion) may need a v2 model.
- **Ranking / relevance signals.** `position` is emitted but nothing about relevance score, personalisation vector, or sort mode. Additive minor bump when a real search-svc lands.
- **N-offers-per-search config.** funnel-sim's per-request `offer_count` knob is a synthetic-load knob. Real search-svc will emit whatever N its ranker returned; consumers should not assume N is bounded.

## References
- markup-svc ADR-0037 — `decision_id` in `/decide` response, the source of every `offers[].decision_id` value.
- funnel-sim ADR-0001 — role of funnel-sim, why this schema is stamped by a service.
- funnel-sim ADR-0005 — the typed s3sink that transports this event to MinIO.
