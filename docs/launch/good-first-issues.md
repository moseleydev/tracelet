# Starter issues

Filed on GitHub as issues #5 to #18 at launch. Each one is self-contained and links to the code a contributor needs. Labels are in brackets.

---

## 1. Detector: unbounded queries [good first issue, detector]

Flag `SELECT` queries with no `LIMIT` that return many rows on average (`Stats.MeanRows`), for example over 1,000. They get slower as tables grow and usually mean a page is loading everything.

- **Where:** a new file `detectors/unbounded.go`. See `full_scan.go` for the pattern and `CONTRIBUTING.md` for a walkthrough.
- **Done when:** the detector has tests for a query that fires and one that doesn't, and suggests pagination (keyset pagination for large offsets).

## 2. Detector: `SELECT *` on wide tables [good first issue, detector]

Flag `SELECT *` (from `Stats.Fingerprint.Query`) when the scanned table has many columns or the query is hot. Suggest listing only the columns that are used. Keep it `Info` severity unless the query is a large share of database time (`Input.ShareOfTime`).

## 3. Detector: `OFFSET` pagination [good first issue, detector]

Flag fingerprints containing `OFFSET ?` whose p95 is high. Large offsets make PostgreSQL read and discard rows. Suggest keyset pagination (`WHERE id > $last ORDER BY id LIMIT n`), with an example in the explanation.

## 4. Detector: the same read twice in one request [good first issue, detector]

Using request traces (`Input.Trace` or the stored trace spans), flag a query shape that runs several times in one request but *not* back to back. The N+1 detector covers back-to-back repeats. Suggest caching the result for the duration of the request.

## 5. Detector: `UPDATE` or `DELETE` without a filter [good first issue, detector]

From the plan (`Input.Plan`), flag a `ModifyTable` node over a `Seq Scan` with no `Filter` on a large table. This is often a bug. Severity `Warning`, with an explanation of the risk.

## 6. Detector: implicit casts that defeat an index [detector]

When EXPLAIN shows a filter like `((orders.customer_id)::text = $1)`, the column is being cast, so its index can't be used. Detect the cast in `detectors/filters.go` and suggest fixing the parameter type in the application.

## 7. Detector: unused indexes [detector]

Have the analyzer read `pg_stat_user_indexes`, then flag large indexes with `idx_scan = 0` that aren't unique or primary keys. They slow down writes and use disk. This needs a small addition to `analyzer/explain.go` plus a new detector, and must stay read-only.

## 8. Node SDK: Knex integration [good first issue, sdk]

Add `instrumentKnex(knex)` to `sdks/node` so Knex queries are tagged, for example by wrapping the client's query method. Include tests with a fake client, like `instrumentPg`.

## 9. Python SDK: psycopg 3 without an ORM [good first issue, sdk]

Add `tracelet_sdk.psycopg.instrument(conn_or_pool)` that tags queries for apps that use psycopg directly. Mind `%` escaping: see `escape_percent` in `tracelet_sdk/__init__.py`.

## 10. Docker Compose example [good first issue, docs]

Add `examples/docker-compose.yml` that runs PostgreSQL, `tracelet up` and the demo shop together. That gives a one-command demo for people who don't have Go installed.

## 11. Studio: session timeline player [studio]

Recorded sessions (`.trc.gz`) can be replayed from the CLI. Add a Studio view that opens a session and scrubs through it on a timeline, showing queries per second and latency over time. See `core/session` for the format.

## 12. Studio: keyboard navigation for the topology [good first issue, studio, accessibility]

Let keyboard users move between topology nodes (for example arrow keys over a list of focusable elements layered on the canvas) and open details with Enter.

## 13. Fingerprinting: MySQL syntax [fingerprint]

Prepare for MySQL support: teach `core/fingerprint` backtick-quoted identifiers, `#` comments and `?` placeholders. Include tests in `fingerprint_test.go`.

## 14. More databases: MySQL proxy [help wanted, proxy]

A MySQL wire-protocol adapter under `proxy/mysql`, mirroring `proxy/postgres`: forward bytes unchanged, emit `core.Event`s, and never block the relay. Open a design discussion first.
