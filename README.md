# Tracelet

**See every query your app sends to its database: how long it took, how often it runs, and how to make it faster.**

![Tracelet Studio: live queries flowing from each route to its tables, colored by latency, with issues flagged](docs/demo.gif)

Tracelet sits between your application and your database as a lightweight proxy. It works with any language or framework, with no code changes. Tracelet Studio then shows your traffic as a live, animated map: slow queries glow red, N+1 loops show up as staircases, and full-table scans come with the exact index that fixes them.

> **Status: v0.1.0, early but usable.** PostgreSQL is supported today. Tracelet is built in the open: see the [roadmap](docs/ROADMAP.md) for what is next, and the [good first issues](https://github.com/moseleydev/tracelet/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) if you would like to help.

## Why

A simple CRUD app can still be slow. A search that scans the whole table, a page that fires 200 queries instead of 2, or a missing index can each add hundreds of milliseconds, and most of us never see the query our ORM actually generates. Tracelet makes that visible.

## How it works

```
your app ──► tracelet proxy (:6433) ──► your database (:5432)
                    │
                    ▼
           fingerprint · aggregate · EXPLAIN · detect
                    │
                    ▼
            Tracelet Studio (:4000)
```

1. **Capture.** A wire-protocol proxy records each query, its duration, row count and errors. It adds well under a millisecond of overhead.
2. **Understand.** Queries are grouped by shape (`WHERE email = ?`) and ranked by total time spent.
3. **Recommend.** Detectors inspect stats and query plans to flag full scans, missing indexes, N+1 patterns and more, each with a suggested fix.
4. **Visualize.** Tracelet Studio shows it all live, and lets you record, replay and simulate traffic.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/moseleydev/tracelet/main/install.sh | sh
```

You can also run `brew install moseleydev/tap/tracelet`, or download a binary for macOS, Linux or Windows from [Releases](https://github.com/moseleydev/tracelet/releases). Each one is a single file with no dependencies, and Studio is built in.

## Quick start

```bash
tracelet up --db postgres://user:pass@localhost:5432/app
```

Change your app's database port from `5432` to `6433` and use your app as usual. Tracelet Studio opens at <http://localhost:4000>:

- **Live topology:** every query flows from your app to the tables it touches, as a dot colored by its latency. Query nodes glow with traffic and are ranked by total time.
- **Queries:** every query shape ranked by total time (calls × latency), with its share of all database time, p95, max, rows and errors.
- **Requests:** with the [SDK](sdks) (Node or Python, one line of setup), queries are grouped by request. You see routes ranked by database time, and a waterfall of every query a request ran, where N+1 loops show up as a staircase.

![A request waterfall: one query, then the same lookup 20 times in a row, an N+1 loop](docs/requests.jpg)
- **Insights:** suggestions ranked by how much time they would save, each with a plain explanation and the exact fix: a missing index, a full table scan, an N+1 loop.
- **Query detail:** p50/p95 latency over time, calls over time, the query plan, and the last error.

![Tracelet Insights suggesting a missing index with the exact CREATE INDEX statement](docs/insights.jpg)

Similar queries are grouped by shape, so 200 lookups with different ids count as one query. The terminal shows the same data:

```
    TOTAL  CALLS    MEAN     P95      MAX  ROWS  ERRORS
    4.00s    200  20.0ms  25.6ms  525.4ms     1       -  SELECT * FROM orders WHERE id = ?
  589.1ms     30  19.6ms  21.2ms   22.8ms     3       -  SELECT id, amount FROM orders WHERE id IN (...)
```

### How suggestions work

Every 15 seconds Tracelet runs `EXPLAIN` on the costliest queries to see how PostgreSQL executes them, then checks the plans and traffic patterns:

| Detector | Finds | Suggests |
|---|---|---|
| `missing-index` | Large tables scanned to find a few rows | The exact `CREATE INDEX CONCURRENTLY`, including ORDER BY columns |
| `full-scan` | Wildcard searches, functions on columns, unfiltered reads of big tables | A trigram or expression index, or how to narrow the query |
| `n-plus-one` | The same query repeated back to back, once per row of a previous query | One query with `= ANY($1)`, a JOIN, or eager loading |

**What-if estimates.** If the database has the [HypoPG](https://github.com/HypoPG/hypopg) extension, Tracelet tests each suggested index before you create it. It adds the index as a *hypothetical* index, which exists only in the query planner for one connection and is never built, plans the query again, and shows the estimated reduction in work (for example "~674× less work") and whether PostgreSQL would use the index at all. Suggestions the planner would ignore are downgraded. To enable it, install the HypoPG package for your PostgreSQL version and run `CREATE EXTENSION hypopg;`. Tracelet never installs extensions itself. Planner cost is an estimate, and measured speedups are usually smaller, because every call also has fixed costs such as the network round trip.

`EXPLAIN` plans a query without running it, so this is safe even for UPDATE and DELETE. See [SECURITY.md](SECURITY.md) for Tracelet's full safety and security model. Tracelet also uses a read-only transaction with a 2-second timeout and its own connection, which never appears in your stats. Turn it off with `--analyze=false`.

### Record, replay and simulate

Record real traffic, then replay it against a staging copy to see how a change performs before it reaches production:

```bash
tracelet up --db postgres://localhost:5432/app --record      # or press Record in Studio
tracelet replay .tracelet/sessions/<session>.trc.gz --target postgres://localhost:5432/app_staging
```

- **Exact.** Sessions capture each query at the wire-protocol level, binary parameters included, so a replay sends exactly what your app sent, whatever its language or ORM.
- **Safe.**
  - `--mode read` (the default) uses read-only connections: PostgreSQL itself refuses writes.
  - `--mode rollback` replays writes inside transactions that are always rolled back.
  - Replay never writes to the database a session was recorded from.
- **Simulate load.** `--scale 10` runs ten copies of every connection, and `--speed 0` sends queries as fast as possible.
- **Compare.** `--save before.json`, change something (add an index), then `--compare before.json`:

```
  CALLS  RECORDED p95  PREVIOUS p95  REPLAY p95   VS PREVIOUS
    465         1.8ms       129.4ms       3.5ms  37.4x faster  SELECT id, amount, status FROM shop_orders WHERE customer_id = ? …
   4100        0.24ms         6.7ms       1.2ms   5.6x faster  SELECT email FROM shop_customers WHERE id = ?
```

Session files contain real query values, unlike everything else Tracelet stores, so treat them like a database dump. Recording costs nothing until you turn it on.

Stats are saved to `.tracelet/tracelet.db`. Browse them later with `tracelet studio` or `tracelet top`. Literal values and parameters are never saved or sent to the browser unless you pass `--capture-params`.

### Try it with the demo shop

The demo simulates an online store with realistic problems: a missing index, an N+1 loop, an unindexed aggregate, wildcard search and a broken query.

```bash
tracelet up --db postgres://localhost:5432/postgres
go run ./examples/shop --db postgres://localhost:6433/postgres
```

## Supported databases

| Database   | Status        |
|------------|---------------|
| PostgreSQL | Supported     |
| MySQL      | Planned       |
| MongoDB    | Planned       |
| Redis      | Planned       |

## Repository layout

| Path          | What lives there                                         |
|---------------|----------------------------------------------------------|
| `cmd/tracelet`| The `tracelet` CLI                                        |
| `proxy/`      | Wire-protocol proxies, one subpackage per database       |
| `core/`       | Shared types, fingerprinting, aggregation, SQLite storage |
| `detectors/`  | Recommendation rules, one file per detector              |
| `studio/`     | Tracelet Studio (React + Vite)                            |
| `sdks/`       | Node and Python SDKs that tag queries with route and trace |
| `examples/`   | Demo apps with deliberately slow queries                 |
| `docs/`       | Roadmap and design docs                                  |

## Development

You need Go 1.25+ and Node 20+.

```bash
make test        # Go tests (set TRACELET_TEST_POSTGRES_URL to include integration tests)
make build       # builds Tracelet Studio and bin/tracelet with the UI embedded
make build-go    # builds bin/tracelet only (no Node needed)
make studio-dev  # runs Tracelet Studio with hot reload against a running `tracelet up`
```

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) to get started. Adding a new detector is the easiest first contribution.

## License

[Apache License 2.0](LICENSE)
