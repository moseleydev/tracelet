# Contributing to Tracelet

Thanks for helping make database performance visible. This guide covers how to set up the project, where to start, and what we expect from a pull request.

## Ground rules

- **Open an issue before large changes.** For a new database adapter, a new Studio view, or anything that changes `core` types, describe the plan first so nobody wastes work.
- **Small issues and fixes can go straight to a PR.**
- **The proxy is on the hot path.** Code in `proxy/` must never block on storage, analysis or the network. If you are unsure, ask in the issue.
- **Privacy by default.** Query parameters can contain personal data. They are redacted unless the user explicitly opts in. Do not log or store them otherwise.
- Be kind. This project follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Setup

You need Go 1.25+ and Node 20+.

```bash
git clone https://github.com/moseleydev/tracelet
cd tracelet
make test
make studio-install
make studio-dev
```

## Good first contribution: add a detector

Detectors turn query stats into recommendations. Each one is a single file in `detectors/` and needs no changes anywhere else.

1. Create `detectors/your_detector.go`:

   ```go
   package detectors

   import "strings"

   type selectStar struct{}

   func init() { Register(selectStar{}) }

   func (selectStar) Name() string { return "select-star" }

   func (selectStar) Detect(in Input) []Finding {
       q := strings.ToUpper(in.Stats.Fingerprint.Query)
       if !strings.HasPrefix(q, "SELECT * ") {
           return nil
       }
       return []Finding{{
           Detector:    "select-star",
           Severity:    Info,
           Title:       "Query selects every column",
           Explanation: "SELECT * transfers columns your code may never read.",
           Suggestion:  "List only the columns you need.",
       }}
   }
   ```

   Your detector receives an `Input` with:

   - `Stats`: calls, latency percentiles, rows and errors for one query shape. `Stats.Fingerprint` has the normalized query, its tables and an executable `Template`.
   - `ShareOfTime`: this query's fraction of all database time, which is good for setting severity.
   - `Plan`: the EXPLAIN plan tree (or nil), with `TableRows` and existing `Indexes` on every node that reads a table.
   - `Bursts` and `Parent`: back-to-back repeats on one connection, and the query that ran before them.

   See `missing_index.go`, `full_scan.go` and `n_plus_one.go` for complete examples.

2. Add `detectors/your_detector_test.go` with at least one case that fires and one that does not.
3. Run `make test` and open a PR.

Detector guidelines:

- **Explain in plain words.** The `Explanation` should make sense to someone who has never read a query plan.
- **Give a fix when you can.** If there is a concrete SQL fix, put it in `Fix`.
- **Avoid noise.** A detector that fires on everything gets ignored. Use thresholds based on `Stats` (count, p95, rows) so findings point at real problems.

## Bigger contributions

- **New database support.** Add a subpackage under `proxy/` that forwards traffic unchanged and emits `core.Event` values. Open an issue first; we will help with the design.
- **Studio views.** Live under `studio/src`. Include a screenshot or short recording in your PR.
- **SDKs.** Small helpers under `sdks/<language>` that add [sqlcommenter](https://google.github.io/sqlcommenter/) comments carrying the route and trace id.

## Pull requests

- Keep each PR focused on one change.
- Run `make test` and `make studio-lint` before pushing. CI runs both.
- Format Go with `gofmt`.
- Describe what changed and why. Link the issue it closes.

## Commit messages

Use a short imperative summary, optionally prefixed with the area:

```
detectors: add leading-wildcard LIKE detector
proxy/postgres: handle extended query protocol
```
