# 1. Role of funnel-sim in the platform

## Status

Accepted — funnel-sim is the downstream-services stand-in for the Pricing Decision Platform's outcome half. It is not a load driver; it is not a fake customer; it is a stand-in for what will later be independent real services (search-svc, booking-svc, payment-svc). Every event that would be emitted by one of those real services in production is emitted by funnel-sim today under the same schema, with the same identity fields, in the same bucket layout.

## Context

The Pricing Decision Platform's C4 sketch shows a "Learning Loop (context + decision + outcome)" arrow between the Pricing Decision API and the Decision Event Stream. markup-svc closed the "context + decision" half via ADR-0035 (decision-event contract) and ADR-0036 (S3 substrate). The "outcome" half — did the customer actually book at this price? did they walk away before payment? — has no runtime home.

Real production would place outcomes on real search, booking, and payment services. None of those exist in the sandbox yet. The choices facing this arc were:

1. **Emit outcomes from traffic-gen directly.** traffic-gen is already the load driver. A `--profile=session` mode could POST directly to a MinIO endpoint, no new service needed. Rejected: the events would carry `service=traffic-gen` in their metadata. When real booking-svc lands, the field flips to `service=booking-svc` — silent schema break at cutover. Beyond that, a single emitter for both "was the offer shown" and "was the offer bought" collapses two events that must be statistically independent for A/B lift math to work.
2. **Build a real booking-svc now.** Rejected: it's a multi-quarter build. The arc needs to prove the correlation + measurement mechanics against realistic-shape events; a real booking-svc is unrelated infrastructure for that goal.
3. **Build funnel-sim as a service that stands in for real downstream services under the same wire contract.** Accepted (this ADR).

## Decision

funnel-sim is a Go binary exposing five HTTP endpoints:

- `POST /search` — accepts a search query, fans out N `/decide` calls to markup-svc, aggregates priced offers, emits one `search.v1` event.
- `POST /booking/init` — accepts a chosen offer, mints a booking_id, records `PhaseInitiated` in the in-memory store, emits `booking.v1{event_type=booking.initiated}`.
- `POST /booking/reserve` — transitions `initiated → reserved`, emits `booking.v1{event_type=booking.reserved}`.
- `POST /booking/purchase` — transitions `reserved → purchased`, mints an order_id, emits `booking.v1{event_type=booking.purchased, order_id, revenue}`.
- `POST /booking/timeout` — terminal abandonment from any post-init phase, emits `booking.v1{event_type=booking.timeout, phase=<walked_away_from>, timeout_kind=customer_abandoned}`.

funnel-sim IS a service. It:
- Emits events with `service=funnel-sim` today — the same slot real booking-svc will use tomorrow.
- Maintains in-memory booking state that only funnel-sim reads (no other consumer reads its state; consumers read the emitted events from the bucket).
- Exposes health probes (`/healthz`, `/readyz`), Prometheus `/metrics`, structured JSON logs matching the platform's Filebeat contract, and W3C-propagated OTel spans so decision-gateway → funnel-sim → markup-svc renders as one Jaeger waterfall.

funnel-sim IS NOT:
- A load driver. traffic-gen is the load driver. funnel-sim receives traffic; it does not generate it.
- A fake customer. traffic-gen models the customer (or a real browser will, in production).
- A permanent piece of the architecture. When real search-svc / booking-svc / payment-svc land, funnel-sim's endpoints are replaced one at a time. Each replacement satisfies the same HTTP contract and emits the same event schema, so downstream analytics keeps working through the swap.

### Layered-swap architecture

The load-bearing insight this ADR encodes: the CUSTOMER axis (traffic-gen ↔ real browsers) and the DOWNSTREAM SERVICES axis (funnel-sim ↔ real services) are orthogonal. Each can be swapped independently without breaking the other. traffic-gen swap has no impact on funnel-sim's wire contract. funnel-sim swap has no impact on traffic-gen's session driver. The two swaps happen on different clocks in the real world; the architecture pre-commits to letting them.

## Consequences

### Positive
- The outcome half of the Learning Loop becomes implementable today. `funnel-events/booking-v1/event_type=booking.purchased/…` populated by funnel-sim joins on `decision_id` to `markup-decisions/markup-decision-v1/…` from markup-svc; conversion and elasticity fall out of a single DuckDB query.
- Independent swap-out. When real search-svc lands, funnel-sim's `/search` handler retires; the compose file swaps `funnel-sim` for `search-svc` at the network layer; nothing else changes.
- Realistic event-emitter identity. `service=funnel-sim` today matches the shape of `service=booking-svc` tomorrow. Consumers keying on the identity field see one continuous stream across the cutover.

### Negative
- One more service to run in local dev. Compose file grows by one entry; a full stack now needs minio + markup-svc + decision-gateway + funnel-sim + (optionally) traffic-gen + observability. `docker compose up` is the mitigation.
- Duplication with markup-svc. funnel-sim reuses the sink shape (queue → gzipped JSONL → S3 with Hive partitions) but with typed events (SearchEvent, BookingEvent) rather than markup-svc's Event. Copy-paste, not shared library. See ADR-0005 for the trade-off.
- Behavioural fidelity is bounded. funnel-sim's in-memory state is not a real booking system: no reservations expire on a TTL, no seat inventory is decremented, no payment engine models 3DS challenges or declines. It emits the events, not the business logic. The gap is documented per-endpoint and accepted for v1.

## References
- Pricing Decision Platform C4 sketch — the Learning Loop arrow this service completes.
- markup-svc ADR-0035 — `markup.decision.v1` schema (join partner via `decision_id`).
- markup-svc ADR-0037 — `decision_id` in response + `journey_id` in `request_context` (the wire contract funnel-sim consumes).
- `~/Code/workspaceBRE/learning-loop-plan.local.md` §Layered-swap architecture, §funnel-sim service.
