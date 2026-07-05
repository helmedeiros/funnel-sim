# 5. Emit via typed generic s3sink

## Status

Accepted — one generic `s3sink.Sink[T funnelevents.Event]` implementation instantiated twice at runtime (once for `SearchEvent`, once for `BookingEvent`). Each instance batches its own queue, partitions by the event's `Partition()` method, and writes to the funnel-events MinIO bucket independently.

## Context

funnel-sim emits two schemas: `search.v1` and `booking.v1`. Both need the same substrate posture (bounded queue, gzipped JSONL batches, Hive-partitioned object keys, bounded exponential backoff on failure, drop-with-metric on queue-full). Options for sharing the substrate:

1. **Two hand-rolled sinks, one per schema.** Explicit, no generics, ~250 LOC each = ~500 LOC total. Duplication is real, but every read is straightforward.
2. **One sink with `interface{}` at the queue boundary.** No generics, but every publish + serialize pays a type assertion. Runtime cost per event is tiny but honest overhead vs. option 3.
3. **Generic sink parameterized by an `Event` interface constraint.** One implementation, two instantiations, ~250 LOC + 5 LOC per event type. Type-safe at the boundary. Requires Go 1.18+ for generics.
4. **Move the sink into a shared library outside the two repos.** Cleanest long-term but a yak: creates a `github.com/helmedeiros/pricing-eventsink` module both markup-svc and funnel-sim would depend on. Deferred.

Go 1.18+ has stable generics. funnel-sim is a new module with no legacy constraint. The type-safety at the queue boundary matters — an `interface{}` queue means a compile-time-valid but semantically-wrong `Publish(searchEvent)` on a booking sink would slip through and produce garbage in the wrong partition.

Choice (3). This ADR.

## Decision

### The `Event` constraint

```go
type Event interface {
    SearchEvent | BookingEvent
    Partition() string
}
```

Union-typed constraint listing the two concrete event types. `Partition()` returns the Hive partition segment specific to the event: empty string for `search.v1` (single partition), `"event_type=booking.<state>"` for `booking.v1`.

### The generic Sink

`s3sink.Sink[T Event]` holds:
- One `chan T` bounded at `QueueSize` (default 10000). Non-blocking `Publish(e T)` via `select { case queue <- e: default: drop }`.
- One background goroutine draining the queue, batching by `Partition()`, flushing on time (`BatchEvery`, default 5m) or byte budget (`BatchSize`, default 10 MB).
- Bounded exponential backoff on upload (100ms / 400ms / 1.6s), with `minio.MaxRetry=1` to disable the SDK's own retry so this backoff is authoritative.
- Rate-limited structured log on buffer_full drops (5s quiet window) so on-call sees the early signal.

At runtime, cmd instantiates two sinks:

```go
searchSink := s3sink.New[funnelevents.SearchEvent](ctx, searchCfg, searchMetrics)
bookingSink := s3sink.New[funnelevents.BookingEvent](ctx, bookingCfg, bookingMetrics)
```

Each has its own queue, own goroutine, own metrics counters (labeled `event_family=search` / `event_family=booking`). No cross-contamination.

### Partition-keyed batching

Inside `run()`, batches are held in `map[string][]T` keyed by `Partition()`. On flush, each partition's batch becomes one gzipped JSONL object under the correct Hive path:

- `funnel-events/search-v1/dt=…/hour=…/env=…/instance=…/batch-<…>.jsonl.gz` (partition=`""`)
- `funnel-events/booking-v1/dt=…/hour=…/env=…/event_type=booking.initiated/instance=…/batch-<…>.jsonl.gz`
- `funnel-events/booking-v1/dt=…/hour=…/env=…/event_type=booking.reserved/instance=…/batch-<…>.jsonl.gz`
- `funnel-events/booking-v1/dt=…/hour=…/env=…/event_type=booking.purchased/instance=…/batch-<…>.jsonl.gz`
- `funnel-events/booking-v1/dt=…/hour=…/env=…/event_type=booking.timeout/instance=…/batch-<…>.jsonl.gz`

The booking sink partitions by event type so revenue queries hit only `event_type=booking.purchased/` (see ADR-0003).

### Non-blocking Publish contract

`Publish(e T)` MUST NOT block a caller. On queue-full it drops the event and increments `funnel_event_sink_dropped_total{reason=buffer_full}`. This preserves the funnel-sim /search + /booking/* handler latency guarantees under a saturated queue: the handler stays fast, observability sees the drop, ops decide whether to bump QueueSize.

### Graceful shutdown

On `ctx.Done()`, the goroutine drains the queue channel, batches whatever's left, and flushes with a fresh 10s-bounded context so SIGTERM during a rolling deploy doesn't strand events. `minio.MaxRetry=1` ensures the shutdown flush honors this bound and doesn't hang on SDK-internal retries.

## Consequences

### Positive
- One implementation, two instantiations. ~250 LOC covers both sinks. Type-safe at the queue boundary — no `interface{}`, no assertions, no runtime type surprises.
- Independent scaling. Each sink has its own queue and goroutine; a burst of booking events cannot starve the search sink. Each has its own metrics so /metrics shows both families cleanly.
- Same operational posture as markup-svc's s3sink. Ops muscle memory transfers: same buffer_full behavior, same backoff, same shutdown protocol.

### Negative
- Generics push the minimum Go version to 1.21 (chosen as the module's baseline). markup-svc still targets 1.18. When we eventually extract to a shared library (see Not-closed) either both bump to 1.21 or the library targets 1.18 and drops generics.
- Two goroutines instead of one. Each holds its own batch buffer (map + slices). Memory footprint doubles vs. a single unified sink; still bounded and small.
- Duplication with markup-svc's s3sink. The internals (queue, batching, retry, gzip) share ~80% of the code between markup-svc/internal/observability/decisionsink/s3sink and funnel-sim/internal/funnelevents/s3sink. Refactor to shared library deferred (see Not-closed).

### Not closed
- **Shared eventsink library.** Extract the common code path to `github.com/helmedeiros/pricing-eventsink`, depended on by both markup-svc and funnel-sim. Requires: aligning both modules on the same Go version, deciding whether the library owns the Event type constraint or the caller supplies it, publishing the library as a taggable module. Parked.
- **Multi-object partitioning by size.** Very large batches would benefit from splitting into multiple objects instead of one large gzip. Not needed at v1 traffic levels; deferred with an explicit trigger of "sustained single-flush objects > 100 MB in prod".
- **Per-sink backpressure signals to the handler.** A saturated queue currently drops silently. A future extension could surface queue depth in the HTTP handler's response headers so a well-behaved caller (funnel-sim's own aggregator, or a real BFF) can throttle. Deferred.

## References
- markup-svc ADR-0036 — decision-event substrate (MinIO / S3). The pattern this ADR copies with generics.
- funnel-sim ADR-0002 — search.v1 contract.
- funnel-sim ADR-0003 — booking.v1 contract with per-event-type partitioning.
