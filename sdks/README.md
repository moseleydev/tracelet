# SDKs

Tracelet works without any SDK: point your app at the proxy and every query is captured. The SDKs add one thing, **request context**. Each query gets a [sqlcommenter](https://google.github.io/sqlcommenter/) comment with the route and a W3C trace id:

```sql
SELECT * FROM users WHERE id = $1 /*route='GET%20%2Fusers%2F%3Aid',traceparent='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'*/
```

With that, Tracelet Studio shows:

- **Requests:** routes ranked by database time, with queries and DB time per request.
- **Waterfalls:** every query a single request ran, in order and to scale, so an N+1 loop shows up as a staircase.
- **Routes in the live topology:** queries flow from the route that ran them.
- **Exact N+1 detection:** loops are found within each request, even when the app uses a different pooled connection for every query.

Nothing is sent anywhere. The comment travels with the query to your database, and the proxy reads it.

| Language | Package | Covers |
|---|---|---|
| Node.js | [`tracelet-sdk`](node) | Express middleware, node-postgres, any driver via `comment()` |
| Python | [`tracelet-sdk`](python) | Django, Flask, FastAPI/ASGI, WSGI, SQLAlchemy, any driver via `comment()` |

Any existing sqlcommenter or OpenTelemetry integration that sends `route` and `traceparent` (or `controller` and `action`) works too, for example Rails, Laravel or Spring.

## Compatibility

All SDKs and the Go parser test against the same cases in [`testdata/sqlcommenter.json`](testdata/sqlcommenter.json). A new SDK should do the same.
