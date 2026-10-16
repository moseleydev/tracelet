export interface RequestContext {
  /** The route pattern, e.g. "GET /users/:id", or a function returning it. */
  route?: string | (() => string | undefined)
  /** A W3C traceparent. A new one is generated when missing or invalid. */
  traceparent?: string
}

export interface Traceparent {
  version: string
  traceId: string
  parentId: string
  flags: string
}

/** Runs fn with request context; queries started inside it are tagged. */
export function withRequest<T>(context: RequestContext, fn: () => T): T

/** The current request context, or undefined outside a request. */
export function currentRequest(): RequestContext | undefined

/**
 * Appends a sqlcommenter comment with the given fields, or the current
 * request's, to sql. SQL that already has a comment is returned unchanged.
 */
export function comment(sql: string, fields?: Record<string, string | undefined>): string

/** Express / Connect middleware that starts request context per request. */
export function middleware(): (req: any, res: any, next: (err?: unknown) => void) => void

/** Tags every query made through node-postgres. Returns the module. */
export function instrumentPg<T>(pg: T): T

/** Returns a new traceparent, continuing the trace of `incoming` if valid. */
export function newTraceparent(incoming?: string): string

/** Parses a W3C traceparent header, or returns null if it is invalid. */
export function parseTraceparent(header: unknown): Traceparent | null
