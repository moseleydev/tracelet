'use strict'

// tracelet-sdk: tags each database query with the request that ran it, as a
// sqlcommenter comment (https://google.github.io/sqlcommenter/):
//
//   SELECT * FROM users WHERE id = $1 /*route='GET%20%2Fusers%2F%3Aid',traceparent='00-…-…-01'*/
//
// Tracelet reads these comments to group queries into requests. Nothing is
// sent anywhere: the comment travels with the query to your database.

const { AsyncLocalStorage } = require('node:async_hooks')
const { randomBytes } = require('node:crypto')

const storage = new AsyncLocalStorage()

const TRACEPARENT = /^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$/

/** Parses a W3C traceparent header, or returns null if it is not valid. */
function parseTraceparent(header) {
  if (typeof header !== 'string') return null
  const m = TRACEPARENT.exec(header.trim())
  if (!m || m[1] === 'ff' || /^0+$/.test(m[2]) || /^0+$/.test(m[3])) return null
  return { version: m[1], traceId: m[2], parentId: m[3], flags: m[4] }
}

/**
 * Returns a traceparent for a new request span. When an incoming header is
 * given (for example from OpenTelemetry or a load balancer), the trace id is
 * kept so Tracelet's traces line up with the rest of your tracing.
 */
function newTraceparent(incoming) {
  const parent = parseTraceparent(incoming)
  const traceId = parent ? parent.traceId : randomBytes(16).toString('hex')
  const flags = parent ? parent.flags : '01'
  return `00-${traceId}-${randomBytes(8).toString('hex')}-${flags}`
}

/**
 * Runs fn with request context. Every query started inside fn (including
 * after awaits) is tagged with it.
 *
 * @param {{ route?: string | (() => string | undefined), traceparent?: string }} context
 */
function withRequest(context, fn) {
  const ctx = { ...context }
  if (!parseTraceparent(ctx.traceparent)) ctx.traceparent = newTraceparent(ctx.traceparent)
  return storage.run(ctx, fn)
}

/** The current request context, or undefined outside a request. */
function currentRequest() {
  return storage.getStore()
}

// encodeURIComponent leaves ' unescaped; sqlcommenter escapes it with a backslash.
const enc = (s) => encodeURIComponent(String(s)).replace(/'/g, "\\'")

/**
 * Appends a sqlcommenter comment with the given fields (or the current
 * request's) to sql. SQL that already contains a comment is returned
 * unchanged, as the sqlcommenter spec requires.
 *
 * @param {string} sql
 * @param {Record<string, string | undefined>} [fields]
 */
function comment(sql, fields) {
  if (typeof sql !== 'string' || sql.includes('*/')) return sql
  if (!fields) {
    const ctx = storage.getStore()
    if (!ctx) return sql
    const route = typeof ctx.route === 'function' ? ctx.route() : ctx.route
    fields = { route, traceparent: ctx.traceparent }
  }
  const pairs = Object.keys(fields)
    .filter((k) => fields[k] !== undefined && fields[k] !== null && fields[k] !== '')
    .sort()
    .map((k) => `${enc(k)}='${enc(fields[k])}'`)
  if (pairs.length === 0) return sql

  const text = `/*${pairs.join(',')}*/`
  const trimmed = sql.replace(/\s+$/, '')
  if (trimmed.endsWith(';')) return `${trimmed.slice(0, -1).replace(/\s+$/, '')} ${text};`
  return `${trimmed} ${text}`
}

/**
 * Express / Connect middleware. Starts request context for each request,
 * continuing an incoming traceparent header if there is one. The route is
 * the matched route pattern ("GET /users/:id"), read when each query runs,
 * so it is known once Express has routed the request. Raw URLs are never
 * used, so ids and other values in paths don't end up in your stats.
 */
function middleware() {
  return function traceletMiddleware(req, res, next) {
    const route = () => {
      const pattern = req.route && req.route.path
      if (typeof pattern !== 'string') return undefined
      return `${req.method} ${(req.baseUrl || '') + pattern}`
    }
    withRequest({ route, traceparent: newTraceparent(req.headers && req.headers.traceparent) }, next)
  }
}

const PATCHED = Symbol.for('tracelet.instrumented')

/**
 * Tags every query made through node-postgres (`pg`). Pass the module:
 *
 *   const pg = require('pg')
 *   require('tracelet-sdk').instrumentPg(pg)
 *
 * Queries with a `name` (pg's named prepared statements) are left alone,
 * because pg requires a named statement's text to stay the same.
 */
function instrumentPg(pg) {
  const proto = pg && pg.Client && pg.Client.prototype
  if (!proto || typeof proto.query !== 'function') throw new TypeError('instrumentPg: pass the pg module')
  if (proto[PATCHED]) return pg
  const original = proto.query
  proto.query = function query(config, ...rest) {
    if (typeof config === 'string') {
      config = comment(config)
    } else if (config && typeof config.text === 'string' && !config.name) {
      // Copy: pg Query objects and shared configs must not be mutated.
      config = Object.assign(Object.create(Object.getPrototypeOf(config)), config, { text: comment(config.text) })
    }
    return original.call(this, config, ...rest)
  }
  proto[PATCHED] = true
  return pg
}

module.exports = {
  withRequest,
  currentRequest,
  comment,
  middleware,
  instrumentPg,
  newTraceparent,
  parseTraceparent,
}
