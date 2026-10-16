import { useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { fmtCount, fmtMs, nsToMs, windows } from './api'
import type { Finding, WindowId } from './api'
import { useFindings, useInfo, useLive, useRoutes, useStats } from './hooks'
import { Requests } from './Requests'
import { Insights } from './Insights'
import { QueryDetail } from './QueryDetail'
import { QueryTable, SpeedChip } from './QueryTable'
import { Topology } from './Topology'

type View = 'topology' | 'queries' | 'requests' | 'insights'

function readHash(): { view: View; query: string | null } {
  const p = new URLSearchParams(window.location.hash.slice(1))
  const v = p.get('view')
  return { view: v === 'queries' || v === 'requests' || v === 'insights' ? v : 'topology', query: p.get('q') }
}

export default function App() {
  const initial = readHash()
  const [view, setView] = useState<View>(initial.view)
  const [selected, setSelected] = useState<string | null>(initial.query)
  const [win, setWin] = useState<WindowId>(() => {
    try {
      return (localStorage.getItem('tracelet.window') as WindowId) || '15m'
    } catch {
      return '15m'
    }
  })

  const info = useInfo()
  const live = useLive()
  const { top, tables, error } = useStats(win)
  const findings = useFindings(win)
  const routes = useRoutes(win)

  // Findings per query, most severe first (the API already orders them).
  const issues = useMemo(() => {
    const m = new Map<string, Finding[]>()
    for (const f of findings.data ?? []) m.set(f.hash, [...(m.get(f.hash) ?? []), f])
    return m
  }, [findings.data])
  const criticalCount = (findings.data ?? []).filter((f) => f.severity === 'critical').length

  useEffect(() => {
    const p = new URLSearchParams({ view })
    if (selected) p.set('q', selected)
    history.replaceState(null, '', `#${p}`)
  }, [view, selected])

  useEffect(() => {
    try {
      localStorage.setItem('tracelet.window', win)
    } catch {
      /* storage unavailable: the choice just isn't remembered */
    }
  }, [win])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setSelected(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const totals = useMemo(() => {
    const t = { calls: 0, time: 0, errors: 0 }
    for (const s of top ?? []) {
      t.calls += s.count
      t.time += s.total
      t.errors += s.errors
    }
    return t
  }, [top])

  const selectedStats = top?.find((s) => s.fingerprint.hash === selected) ?? null
  const isLive = info.data?.live ?? true
  const winLabel = windows.find((w) => w.id === win)?.label.toLowerCase() ?? win

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <img src="/tracelet.svg" alt="" width={24} height={24} />
          Tracelet Studio
          {info.data && <small>{info.data.version}</small>}
        </div>
        <div className="conn" title={info.data?.data}>
          <span className={`dot ${isLive ? live.status : ''}`} aria-hidden />
          <span className="route">
            {!info.data
              ? 'Connecting…'
              : isLive
                ? `${statusText[live.status]} · app → ${info.data.proxy} → ${info.data.upstream}`
                : `Saved data · ${info.data.data}`}
          </span>
        </div>
        <span className="spacer" />
        <label className="dim window-label" htmlFor="window" style={{ fontSize: 12 }}>
          Window
        </label>
        <select id="window" className="select" value={win} onChange={(e) => setWin(e.target.value as WindowId)}>
          {windows.map((w) => (
            <option key={w.id} value={w.id}>
              {w.label}
            </option>
          ))}
        </select>
      </header>

      <section className="kpis" aria-label="Summary">
        {isLive && (
          <>
            <Kpi label="Queries / sec" value={live.metrics.qps < 10 ? live.metrics.qps.toFixed(1) : fmtCount(live.metrics.qps)} sub="last 5s" />
            <Kpi label="Live p95" value={live.metrics.p95 ? <SpeedChip ms={live.metrics.p95} /> : '–'} sub="last 10s" />
          </>
        )}
        <Kpi label="Time in database" value={fmtMs(nsToMs(totals.time))} sub={winLabel} />
        <Kpi label="Queries" value={fmtCount(totals.calls)} sub={winLabel} />
        <Kpi label="Query shapes" value={top ? String(top.length) : '–'} sub="distinct fingerprints" />
        <Kpi
          label="Suggestions"
          value={findings.data ? (criticalCount ? <span className="err">{findings.data.length}</span> : String(findings.data.length)) : '–'}
          sub={findings.data?.length ? `${criticalCount} critical` : 'from EXPLAIN'}
        />
        <Kpi
          label="Errors"
          value={totals.errors ? <span className="err">{fmtCount(totals.errors)}</span> : '0'}
          sub={winLabel}
        />
      </section>

      <div className={`main ${selectedStats ? 'with-detail' : ''}`}>
        <div className="pane">
          <nav className="tabs" role="tablist" aria-label="Views">
            <button role="tab" className="tab" aria-selected={view === 'topology'} onClick={() => setView('topology')}>
              Live topology
            </button>
            <button role="tab" className="tab" aria-selected={view === 'queries'} onClick={() => setView('queries')}>
              Queries
            </button>
            <button role="tab" className="tab" aria-selected={view === 'requests'} onClick={() => setView('requests')}>
              Requests
            </button>
            <button role="tab" className="tab" aria-selected={view === 'insights'} onClick={() => setView('insights')}>
              Insights
              {!!findings.data?.length && (
                <span className={`count-badge ${criticalCount ? 'critical' : ''}`}>{findings.data.length}</span>
              )}
            </button>
            <span className="hint">Ranked by total time: calls × latency</span>
          </nav>
          {error && <div className="banner">Can’t reach Tracelet: {error}</div>}
          {!isLive && (
            <div className="banner">Viewing saved data. Run “tracelet up” to watch queries live.</div>
          )}
          {view === 'requests' ? (
            <Requests routes={routes.data} win={win} onSelectQuery={setSelected} />
          ) : view === 'insights' ? (
            <Insights findings={findings.data} analyze={info.data?.analyze ?? true} live={isLive} onSelect={setSelected} />
          ) : view === 'topology' ? (
            <Topology
              issues={issues}
              routes={routes.data ?? []}
              stats={top ?? []}
              tables={tables ?? []}
              subscribe={live.subscribe}
              live={isLive && live.status === 'live'}
              selected={selected}
              onSelect={setSelected}
              loaded={top !== null}
            />
          ) : (
            <QueryTable stats={top ?? []} selected={selected} onSelect={setSelected} issues={issues} />
          )}
        </div>
        {selectedStats && (
          <QueryDetail
            stats={selectedStats}
            win={win}
            findings={issues.get(selectedStats.fingerprint.hash) ?? []}
            onClose={() => setSelected(null)}
          />
        )}
      </div>
    </div>
  )
}

const statusText = {
  connecting: 'Connecting',
  live: 'Live',
  offline: 'Offline',
  disconnected: 'Disconnected',
} as const

function Kpi({ label, value, sub }: { label: string; value: ReactNode; sub: string }) {
  return (
    <div className="kpi">
      <div className="label">{label}</div>
      <div className="value num">
        <span className="v">{value}</span>
        <span className="sub">{sub}</span>
      </div>
    </div>
  )
}
