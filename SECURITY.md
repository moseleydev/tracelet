# Security

## Reporting a vulnerability

Please report security problems privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Don't open a public issue. You'll get a reply within a few days, and a fix and advisory once the issue is confirmed.

## Security model

Tracelet sits between your application and your database, so it is built to fail safe.

**Your application's traffic**
- **Bytes are forwarded unchanged.** Tracelet never rewrites queries or results.
- **Bugs stay contained.** If Tracelet hits an internal error while reading a connection's messages, it stops tracking that connection and keeps relaying it. An unusual query can't take down other connections.
- **Bounded memory.** Messages are inspected from their first 1 MB at most; anything larger is streamed through without buffering.
- **Never blocks the app.** When Tracelet can't keep up, it drops statistics, not queries.
- **Encryption.** The link between your app and Tracelet is plain TCP and is meant to stay on the same machine; Tracelet warns if `--listen` is not a local address. The link from Tracelet to the database supports TLS with libpq `sslmode` semantics.

**Your data**
- **No literal values by default.** Tracelet stores query *shapes* (`WHERE email = ?`), not values. Parameters, raw query text and values quoted in error messages are kept only with `--capture-params`.
- **Private data file.** `.tracelet/tracelet.db` is readable only by its owner (0600, directory 0700).
- **Studio has no login.** It serves on `127.0.0.1` only, refuses other addresses unless you pass `--studio-public`, and rejects requests whose `Host` isn't local, which blocks DNS-rebinding attacks from web pages. Requests that change state must come from Studio itself (Origin check).
- **Keep passwords out of `ps`.** Use `TRACELET_DB_URL` instead of `--db`.

**Your database**
- **Plain EXPLAIN only.** The analyzer never runs `EXPLAIN ANALYZE`, so analyzed queries are planned, never executed. Each explain runs in a read-only transaction with a 2-second statement timeout and a 100 ms lock timeout, on its own connection.
- **Hypothetical indexes only.** What-if simulations use HypoPG hypothetical indexes, which exist only in the planner's memory and are removed straight away. Tracelet never creates indexes or installs extensions; suggested fixes are shown for you to run.
- **Replay never touches production by accident.** It refuses to write to a database unless you name it explicitly and confirm (see `tracelet replay -h`).

## Dependencies

CI runs on a pinned, current Go toolchain. Before each release, run `govulncheck ./...` and `npm audit` in `studio/`.
