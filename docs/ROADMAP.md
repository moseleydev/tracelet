# Roadmap

Tracelet is built in phases. Each phase ends with something that runs. We open the project to outside contributors after Phase 3, when there is an end-to-end skeleton to build on.

## Phase 0: Foundations ✅

- [x] Monorepo layout, Go module, Studio scaffold
- [x] Core contracts: `core.Event`, `core.Stats`, detector plugin interface
- [x] License (Apache-2.0), README, CONTRIBUTING, Code of Conduct
- [x] CI: Go vet/test/build, Studio lint/build

## Phase 1: Capture engine (PostgreSQL) ✅

- [x] Transparent TCP proxy: the app behaves exactly as without it
- [x] Parse the Postgres wire protocol (simple and extended queries)
- [x] Emit events with query, duration, rows and errors
- [x] Non-blocking event channel; hot-path overhead under 0.5 ms (measured ~0.05 ms on localhost)
- [x] Redact parameters by default (`--capture-params` to opt in)
- [x] SSL to the database (libpq `sslmode` semantics) and cancel-request forwarding
- [x] `tracelet up` prints a live, color-coded query log

## Phase 2: Brain ✅

- [x] Fingerprint queries: literals become `?`, `IN (1, 2, 3)` and multi-row `VALUES` collapse to `(...)`
- [x] Aggregate per fingerprint in 10-second buckets: count, p50/p95/p99, max, total time, rows, errors
- [x] Local storage in SQLite (`.tracelet/tracelet.db`)
- [x] Track which queries touch which tables
- [x] `tracelet top` ranks queries by total time

Everything is pure Go, with no cgo, so Tracelet ships as a single binary for every OS. The fingerprinter is a tokenizer rather than a full SQL parser, and exact query plans come from `EXPLAIN` in Phase 4.

## Phase 3: Studio v1 (first public release)

- [ ] `tracelet up` starts the proxy and serves Studio on `:4000`
- [ ] Live event stream over WebSocket
- [ ] Live topology view: animated queries flowing to tables, colored by latency
- [ ] Query explorer ranked by total time
- [ ] Query detail: latency over time and sample queries
- [ ] Demo app in `examples/` that shows Tracelet in under a minute

## Phase 4: Analyzer and recommendations

- [ ] Automatic, read-only `EXPLAIN` for slow or frequent queries
- [ ] Plan visualizer with the costliest node highlighted
- [ ] Detectors: full scan, missing index, N+1, leading-wildcard `LIKE`, unbounded query, `SELECT *`, duplicate reads
- [ ] What-if index simulation with `hypopg`

## Phase 5: Request context

- [ ] Read route and trace id from sqlcommenter comments
- [ ] Node and Python SDKs
- [ ] Request waterfall view
- [ ] Trace-based N+1 detection

## Phase 6: Record, replay, simulate

- [ ] `tracelet record` writes a `.session` file
- [ ] Session player with a scrubbable timeline
- [ ] `tracelet replay` against staging only: writes rolled back, production hosts refused
- [ ] Load simulation (×10, ×100) with before/after comparison

## Phase 7: More databases

- [ ] MySQL
- [ ] MongoDB
- [ ] Redis
