# tracelet-sdk (Node.js)

Tags each database query with the request that ran it, so [Tracelet](https://github.com/moseleydev/tracelet) can show per-request waterfalls and catch N+1 loops. Zero dependencies. Node 16.4+.

```bash
npm install tracelet-sdk
```

## Express + node-postgres

```js
const express = require('express')
const pg = require('pg')
const tracelet = require('tracelet-sdk')

tracelet.instrumentPg(pg) // before creating pools or clients

const app = express()
app.use(tracelet.middleware()) // as early as possible
```

Every query made while handling a request now carries the route pattern (`GET /users/:id`) and a trace id. An incoming `traceparent` header is continued, so traces line up with OpenTelemetry. Raw URLs are never used, so ids and other values in paths don't end up in your stats.

## Other frameworks and drivers

Wrap the work of one request in `withRequest`, and tag SQL with `comment()`:

```js
const { withRequest, comment } = require('tracelet-sdk')

await withRequest({ route: 'POST /import' }, async () => {
  await db.raw(comment('SELECT * FROM jobs WHERE id = ?'), [id])
})
```

`route` can also be a function, evaluated when each query runs. Context survives `await`.

## Notes

- **Named statements:** queries that use pg's named prepared statements (`{ name: '…' }`) are left untagged, because pg requires a named statement's text to stay the same.
- **Existing comments:** SQL that already contains a comment is left unchanged, as the sqlcommenter spec requires.
