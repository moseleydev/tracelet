# Launch posts (drafts)

Overhead numbers come from the Benchmark workflow on a clean Linux runner (pgbench, PostgreSQL 16, 3 rounds, 2026-10-19), not a laptop. Re-run it after proxy changes and update them.

---

## Show HN

**Title:** Show HN: Tracelet – see every query your app sends to Postgres, and how to make it faster

**Body:**

Hi HN! I built Tracelet because a basic CRUD app can still be slow, and most of us never see the queries our ORM actually sends: a search that scans the whole table, a page that runs 200 queries instead of 2, an index that isn't there.

Tracelet is a small proxy you put between your app and PostgreSQL. It works with any language or ORM, and you only change the port in your database URL. It opens a local UI that shows:

- **Live topology:** each query flows from the route that ran it to the tables it touches, colored by latency.
- **Queries ranked by total time (calls × latency).** A "fast" 20ms lookup that runs 200 times a page usually beats the slow query everyone blames.
- **Suggestions from EXPLAIN:** missing indexes (with the exact `CREATE INDEX`, including ORDER BY columns), full scans, and N+1 loops. With HypoPG installed, it estimates how much an index would help before you create it.
- **Request waterfalls,** via optional Node and Python SDKs that tag queries with their route (sqlcommenter format).
- **Record and replay:** capture real traffic, replay it against a staging copy at 10× load, and compare before and after an index. Replay can't write by accident: read mode uses read-only connections, and rollback mode rolls every write back.

The proxy is the part that matters most for not slowing anything down. It forwards bytes unchanged, never blocks on analysis, and drops statistics rather than queries if it falls behind. A bug in its parsing stops tracking for that one connection, not your traffic. On a clean Linux CI runner it adds about 0.13 ms per query at a steady 2,000 queries/s. Fully saturated on the same small machine, with pgbench, PostgreSQL and Tracelet competing for the same CPUs, it keeps about 70% of direct throughput (73% with prepared statements). The benchmark runs in CI, so you can check the numbers.

Literal values are never stored: Tracelet keeps query shapes (`WHERE email = ?`), not data. It's a single Go binary with the UI embedded, Apache-2.0.

Install: `curl -fsSL https://raw.githubusercontent.com/moseleydev/tracelet/main/install.sh | sh` or `brew install moseleydev/tap/tracelet`, then `tracelet up --db postgres://...`

Repo: https://github.com/moseleydev/tracelet

I'd love feedback, and contributors: new detectors are one file each, and there are starter issues. Questions and ideas are welcome in GitHub Discussions.

---

## X / Twitter thread

1/ Most slow apps aren't slow because of one bad query. They're slow because of a "fast" query that runs 200 times per page.

I built Tracelet to make that visible. Open source, works with any language. 🧵

[demo.gif]

2/ Point your app at the Tracelet proxy (change one port) and open Studio: every query flows from its route to its tables in real time, colored by latency.

3/ Queries are ranked by total time (calls × latency), not by how slow one call is. In the demo shop, a 20ms lookup was 52% of all database time.

4/ It runs EXPLAIN on your costliest queries and tells you what to do: the exact CREATE INDEX (with ORDER BY columns), trigram indexes for LIKE '%x%', and N+1 loops with the rewrite.

5/ Add the Node or Python SDK and you get request waterfalls. An N+1 loop looks like a staircase.

[requests.jpg]

6/ Record real traffic, replay it on staging at 10× load, add the suggested index, replay again:

37× faster on the slow query, and everything else 2–5× faster because the database stopped saturating.

7/ Built not to get in your app's way: bytes are forwarded unchanged, it never blocks on analysis, and it never stores literal values. About 0.13 ms per query in CI. Single Go binary, Apache-2.0.

brew install moseleydev/tap/tracelet
github.com/moseleydev/tracelet

---

## Reddit

**r/PostgreSQL:** "I built an open-source proxy that shows every query your app sends, suggests indexes from EXPLAIN (with HypoPG what-if estimates), and can replay recorded traffic against staging at 10× load"

**r/golang:** "Tracelet: a PostgreSQL wire-protocol proxy and live query visualizer in pure Go (no cgo). Notes on keeping a proxy's hot path cheap"
*(Write-up angle: replacing a per-query channel send with a batched buffer cut the proxy's overhead by more than half, and fuzzing found a crash in the comment parser in 1.4 seconds.)*

**r/node, r/django, r/FastAPI:** "See the SQL your ORM actually sends, as a per-request waterfall, with N+1 detection (one line of setup)"

Lead with the GIF, keep the body to a paragraph, and link the repo. Answer every comment in the first few hours.
