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

## Phase 3: Studio v1 ✅

- [x] `tracelet up` starts the proxy and serves Studio on `:4000`; `tracelet studio` browses saved data
- [x] Live event stream over Server-Sent Events, batched every 100 ms and sampled under heavy load
- [x] Live topology view: animated queries flowing to tables, colored by latency
- [x] Query explorer ranked by total time, with share of time, sorting and filtering
- [x] Query detail: p50/p95 latency over time, calls over time, last error
- [x] Demo shop in `examples/shop` with a missing index, an N+1 loop and a broken query
- [x] Failed prepares and binds are reported as errors (drivers like pgx prepare before executing)

The UI is embedded in the Go binary and has no chart or animation dependencies: the topology is a 2D canvas, the charts are SVG. Light and dark themes follow the OS, and it works down to phone width.

**Next:** open the repository to contributors (see "Before going public" below), then Phase 4.

### Before going public

- [ ] Add a contact address to CODE_OF_CONDUCT.md
- [ ] Release binaries for macOS, Linux and Windows
- [ ] Label 10–15 `good first issue`s (new detectors are ideal)

## Phase 4: Analyzer and recommendations (in progress)

- [x] Automatic `EXPLAIN` of the costliest queries: generic plans, read-only transaction, never executed
- [x] Query templates: literals become `$n` parameters, so plans need no stored values
- [x] Plan tree in the query detail, with full table scans flagged
- [x] Detectors: missing index (with ORDER BY columns), full scan (leading-wildcard `LIKE`, functions on columns, unfiltered reads), N+1 (back-to-back repeats per connection)
- [x] Insights tab, issue badges in the topology and query list, suggestions in `tracelet top`
- [x] Findings clear themselves once fixed (plans refresh every 2 minutes)
- [ ] More detectors (good first issues): unbounded query, `SELECT *`, duplicate reads, `OFFSET` pagination, unused indexes
- [x] What-if index simulation with HypoPG: estimated reduction in work, and whether the planner would use the index, before creating it (btree, hash and brin; GIN/GiST can't be simulated)

## Phase 5: Request context ✅

- [x] Read route and trace id from sqlcommenter comments (`route`, `traceparent`, or `controller` + `action`)
- [x] Group queries into requests; per-route stats (requests, queries per request, DB time per request)
- [x] Node SDK (`tracelet-sdk`): Express middleware, node-postgres, `comment()`
- [x] Python SDK (`tracelet-sdk`): Django, Flask, ASGI/FastAPI, WSGI, SQLAlchemy, `comment()`
- [x] Requests tab with per-request waterfalls; routes as sources in the live topology
- [x] Trace-based N+1 detection, which works even when every query uses a different pooled connection
- [x] Shared sqlcommenter test cases across Go, Node and Python
- [ ] Publish the SDKs to npm and PyPI
- [ ] More integrations (Prisma, Knex, psycopg without an ORM, Rails via existing sqlcommenter gems)

## Phase 6: Record, replay, simulate ✅

- [x] Record from Studio or `tracelet up --record`: wire-exact sessions (`.trc.gz`), zero cost when not recording, size-capped, owner-only files
- [x] `tracelet replay`:
  - read mode on read-only connections;
  - rollback mode where every write is rolled back;
  - refuses to write to the recorded database
- [x] Load simulation (`--scale`, `--speed`) with per-query comparison against the recording
- [x] Before/after comparison (`--save`, `--compare`)
- [ ] Session player with a scrubbable timeline in Studio

## Phase 7: More databases

- [ ] MySQL
- [ ] MongoDB
- [ ] Redis
