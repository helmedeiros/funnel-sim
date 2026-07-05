# funnel-sim

Stand-in for the search / booking / payment services on the Pricing Decision Platform. Emits `search.v1` and `booking.v1` funnel events to an S3-compatible bucket so the outcome half of the Learning Loop is measurable end-to-end without waiting for real downstream services to land.

**funnel-sim is a service.** It receives HTTP calls (from `traffic-gen` in dev, from real BFFs in the future) and emits events. It is not a load driver — that role stays with `traffic-gen`. See [ADR-0001](docs/architecture/decisions/0001-role-of-funnel-sim.md) for the full role framing and the layered-swap architecture that keeps funnel-sim independently replaceable.

## Endpoints

| Method | Path | Emits | Notes |
|--------|------|-------|-------|
| POST   | `/search`           | `search.v1`                          | Fans out N `/decide` calls, aggregates offers |
| POST   | `/booking/init`     | `booking.v1{event_type=initiated}`   | Mints `booking_id`, records `PhaseInitiated`  |
| POST   | `/booking/reserve`  | `booking.v1{event_type=reserved}`    | `initiated → reserved`                        |
| POST   | `/booking/purchase` | `booking.v1{event_type=purchased}`   | Mints `order_id`, records revenue             |
| POST   | `/booking/timeout`  | `booking.v1{event_type=timeout}`     | Terminal at any post-init phase, carries `phase` |
| GET    | `/healthz`          |                                      | liveness probe                                |
| GET    | `/readyz`           |                                      | readiness probe                               |
| GET    | `/metrics`          |                                      | Prometheus scrape (`--metrics-enabled`)       |

## Quick start (compose)

`decision-gateway/docker-compose.yaml` includes funnel-sim as a `build:` service pointing at this repo (sibling directory). From the decision-gateway repo:

```bash
docker compose up funnel-sim
# ... call it directly:
curl -X POST http://localhost:8081/search \
  -H 'Content-Type: application/json' \
  -d '{"journey_id":"j-1","route":"BR-DE","departure":"2024-09-14","offer_count":3}'
```

The bucket layout `funnel-events/{search-v1,booking-v1}/dt=…/hour=…/…` auto-creates on first PUT when `--event-sink-bucket-auto-create` is set.

## Correlation model

funnel-sim assumes the following IDs on the wire:

- `journey_id` — session identity. Caller-supplied. Threaded to markup-svc as `request_context.journey_id` per markup-svc ADR-0037. THE session-level join key.
- `decision_id` — one per priced offer. Minted by markup-svc, returned in the `/decide` response body (ADR-0037), carried on both `search.v1.offers[].decision_id` and every subsequent `booking.v1.decision_id`. THE offer-level join key.
- `search_id` — one per search render. Minted by funnel-sim. Groups the N offers shown in one render.
- `booking_id` — one per reservation attempt. Minted by funnel-sim's `/booking/init`.
- `order_id` — one per successful purchase. Minted by funnel-sim's `/booking/purchase`.

`trace_id` / `correlation_id` are observability-only and are the wrong scope for funnel analytics. Every event carries them anyway for cross-plane debugging.

## Observability

Three planes, all wired by default when the flags are on:

- **Prometheus** (`--metrics-enabled`) — `funnel_sim_requests_total{method,path,status_class,env}`, `funnel_sim_request_duration_seconds`, `funnel_event_sink_flushed_total{event_family,env}`, `funnel_event_sink_dropped_total{event_family,reason,env}`, `funnel_event_sink_flushed_bytes_total`, `funnel_event_sink_objects_total`, `funnel_sim_bookings_in_flight{env}`.
- **Structured JSON logs** (`funnel-sim.access`, `funnel.event.sink.flushed`, `funnel.event.sink.flush_failed`, `funnel.event.sink.buffer_full`) emitted to stdout in the platform shape `{time, level, msg, attrs}`. Filebeat's `decode_json_fields` processor lands them in `platform-logs-*` alongside markup-svc and decision-gateway logs.
- **OTel spans** (`--otel-enabled`) — the funnel-sim handler is a trace HOP. Incoming W3C traceparent is extracted, the outbound `/decide` call to markup-svc inherits it, so a full trace shows traffic-gen → decision-gateway → funnel-sim → markup-svc as one waterfall.

## ADRs

| ADR | Title |
|-----|-------|
| [0001](docs/architecture/decisions/0001-role-of-funnel-sim.md) | Role of funnel-sim in the platform |
| [0002](docs/architecture/decisions/0002-search-v1-contract.md) | `search.v1` funnel-event contract |
| [0003](docs/architecture/decisions/0003-booking-v1-contract.md) | `booking.v1` funnel-event contract |
| [0004](docs/architecture/decisions/0004-in-memory-booking-state.md) | In-memory booking state |
| [0005](docs/architecture/decisions/0005-emit-via-typed-s3sink.md) | Emit via typed generic s3sink |

## Quality gates

`make ci-local` runs `go vet`, race-detector tests, the coverage gate (`COVER_MIN` in the Makefile), and the ADR-index check.

## License

[MIT](LICENSE)
