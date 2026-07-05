# 3. `booking.v1` funnel-event contract

## Status

Accepted — one schema, four event types (`booking.initiated`, `booking.reserved`, `booking.purchased`, `booking.timeout`). Each transition emits one event. Terminal timeout at any post-init phase carries a `phase` field naming the phase the customer walked away from — collapsing that field into a single "timeout" bucket would lose half the elasticity signal.

## Context

The booking funnel has a natural state machine:

```
booking.initiated ──► booking.reserved ──► booking.purchased  (terminal success)
        │                     │
        │                     └────► booking.timeout {phase:"reserved"}   (walked at payment)
        │
        └──────────────────────────► booking.timeout {phase:"initiated"}  (walked at info entry)
```

Two possible schema shapes:
1. **Separate schemas per event type.** A `booking_initiated.v1`, `booking_reserved.v1`, `booking_purchased.v1`, `booking_timeout.v1` — four types in four separate ADRs.
2. **One schema with an `event_type` field distinguishing the transition.**

Choice (1) minimizes null fields per row but multiplies the schema surface area by four. Every downstream consumer maintains four struct definitions, four SQL views. Choice (2) has some null fields (e.g. `order_id` only on `booking.purchased`) but is one struct, one view, one join. For the Learning Loop's join shape — LEFT JOIN funnel to decisions on `decision_id` — one flat schema is cheaper to reason about and query.

Choice (2). This ADR.

## Decision

### Schema

```jsonc
{
  "schema_version": "1.0.0",
  "event_type":     "booking.initiated",     // | reserved | purchased | timeout
  "event_id":       "…",
  "ts":             "2024-09-14T…Z",
  "env":            "production",

  "journey_id":     "…",                     // ← primary session-level join key
  "search_id":      "…",                     // which search this offer came from
  "decision_id":    "…",                     // ← primary offer-level join key to markup.decision.v1
  "offer_id":       "…",
  "booking_id":     "…",                     // one per booking attempt
  "order_id":       "…",                     // ONLY on booking.purchased

  "price":          49.99,                   // customer-visible price
  "markup_factor":  1.08,
  "revenue":        49.99,                   // ONLY on booking.purchased
  "currency":       "EUR",

  "experiment":     "BR_peak_uplift_v3",     // A/B arm identity, stamped on EVERY event
  "variant":        "treatment",

  // On booking.timeout ONLY:
  "phase":          "initiated",             // | "reserved"
  "timeout_kind":   "customer_abandoned",    // v1 only value; "reservation_ttl_expired" is parked

  "correlation_id": "c-…",
  "trace_id":       "…",
  "span_id":        "…"
}
```

### The `phase` field on timeout — why it earns its keep

Two customer behaviors both surface as "timeout" but carry different signals:
- `phase: "initiated"` — the customer saw the offer, clicked to book, then walked away before providing personal info. High price-sensitivity signal ("sticker shock", "comparison shopping").
- `phase: "reserved"` — the customer provided info, reached the payment step, then walked. Lower price-sensitivity signal, higher form/payment-friction signal.

Collapsing these into one event ("something timed out") kills the ability to distinguish the two effects in analysis. Splitting them on the `phase` field lets a single SQL query with `WHERE phase = 'initiated'` isolate the price-shock cohort from the payment-friction cohort.

### `timeout_kind` is fixed at `customer_abandoned` in v1

Real production has a second timeout mode: reservation TTL expiry (seat holds released after 15 min). funnel-sim does not model TTLs; every timeout in v1 is caller-driven via `POST /booking/timeout`. When a background reaper lands (parked), `timeout_kind: "reservation_ttl_expired"` becomes a second value; consumers already read the field so no schema bump is needed.

### `experiment` + `variant` on every event

Denormalizing the arm identity onto every event eliminates the need to re-join to `markup.decision.v1` to know which arm a downstream event belongs to. A/B lift analysis becomes a single-table window function per event stream:

```sql
SELECT variant,
       COUNT(*) FILTER (WHERE event_type='booking.purchased') * 1.0
         / COUNT(*) FILTER (WHERE event_type='booking.initiated') AS init_to_purchase_rate
FROM funnel_events.booking_v1
GROUP BY variant
```

### Partition layout

The s3sink partitions booking events by `event_type` so a revenue query hitting `event_type=booking.purchased/` scans only ~5% of the total volume:

```
funnel-events/booking-v1/
  dt=YYYY-MM-DD/hour=HH/env=<env>/event_type=booking.initiated/instance=<hostname>/batch-<…>.jsonl.gz
  dt=YYYY-MM-DD/hour=HH/env=<env>/event_type=booking.reserved/instance=<hostname>/batch-<…>.jsonl.gz
  dt=YYYY-MM-DD/hour=HH/env=<env>/event_type=booking.purchased/instance=<hostname>/batch-<…>.jsonl.gz
  dt=YYYY-MM-DD/hour=HH/env=<env>/event_type=booking.timeout/instance=<hostname>/batch-<…>.jsonl.gz
```

Athena / DuckDB / Snowflake `COPY INTO` all read the Hive partition scheme natively.

## Consequences

### Positive
- One schema, one struct, one SQL view. Downstream consumers pay one join to funnel-events, not four.
- Timeout phase field preserves the two customer-behavior signals as separate cohorts. Price-shock and payment-friction analyses become one `WHERE` clause apart.
- Per-event-type partitioning scales revenue queries against a small fraction of the data.

### Negative
- Some fields are nullable per event_type (`order_id` on purchased, `revenue` on purchased, `phase` + `timeout_kind` on timeout). Consumers reading with a strict schema tool must handle the sparse columns. Documented in this ADR.
- Denormalized `experiment` + `variant` add ~30 bytes per event. At ~4× booking events per journey and ~10% click-through, the redundancy is bounded and cheap relative to the query ergonomics gain.

### Not closed
- **System-driven timeouts.** `timeout_kind: reservation_ttl_expired` is parked. Needs a background reaper in funnel-sim + a TTL config.
- **Payment failure branching.** v1 assumes `booking.purchased` always succeeds. Real payment flows have declines, 3DS challenges, refunds. Modelling those is a whole additional state branch — parked as a separate arc.
- **Cross-currency.** `currency` is emitted but funnel-sim only produces EUR in v1. Real search-svc / booking-svc will emit heterogeneous currencies; consumers should not assume it.

## References
- markup-svc ADR-0035 — `markup.decision.v1` join partner via `decision_id`.
- funnel-sim ADR-0002 — `search.v1`, the other half of the funnel-events schema.
- `~/Code/workspaceBRE/learning-loop-plan.local.md` §Funnel state machine, §Event contracts.
