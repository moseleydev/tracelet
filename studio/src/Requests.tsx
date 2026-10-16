import { useEffect, useMemo, useState } from 'react'
import { api, fmtCount, fmtMs, nsToMs, speedOf } from './api'
import type { RouteStats, Span, TraceOrder, WindowId } from './api'
import { usePoll } from './hooks'
import { SpeedChip } from './QueryTable'

const routeLabel = (route: string) => route || '(no route)'

interface Props {
  routes: RouteStats[] | null
  win: WindowId
  onSelectQuery: (hash: string) => void
}

export function Requests({ routes, win, onSelectQuery }: Props) {
  const [route, setRoute] = useState<string | null>(null)
  const [order, setOrder] = useState<TraceOrder>('slowest')
  const [traceId, setTraceId] = useState<string | null>(null)

  // Default to the costliest route.
  useEffect(() => {
    if (route === null && routes?.length) setRoute(routes[0].route)
  }, [routes, route])

  const traces = usePoll(
    () => (route === null ? Promise.resolve([]) : api.traces(route, order, win)),
    [route, order, win],
    4000,
  )

  // Open the first request of the list when nothing is chosen.
  useEffect(() => {
    const list = traces.data
    if (list && list.length && (!traceId || !list.some((t) => t.id === traceId))) setTraceId(list[0].id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [traces.data])

  if (!routes) return <div className="requests dim pad">Loading…</div>
  if (routes.length === 0) {
    return (
      <div className="requests">
        <div className="notice">
          <strong>No requests yet.</strong> Tracelet groups queries into requests when your app tags them with a route and trace id
          (sqlcommenter comments). Add the SDK: <code>npm install tracelet-sdk</code> for Node or <code>pip install tracelet-sdk</code>{' '}
          for Python. Any OpenTelemetry or sqlcommenter integration works too.
        </div>
      </div>
    )
  }

  const totalDb = routes.reduce((s, r) => s + r.db_time, 0)

  return (
    <div className="requests">
      <section className="routes-panel" aria-label="Routes">
        <div className="routes-list" role="listbox" aria-label="Routes, by database time">
          {routes.map((r) => {
            const share = totalDb ? r.db_time / totalDb : 0
            return (
              <button
                key={r.route}
                role="option"
                aria-selected={route === r.route}
                className="route-item"
                onClick={() => {
                  setRoute(r.route)
                  setTraceId(null)
                }}
              >
                <code className="route-name">{routeLabel(r.route)}</code>
                <span className="route-meta num">
                  <span>
                    <b>{fmtCount(r.requests)}</b> requests
                  </span>
                  <span className={r.avg_queries >= 10 ? 'many' : ''}>
                    <b>{r.avg_queries.toFixed(r.avg_queries < 10 ? 1 : 0)}</b> queries/req
                    {r.max_queries > r.avg_queries + 0.5 && <span className="dim"> (max {fmtCount(r.max_queries)})</span>}
                  </span>
                  <span>
                    p95 <SpeedChip ms={nsToMs(r.p95)} /> DB/req
                  </span>
                </span>
                <span className="impact-line num">
                  <span className="share-bar" aria-hidden>
                    <i style={{ width: `${Math.max(2, share * 100)}%` }} />
                  </span>
                  <span className="dim">{(share * 100).toFixed(share < 0.1 ? 1 : 0)}% of DB time</span>
                </span>
              </button>
            )
          })}
        </div>
      </section>

      <section className="traces-panel" aria-label="Requests">
        <div className="toolbar">
          <strong className="mono route-title">{route === null ? '' : routeLabel(route)}</strong>
          <span className="spacer" />
          <div className="seg" role="radiogroup" aria-label="Order requests by">
            {(['slowest', 'recent', 'queries'] as TraceOrder[]).map((o) => (
              <button key={o} role="radio" aria-checked={order === o} onClick={() => setOrder(o)}>
                {o === 'slowest' ? 'Slowest' : o === 'recent' ? 'Recent' : 'Most queries'}
              </button>
            ))}
          </div>
        </div>
        <div className="trace-list" role="listbox" aria-label="Requests">
          {(traces.data ?? []).map((t) => (
            <button
              key={t.id}
              role="option"
              aria-selected={t.id === traceId}
              className="trace-item num"
              onClick={() => setTraceId(t.id)}
            >
              <span className="dim">{new Date(t.start).toLocaleTimeString()}</span>
              <span>
                <b>{fmtMs(nsToMs(t.db_time))}</b> in DB
              </span>
              <span>{fmtCount(t.queries)} queries</span>
              {t.errors > 0 && <span className="err">{t.errors} failed</span>}
            </button>
          ))}
        </div>
        {traceId && <Waterfall id={traceId} onSelectQuery={onSelectQuery} />}
      </section>
    </div>
  )
}

const MAX_ROWS = 300

interface Row {
  span: Span
  query: string
  run: number // length of the run of identical queries this row starts, else 0
  inRun: boolean
}

function Waterfall({ id, onSelectQuery }: { id: string; onSelectQuery: (hash: string) => void }) {
  const detail = usePoll(() => api.trace(id), [id], 60_000)
  const [showAll, setShowAll] = useState(false)
  const [hover, setHover] = useState<number | null>(null)

  const rows = useMemo<Row[]>(() => {
    const spans = detail.data?.trace.spans ?? []
    const out: Row[] = spans.map((span) => ({
      span,
      query: detail.data?.queries[span.h]?.query || span.h,
      run: 0,
      inRun: false,
    }))
    for (let i = 0; i < out.length; ) {
      let j = i
      while (j < out.length && out[j].span.h === out[i].span.h) j++
      if (j - i >= 3) {
        out[i].run = j - i
        for (let k = i; k < j; k++) out[k].inRun = true
      }
      i = j
    }
    return out
  }, [detail.data])

  if (!detail.data) return <p className="dim pad">{detail.error ? `Couldn’t load: ${detail.error}` : 'Loading…'}</p>
  const t = detail.data.trace
  const total = Math.max(1, t.duration)
  const shown = showAll ? rows : rows.slice(0, MAX_ROWS)
  const ticks = [0, 0.25, 0.5, 0.75, 1]

  return (
    <div className="waterfall">
      <div className="wf-head num">
        <span>
          <b>{fmtCount(t.queries)}</b> queries
        </span>
        <span>
          <b>{fmtMs(nsToMs(t.db_time))}</b> in the database
        </span>
        <span>
          <b>{fmtMs(nsToMs(t.duration))}</b> from first query to last
        </span>
        {t.truncated && <span className="err">Truncated: very long request</span>}
        <span className="dim mono">trace {t.id.slice(0, 12)}…</span>
      </div>
      <div className="wf-grid" role="table" aria-label="Queries in this request, in order">
        <div className="wf-row wf-axis" role="row" aria-hidden>
          <span />
          <span className="wf-track">
            {ticks.map((f) => (
              <i key={f} style={{ left: `${f * 100}%` }}>
                {fmtMs(nsToMs(total * f))}
              </i>
            ))}
          </span>
        </div>
        {shown.map((r, i) => {
          const left = (r.span.o / total) * 100
          const width = Math.max(0.4, (r.span.d / total) * 100)
          const speed = speedOf(nsToMs(r.span.d))
          return (
            <div
              key={i}
              role="row"
              tabIndex={0}
              className={`wf-row ${r.inRun ? 'in-run' : ''} ${r.run ? 'run-start' : ''}`}
              onClick={() => onSelectQuery(r.span.h)}
              onKeyDown={(e) => e.key === 'Enter' && onSelectQuery(r.span.h)}
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover(null)}
            >
              <span className="wf-label mono" role="cell" title={r.query}>
                {r.run > 0 && <span className="run-badge">{r.run}× in a row</span>}
                {r.query}
              </span>
              <span className="wf-track" role="cell">
                <i className={`wf-bar ${speed} ${r.span.e ? 'failed' : ''}`} style={{ left: `${left}%`, width: `${width}%` }} />
                {hover === i && (
                  <span className="wf-tip num" style={left > 60 ? { right: `${100 - left}%` } : { left: `${left + width}%` }}>
                    at {fmtMs(nsToMs(r.span.o))} · took {fmtMs(nsToMs(r.span.d))} · {fmtCount(r.span.r)} rows
                    {r.span.e ? ' · failed' : ''}
                  </span>
                )}
              </span>
            </div>
          )
        })}
      </div>
      {rows.length > MAX_ROWS && !showAll && (
        <button className="icon-btn" onClick={() => setShowAll(true)}>
          Show all {rows.length} queries
        </button>
      )}
      <p className="caption">
        Each row is one query, in the order the request ran them. Bars show when it started and how long it took; click a row for that
        query’s details. A long run of the same query is usually an N+1 loop.
      </p>
    </div>
  )
}
