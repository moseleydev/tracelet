# Roadmap

Tracelet is built in phases. Each phase ends with something that runs. We open the project to outside contributors after Phase 3, when there is an end-to-end skeleton to build on.

## Phase 0: Foundations ✅

- [x] Monorepo layout, Go module, Studio scaffold
- [x] Core contracts: `core.Event`, `core.Stats`, detector plugin interface
- [x] License (Apache-2.0), README, CONTRIBUTING, Code of Conduct
- [x] CI: Go vet/test/build, Studio lint/build

## Phase 1: Capture engine (PostgreSQL)

- [ ] Transparent TCP proxy: the app behaves exactly as without it
- [ ] Parse the Postgres wire protocol (simple and extended queries)
- [ ] Emit events with query, duration, rows and errors
- [ ] Non-blocking event channel; hot-path overhead under 0.5 ms
- [ ] Redact parameters by default

## Phase 2: Brain

- [ ] Fingerprint queries with `pg_query_go`
- [ ] Aggregate per fingerprint: count, p50/p95/p99, total time, rows
- [ ] Local storage (DuckDB or SQLite)
- [ ] Track which queries touch which tables

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
