# Changelog

All notable changes to this project are documented in this file.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) + [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial scaffold: five HTTP endpoints (`/search`, `/booking/init`, `/booking/reserve`, `/booking/purchase`, `/booking/timeout`), in-memory booking state machine with terminal `phase`-carrying timeout, generic `s3sink.Sink[T funnelevents.Event]` writing `search.v1` and `booking.v1` batches to a MinIO / S3-compatible bucket. ADRs 0001-0005 pin the role of the service, both event schemas, the ephemeral state posture, and the typed-sink shape. Observability wired end-to-end: Prometheus (`funnel_sim_*` + `funnel_event_sink_*`), structured JSON logs matching the platform Filebeat contract, OTel spans propagating the W3C traceparent so decision-gateway → funnel-sim → markup-svc renders as one Jaeger waterfall. Compose integration behind a `build:` context in the sibling `decision-gateway/docker-compose.yaml`.
