# Tracelet

**See every query your app sends to its database: how long it took, how often it runs, and how to make it faster.**

Tracelet sits between your application and your database as a lightweight proxy. It works with any language or framework, with no code changes. Tracelet Studio then shows your traffic as a live, animated map: slow queries glow red, N+1 loops show up as staircases, and full-table scans come with the exact index that fixes them.

> **Status: early development.** Tracelet is being built in the open. See the [roadmap](docs/ROADMAP.md) for what works today and what is next.

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

## Quick start

```bash
make build
./bin/tracelet up --db postgres://user:pass@localhost:5432/app
```

Change your app's database port from `5432` to `6433` and use your app as usual. Every query appears in your terminal with its latency and row count:

```
16:06:10.277   152.24ms      1 rows  SELECT count(*) FROM orders WHERE email LIKE '%abc%';
16:06:10.434    16.03ms      1 rows  SELECT * FROM orders WHERE id = 42;
16:06:10.450     1.29ms  ERROR      SELECT * FROM no_such_table;
```

Stop it with Ctrl+C to see which queries cost the most time. Similar queries are grouped by shape, so 200 lookups with different ids count as one query:

```
    TOTAL  CALLS    MEAN     P95      MAX  ROWS  ERRORS
    4.00s    200  20.0ms  25.6ms  525.4ms     1       -  SELECT * FROM orders WHERE id = ?
  589.1ms     30  19.6ms  21.2ms   22.8ms     3       -  SELECT id, amount FROM orders WHERE id IN (...)
  175.6ms      5  35.1ms  36.7ms   36.7ms     1       -  SELECT count(*) FROM orders WHERE email LIKE ?
```

Stats are saved to `.tracelet/tracelet.db`, so you can run `tracelet top` at any time. Literal values and parameters are never saved unless you pass `--capture-params`.

Tracelet Studio, the visual interface, arrives in Phase 3.

## Supported databases

| Database   | Status        |
|------------|---------------|
| PostgreSQL | In progress   |
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
| `sdks/`       | Optional helpers that tag queries with route and trace   |
| `examples/`   | Demo apps with deliberately slow queries                 |
| `docs/`       | Roadmap and design docs                                  |

## Development

You need Go 1.25+ and Node 20+.

```bash
make test        # Go tests (set TRACELET_TEST_POSTGRES_URL to include integration tests)
make build       # builds bin/tracelet
make studio-dev  # runs Tracelet Studio with hot reload
```

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) to get started. Adding a new detector is the easiest first contribution.

## License

[Apache License 2.0](LICENSE)
