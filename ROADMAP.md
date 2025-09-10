# Showoff Roadmap & Pre-Kubernetes Review

This document aggregates proposed improvements before (and shortly after) deploying the server to Kubernetes. Items are grouped and prioritized. Initial focus should be on Stability, Security, and Observability.

Legend: P1 = must-have pre-K8s, P2 = soon after, P3 = nice-to-have / later.

## 1. Stability & Correctness

| Priority | Item | Notes |
|----------|------|-------|
| P1 | Graceful shutdown | Catch SIGINT/SIGTERM, stop new accepts, drain ongoing tunnels with timeout. |
| P1 | Replace spin-wait with signaling | Use a channel or sync.Cond per pending request instead of 25ms polling. |
| P1 | Strong random IDs | Use `crypto/rand` base62 (length ≥ 20) instead of `math/rand`. |
| P1 | Header read robustness | Loop until full HTTP headers (CRLF CRLF) or size cap; handle partial first read. |
| P1 | Enforce max header size (server) | Mirror client rewrite cap; reject > configured limit (e.g. 32KB). |
| P1 | Pending cleanup loop | Periodically expire stalled pending requests (configurable timeout). |
| P2 | Idle tunnel timeout | Close inactive long-lived tunnels to reclaim resources. |
| P1 | Backpressure / streaming improvements | Avoid buffering large bodies; stream after minimal header parse. |
| P2 | Context-aware deadlines | Set per-connection read/write deadlines to prevent hung goroutines. |
| P3 | Connection pooling to targets | Optional keep-alive pool if many short requests. |

## 2. Security & Hardening

| Priority | Item | Notes |
|----------|------|-------|
| P2 | TLS termination | Either native TLS on control/data/public or documented reverse proxy setup. |
| P3 | Per-client authentication tokens | Unique token per client instead of single shared secret. |
| P2 | Increase ID entropy | Crypto-strength to prevent guessing / hijacking pending tunnels. |
| P2 | Sanitize Host header | Reject multiple Host headers and malformed values. |
| P2 | Heartbeat / keepalive | Ping/pong on control channel for faster dead-client detection. |
| P2 | Rate limiting | Global + per-client connection & request rate caps. |
| P2 | Optional name allowlist / pattern | Restrict which public names can be registered. |
| P2 | Authorization plugin interface | Allow custom auth (JWT/HMAC/OPA). |
| P3 | mTLS between client/server | Strong mutual identity. |
| P3 | PROXY protocol / X-Forwarded-For support | Preserve original source IP if behind LB. |

## 3. Observability

| Priority | Item | Notes |
|----------|------|-------|
| P1 | Structured logging | JSON logs with levels (info/debug/error). |
| P1 | Prometheus metrics | Active clients, pending tunnels, tunnel durations, errors, bytes transferred. |
| P1 | Health endpoint (/healthz) | Liveness: listeners bound. |
| P1 | Readiness endpoint (/readyz) | Ready after control/public/data listeners and metric exporter up. |
| P2 | pprof endpoint (guarded) | Performance diagnostics. |
| P2 | Metrics: per-client counters | Requests, failures, bytes. |
| P3 | Tracing (OpenTelemetry) | Control + data tunnel spans. |

## 4. Operational Config & Packaging

| Priority | Item | Notes |
|----------|------|-------|
| P1 | Environment variable config | Mirror flags (e.g. SHOWOFF_CONTROL_ADDR). |
| P1 | Dockerfile (multi-stage, distroless) | For containerization. |
| P1 | Basic Helm chart / Kustomize | Service, Deployment, ConfigMap, Secret (tokens), probes. |
| P1 | Version/build info | `--version` flag & /version endpoint. |
| P2 | Config file (YAML/JSON) | Alternative to many flags. |
| P2 | Resource limits guidance | CPU/memory defaults in chart. |
| P3 | Auto TLS (ACME) | Integrated cert fetch. |

