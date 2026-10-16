// Types and fetchers for the Tracelet Studio API served by `tracelet up`.
// Go durations arrive as integer nanoseconds; live and series values arrive
// as microseconds.

export interface Fingerprint {
  hash: string
  query: string
  tables: string[] | null
}

export interface Stats {
  fingerprint: Fingerprint
  count: number
  errors: number
  total: number // ns
  mean: number
  p50: number
  p95: number
  p99: number
  max: number
  mean_rows: number
  first_seen: string
  last_seen: string
  last_error?: string
}

export interface TableStats {
  table: string
  fingerprints: string[]
  count: number
  total: number // ns
}

export interface SeriesPoint {
  at: number // unix ms
  count: number
  errors: number
  mean: number // µs
  p50: number
  p95: number
  max: number
  error?: string
}

export type Severity = 'info' | 'warning' | 'critical'

export interface Finding {
  hash: string
  detector: string
  severity: Severity
  title: string
  explanation: string
  suggestion: string
  fix?: string
  updated_at: string
  stats: Stats
  share_of_time: number
  index?: { table: string; columns: string[]; method?: string; extension?: string }
  simulation?: Simulation
}

/** A what-if estimate from a HypoPG hypothetical index. */
export interface Simulation {
  at: string
  unavailable?: string
  cost_before?: number
  cost_after?: number
  uses_index: boolean
  speedup?: number
}

/** Formats a speedup factor: 213 -> "213×", 4.26 -> "4.3×". */
export function fmtSpeedup(x: number): string {
  if (x >= 100) return `${Math.round(x).toLocaleString()}×`
  return `${x.toFixed(x < 10 ? 1 : 0)}×`
}

export interface PlanNode {
  node: string
  relation?: string
  schema?: string
  index?: string
  filter?: string
  index_cond?: string
  sort_key?: string[]
  est_rows: number
  total_cost: number
  children?: PlanNode[]
  table_rows?: number
  indexes?: { name: string; columns: string[]; unique?: boolean }[]
}

export interface PlanRecord {
  hash: string
  explained_at: string
  plan?: PlanNode
  error?: string
}

export interface RouteStats {
  route: string // "" for requests without a route
  requests: number
  queries: number
  avg_queries: number
  max_queries: number
  db_time: number // ns, all requests
  p50: number // ns, database time per request
  p95: number
  errors: number
}

export interface Span {
  h: string // fingerprint hash
  o: number // ns offset from the request start
  d: number // ns
  r: number
  e?: boolean
  c?: number
}

export interface Trace {
  id: string
  route: string
  start: string
  duration: number // ns
  queries: number
  db_time: number // ns
  errors: number
  truncated?: boolean
  spans?: Span[]
}

export interface TraceDetail {
  trace: Trace
  queries: Record<string, Fingerprint>
}

export type TraceOrder = 'slowest' | 'recent' | 'queries'

export interface Info {
  version: string
  upstream?: string
  proxy?: string
  data: string
  capture_params: boolean
  analyze: boolean
  started_at: string
  live: boolean
}

export interface LiveEvent {
  h: string // fingerprint hash
  q: string // normalized query
  t: string[] | null
  rt?: string // request route
  d: number // µs
  r: number
  e?: boolean
  at: number // unix ms
}

export interface LiveBatch {
  events: LiveEvent[]
  total: number
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`)
  return res.json() as Promise<T>
}

export const api = {
  info: () => get<Info>('/api/info'),
  top: (since: string) => get<Stats[]>(`/api/top?since=${since}&limit=200`),
  tables: (since: string) => get<TableStats[]>(`/api/tables?since=${since}`),
  series: (hash: string, since: string) =>
    get<SeriesPoint[]>(`/api/series?hash=${encodeURIComponent(hash)}&since=${since}`),
  findings: (since: string) => get<Finding[]>(`/api/findings?since=${since}`),
  routes: (since: string) => get<RouteStats[]>(`/api/routes?since=${since}`),
  traces: (route: string, order: TraceOrder, since: string) =>
    get<Trace[]>(`/api/traces?route=${encodeURIComponent(route)}&order=${order}&since=${since}&limit=50`),
  trace: (id: string) => get<TraceDetail>(`/api/trace?id=${encodeURIComponent(id)}`),
  plan: async (hash: string): Promise<PlanRecord | null> => {
    const res = await fetch(`/api/plan?hash=${encodeURIComponent(hash)}`)
    if (res.status === 404) return null
    if (!res.ok) throw new Error(`plan: ${res.status}`)
    return res.json() as Promise<PlanRecord>
  },
}

export const severityLabel: Record<Severity, string> = {
  critical: 'Critical',
  warning: 'Warning',
  info: 'Info',
}

export const severityRank: Record<Severity, number> = { critical: 2, warning: 1, info: 0 }

// Latency classes. Thresholds match the CLI's colors.
export type Speed = 'fast' | 'slow' | 'critical'

export function speedOf(ms: number): Speed {
  if (ms >= 100) return 'critical'
  if (ms >= 10) return 'slow'
  return 'fast'
}

export const speedLabel: Record<Speed, string> = {
  fast: 'Fast',
  slow: 'Slow',
  critical: 'Critical',
}

export const nsToMs = (ns: number) => ns / 1e6
export const usToMs = (us: number) => us / 1e3

/** Formats milliseconds with precision that suits the size. */
export function fmtMs(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)}ms`
  if (ms < 1000) return `${ms < 10 ? ms.toFixed(1) : Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const m = Math.floor(ms / 60_000)
  return `${m}m ${Math.round((ms % 60_000) / 1000)}s`
}

export function fmtCount(n: number): string {
  if (n < 1000) return String(Math.round(n))
  if (n < 1e6) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`
  return `${(n / 1e6).toFixed(1)}M`
}

export const windows = [
  { id: '5m', label: '5 min' },
  { id: '15m', label: '15 min' },
  { id: '1h', label: '1 hour' },
  { id: '24h', label: '24 hours' },
  { id: '0', label: 'All time' },
] as const

export type WindowId = (typeof windows)[number]['id']
