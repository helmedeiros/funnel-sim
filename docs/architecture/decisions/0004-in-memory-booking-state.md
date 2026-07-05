# 4. In-memory booking state (no persistence)

## Status

Accepted — booking state lives in a `sync.Mutex`-guarded map that vanishes on process restart. Real production booking-svc will have its own durable store; the funnel-sim contract is that state is ephemeral and only lives for the duration of the traffic-gen session that created it.

## Context

funnel-sim tracks per-booking state so it can enforce the phase transition contract (initiated → reserved → purchased, terminal timeout at any post-init phase). Options:

1. **Persist to disk (SQLite / fsstore reuse).** Bookings survive restart; a partially-progressed booking left in state=reserved can be resumed after a container bounce.
2. **In-memory map with mutex.** Bookings vanish on restart. Simplest possible.
3. **In-memory with periodic snapshot to disk.** Compromise, most complex.

Reality check: funnel-sim's bookings are synthetic. traffic-gen creates them; nothing else reads their state (the only consumers are the events already in the bucket, not the live map). A container restart is a testing / rollout event, not a customer-facing event. Losing in-flight synthetic bookings across a restart costs nothing analytically.

Real booking-svc will have durable state — that's booking-svc's concern, not funnel-sim's.

Choice (2): in-memory only. This ADR.

## Decision

- `booking.Store` holds `map[string]*booking.Entry` guarded by `sync.Mutex`. Zero external persistence.
- `Init` is idempotent under retry with the same `bookingID`: second call returns the existing entry, no state mutation. Rationale: HTTP retries after a client-side timeout must not mint a duplicate booking.
- `Reserve` / `Purchase` from a terminal phase return `ErrTerminal` and map to HTTP 409. Reading a non-existent booking returns `ErrNotFound` and maps to 404.
- `Store.Len()` powers a Prometheus gauge (`funnel_sim_bookings_in_flight`) polled every 5s. Zero-cost observability into unbounded state growth.
- Store is bounded ONLY by process memory. There is no eviction policy in v1: traffic-gen sessions come and go, most bookings terminate quickly (purchased or timeout), memory footprint stays low under expected workloads.

### What we do NOT commit to

- No booking recovery on restart. When funnel-sim restarts, every in-flight booking is lost. A traffic-gen session mid-transition sees a 404 on its next call and can either restart the flow or drop the session. This is honest simulation behavior; it matches how a real production booking-svc crash would be handled during a rolling deploy, minus the durability guarantees.
- No TTL-based eviction. If a booking is neither purchased nor timed out, it accumulates until process restart. In practice traffic-gen's session mode terminates every session in bounded time, so unbounded growth would signal a driver bug.
- No cross-instance sharing. Multiple funnel-sim replicas do NOT see each other's state. Every booking is affinity-bound to the replica that received the `/booking/init`. A load balancer without session affinity would break this — compose runs one replica, so v1 is safe.

## Consequences

### Positive
- Zero external dependencies for booking state. funnel-sim + markup-svc + minio is enough; no SQLite, no Redis, no schema migrations. Compose bring-up is fast.
- Deterministic per-test cleanup. Tests instantiate a fresh `Store` and get a clean slate — no cleanup teardown needed.
- Trivial correctness reasoning. The state is one `map` under one mutex; no cross-goroutine invariants beyond the mutex boundary.

### Negative
- No booking survivability across restart. A rolling deploy of funnel-sim during an active traffic-gen run drops all in-flight synthetic sessions. Acceptable for the sim; unacceptable for a real service.
- Unbounded memory ceiling. A misbehaving traffic-gen that inits bookings without ever transitioning them to terminal would grow the store forever. Guarded by the `funnel_sim_bookings_in_flight` gauge + operator eyeball; not by an in-process eviction policy.
- Session affinity requirement. Any future multi-replica deployment needs sticky sessions or the state machine breaks (a `booking/reserve` may land on a replica that never saw the `booking/init`). Documented; not enforced in v1.

## References
- funnel-sim ADR-0001 — role of funnel-sim; why state can be ephemeral.
- funnel-sim ADR-0003 — booking.v1 schema; the emitted events are the durable record, not the in-memory state.
- markup-svc ADR-0034 — `env` label pattern (this gauge follows the same posture).