## 5. Protocol & Feature Enhancements

| Priority | Item | Notes |
|----------|------|-------|
| P1 | Preserve & forward original request immediately | Ensure zero request loss / immediate streaming. (Partially done.) |
| P2 | Multiplex multiple tunnels | Use yamux/HTTP2/QUIC to reuse a single data connection per client. |
| P2 | Websocket / raw TCP mode | Support non-HTTP protocols cleanly. |
| P2 | Server-side Host rewrite options | Mirror client stripping logic for centralized config. |
| P3 | UDP tunneling | Possibly via QUIC or separate channel. |
| P3 | Web UI / status dashboard | Display active clients & metrics. |

## 6. Reliability & Scaling

| Priority | Item | Notes |
|----------|------|-------|
| P2 | Limit simultaneous pending requests per client | Avoid resource exhaustion. |
| P2 | Global cap on clients / tunnels | Configurable maxima with backoff. |
| P2 | Horizontal scaling plan | Strategy: sticky hashing for names or shared state (Redis/NATS). |
| P2 | State abstraction layer | Interface for registry to enable future clustering. |
| P3 | Distributed coordination | Shared pub/sub for request dispatch in multi-instance cluster. |

## 7. Code Quality & Testing

| Priority | Item | Notes |
|----------|------|-------|
| P1 | Unit tests: host parsing, auth, timeout | Baseline correctness. |
| P1 | Integration test harness | End-to-end request flow validation. |
| P1 | Lint / static analysis (golangci-lint) | Enforce style & catch issues. |
| P2 | Fuzzing (header parser, protocol) | Detect edge-case panics. |
| P2 | Benchmark tunnel latency | Baseline performance metrics. |
| P3 | Chaos tests (drop control link) | Resilience verification. |

## 8. Performance Optimizations (Later)

| Priority | Item | Notes |
|----------|------|-------|
| P2 | Buffer pooling (sync.Pool) | Reduce allocations for initial read buffers. |
| P2 | Reduce goroutine churn | Reuse workers for accept/proxy loops. |
| P3 | QUIC transport option | Potential latency improvements, multiplexing. |

## 9. Edge Cases / Bug Risks

| Risk | Mitigation |
|------|-----------|
| Duplicate client names causing denial | Allow replace with flag or reject + metric. |
| Stale pending requests leak | Implement timeout sweep (P1). |
| Multiple Host headers / header smuggling | Strict single Host enforcement (P1). |
| Partial header read mis-parse | Loop until CRLFCRLF or size cap (P1). |
| Predictable request IDs leading to hijack | Switch to crypto/rand (P1). |
| Dead client detected late | Heartbeat + shorter read deadlines (P2). |

## 10. Suggested Minimal Pre-K8s Scope (Slice from above)

1. Strong random IDs & authentication improvements.
2. Graceful shutdown & health/readiness endpoints.
3. Structured logging + Prometheus metrics.
4. Pending signaling + cleanup loop.
5. Header parsing robustness + size limits.
6. Dockerfile & Helm chart.

## 11. Implementation Order (Sprint Outline)

Sprint 1: IDs, header robustness, graceful shutdown, pending signaling.

Sprint 2: Auth improvements, metrics, health/readiness, Dockerfile, Helm chart.

Sprint 3: Heartbeats, rate limiting, structured logging refactor, timeout policies.

Sprint 4: Multiplexing design spike, TLS integration decision (native vs proxy), clustering strategy doc.

## 12. Open Questions

* Do we require multi-tenant isolation soon (namespacing / separate auth domains)?
* Is TLS termination preferred inside the binary or delegated to ingress (e.g., nginx / Traefik / Caddy)?
* Expected peak tunnels/sec & concurrency to size resource limits?
* Need persistent audit logs of connections (compliance)?

---

Feel free to annotate this file with decisions or move accepted items into a CHANGELOG once delivered.
