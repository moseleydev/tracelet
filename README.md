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

> Not usable yet. This is the target experience for the first release.

```bash
tracelet up --db postgres://user:pass@localhost:5432/app
```

Change your app's database port from `5432` to `6433`, use your app as usual, and open <http://localhost:4000>.

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
| `core/`       | Shared types, fingerprinting, aggregation, storage       |
| `detectors/`  | Recommendation rules, one file per detector              |
| `studio/`     | Tracelet Studio (React + Vite)                            |
| `sdks/`       | Optional helpers that tag queries with route and trace   |
| `examples/`   | Demo apps with deliberately slow queries                 |
| `docs/`       | Roadmap and design docs                                  |

## Development

You need Go 1.25+ and Node 20+.

```bash
make test        # Go tests
make build       # builds bin/tracelet
make studio-dev  # runs Tracelet Studio with hot reload
```

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) to get started. Adding a new detector is the easiest first contribution.

## License

[Apache License 2.0](LICENSE)
