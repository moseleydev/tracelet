#!/usr/bin/env bash
# Measures what Tracelet costs an application: pgbench against PostgreSQL
# directly, then through `tracelet up`, interleaved so both see the same
# conditions. Prints a Markdown table.
#
#   DB=postgres://user:pass@localhost:5432/postgres bench/run.sh
#
# Needs pgbench and a built bin/tracelet (make build-go). Creates and drops
# a database named tracelet_bench.
set -euo pipefail

DB=${DB:?set DB to a PostgreSQL URL}
DURATION=${DURATION:-15}
ROUNDS=${ROUNDS:-3}
CLIENTS=${CLIENTS:-16}
RATE=${RATE:-2000}
TRACELET=${TRACELET:-./bin/tracelet}
PROXY_PORT=${PROXY_PORT:-6499}
STUDIO_PORT=${STUDIO_PORT:-4099}

base=${DB%/*}
bench_db="$base/tracelet_bench"
proxied="$(echo "$bench_db" | sed -E "s#@[^/]+/#@127.0.0.1:${PROXY_PORT}/#")"
data=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$data"; psql "$DB" -qc "DROP DATABASE IF EXISTS tracelet_bench" >/dev/null 2>&1 || true' EXIT

psql "$DB" -qc "DROP DATABASE IF EXISTS tracelet_bench" -c "CREATE DATABASE tracelet_bench" >/dev/null
pgbench -q -i -s 10 "$bench_db" >/dev/null 2>&1

"$TRACELET" up --db "$bench_db" --listen "127.0.0.1:$PROXY_PORT" --studio "127.0.0.1:$STUDIO_PORT" \
  --data "$data/t.db" --quiet --open=false >"$data/up.log" 2>&1 &
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$STUDIO_PORT/api/info" && break; sleep 0.2; done

saturated() { pgbench -S -M "$2" -c "$CLIENTS" -j 4 -T "$DURATION" "$1" 2>&1 | awk '/^tps/{printf "%.0f", $3}'; }
steady() { pgbench -S -M prepared -c 8 -j 4 -R "$RATE" -T "$DURATION" "$1" 2>&1 | awk '/latency average/{printf "%s", $4}'; }

echo "| Round | Workload | Direct | Via Tracelet |"
echo "|---|---|---|---|"
for r in $(seq 1 "$ROUNDS"); do
  echo "| $r | saturated, simple protocol ($CLIENTS clients) | $(saturated "$bench_db" simple) tps | $(saturated "$proxied" simple) tps |"
  echo "| $r | saturated, prepared ($CLIENTS clients) | $(saturated "$bench_db" prepared) tps | $(saturated "$proxied" prepared) tps |"
  echo "| $r | steady $RATE queries/s, average latency | $(steady "$bench_db") ms | $(steady "$proxied") ms |"
done
