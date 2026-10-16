'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const sdk = require('..')

const vectors = require(path.join(__dirname, '../../testdata/sqlcommenter.json'))

test('writes the shared sqlcommenter test cases byte for byte', () => {
  for (const c of vectors.cases) {
    assert.equal(sdk.comment(c.sql, c.fields), c.commented, c.name)
  }
})

test('comment() without fields uses the current request, and is a no-op outside one', async () => {
  assert.equal(sdk.comment('SELECT 1'), 'SELECT 1')
  const tp = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
  await sdk.withRequest({ route: 'GET /a', traceparent: tp }, async () => {
    await new Promise((r) => setTimeout(r, 5)) // context survives awaits
    assert.equal(sdk.comment('SELECT 1'), `SELECT 1 /*route='GET%20%2Fa',traceparent='${tp}'*/`)
  })
})

test('traceparent: parses valid headers, rejects invalid ones, continues incoming traces', () => {
  assert.deepEqual(sdk.parseTraceparent('00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'), {
    version: '00',
    traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
    parentId: '00f067aa0ba902b7',
    flags: '01',
  })
  for (const bad of [undefined, '', 'nope', '00-00000000000000000000000000000000-00f067aa0ba902b7-01', 'ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01']) {
    assert.equal(sdk.parseTraceparent(bad), null, String(bad))
  }
  const fresh = sdk.parseTraceparent(sdk.newTraceparent())
  assert.ok(fresh)
  const continued = sdk.parseTraceparent(sdk.newTraceparent('00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00'))
  assert.equal(continued.traceId, '4bf92f3577b34da6a3ce929d0e0e4736')
  assert.notEqual(continued.parentId, '00f067aa0ba902b7') // a new span for this request
  assert.equal(continued.flags, '00')
})

test('withRequest generates a traceparent when none is given', () => {
  sdk.withRequest({ route: '/x' }, () => {
    assert.ok(sdk.parseTraceparent(sdk.currentRequest().traceparent))
  })
})

test('middleware reads the Express route pattern lazily, never the raw URL', async () => {
  const mw = sdk.middleware()
  const req = { method: 'GET', url: '/users/42?token=secret', headers: {}, baseUrl: '/api' }
  await new Promise((resolve) => {
    mw(req, {}, () => {
      // Before routing: no route yet, but the request is traced.
      assert.match(sdk.comment('SELECT 1'), /^SELECT 1 \/\*traceparent='00-[0-9a-f]{32}-[0-9a-f]{16}-01'\*\/$/)
      req.route = { path: '/users/:id' } // Express sets this when a route matches
      const sql = sdk.comment('SELECT 1')
      assert.match(sql, /route='GET%20%2Fapi%2Fusers%2F%3Aid'/)
      // The raw path and query string never leak. (Don't match a bare "42":
      // the random trace id can contain those digits.)
      assert.doesNotMatch(sql, /users%2F42|secret/)
      resolve()
    })
  })
})

test('middleware continues an incoming traceparent', async () => {
  const mw = sdk.middleware()
  const req = { method: 'GET', headers: { traceparent: '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' } }
  await new Promise((resolve) =>
    mw(req, {}, () => {
      assert.match(sdk.comment('SELECT 1'), /traceparent='00-4bf92f3577b34da6a3ce929d0e0e4736-/)
      resolve()
    }),
  )
})

test('instrumentPg tags string and config queries, skips named statements, and patches once', async () => {
  const calls = []
  class Client {
    query(config, values, cb) {
      calls.push({ config, values, cb })
      return 'result'
    }
  }
  const pg = { Client }
  assert.equal(sdk.instrumentPg(pg), pg)
  sdk.instrumentPg(pg) // idempotent

  const client = new Client()
  const shared = { text: 'SELECT * FROM t WHERE id = $1', values: [1] }
  await sdk.withRequest({ route: '/r', traceparent: '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' }, async () => {
    const cb = () => {}
    assert.equal(client.query('SELECT 1', [], cb), 'result')
    client.query(shared)
    client.query({ name: 'stmt', text: 'SELECT 2' })
    calls[0].cbSeen = calls[0].cb === cb
  })
  client.query('SELECT 3') // outside a request: unchanged

  assert.match(calls[0].config, /^SELECT 1 \/\*route='%2Fr',traceparent=/)
  assert.ok(calls[0].cbSeen, 'extra arguments pass through')
  assert.match(calls[1].config.text, /^SELECT \* FROM t WHERE id = \$1 \/\*route=/)
  assert.deepEqual(calls[1].config.values, [1])
  assert.equal(shared.text, 'SELECT * FROM t WHERE id = $1', 'caller config is not mutated')
  assert.equal(calls[2].config.text, 'SELECT 2', 'named statements are left alone')
  assert.equal(calls[3].config, 'SELECT 3')
  assert.throws(() => sdk.instrumentPg({}), TypeError)
})

test('concurrent requests keep separate context', async () => {
  const seen = []
  await Promise.all(
    ['/a', '/b', '/c'].map((route) =>
      sdk.withRequest({ route }, async () => {
        await new Promise((r) => setTimeout(r, Math.random() * 10))
        seen.push([route, sdk.comment('SELECT 1')])
      }),
    ),
  )
  for (const [route, sql] of seen) assert.ok(sql.includes(`route='${encodeURIComponent(route)}'`), sql)
})
