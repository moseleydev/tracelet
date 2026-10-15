import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { api, fmtCount, fmtMs, nsToMs, usToMs } from './api'
import type { SeriesPoint, Stats, WindowId } from './api'
import { BarChart, LineChart } from './charts'
import { usePoll } from './hooks'
import { SpeedChip, TableTags } from './QueryTable'

const BUCKET_MS = 10_000
const MAX_POINTS = 120

interface Bucketed {
  times: number[]
  p50: (number | null)[]
  p95: (number | null)[]
  calls: number[]
  errors: number[]
  bucketMs: number
}

/**
 * Lays the stored 10s rollups on a continuous timeline (so gaps show as gaps)
 * and merges neighbours when there are too many to draw. Merged buckets
 * report a call-weighted p50 and the highest p95, which errs on the side of
 * showing a spike rather than hiding it.
 */
function bucketize(points: SeriesPoint[]): Bucketed {
  const out: Bucketed = { times: [], p50: [], p95: [], calls: [], errors: [], bucketMs: BUCKET_MS }
  if (!points.length) return out
  const start = points[0].at
  const end = points[points.length - 1].at
  const slots = Math.floor((end - start) / BUCKET_MS) + 1
  const group = Math.max(1, Math.ceil(slots / MAX_POINTS))
  out.bucketMs = BUCKET_MS * group

  const n = Math.ceil(slots / group)
  const acc = Array.from({ length: n }, () => ({ calls: 0, errors: 0, p50w: 0, p95: 0 }))
  for (const p of points) {
    const i = Math.floor((p.at - start) / BUCKET_MS / group)
    const a = acc[i]
    a.calls += p.count
    a.errors += p.errors
    a.p50w += p.p50 * p.count
    a.p95 = Math.max(a.p95, p.p95)
  }
  acc.forEach((a, i) => {
    out.times.push(start + i * out.bucketMs)
    out.calls.push(a.calls)
    out.errors.push(a.errors)
    out.p50.push(a.calls ? usToMs(a.p50w / a.calls) : null)
    out.p95.push(a.calls ? usToMs(a.p95) : null)
  })
  return out
}

export function QueryDetail({ stats, win, onClose }: { stats: Stats; win: WindowId; onClose: () => void }) {
  const hash = stats.fingerprint.hash
  const series = usePoll<SeriesPoint[]>(() => api.series(hash, win), [hash, win], 3000)
  const b = useMemo(() => bucketize(series.data ?? []), [series.data])
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(stats.fingerprint.query)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard blocked; the query is selectable text anyway */
    }
  }

  const per = b.bucketMs >= 60_000 ? `${b.bucketMs / 60_000} min` : `${b.bucketMs / 1000}s`

  return (
    <aside className="detail" aria-label="Query details">
      <header>
        <h2>Query shape · {fmtCount(stats.count)} calls</h2>
        <div style={{ display: 'flex', gap: 6 }}>
          <button className="icon-btn" onClick={copy}>
            {copied ? 'Copied' : 'Copy SQL'}
          </button>
          <button className="icon-btn" onClick={onClose} aria-label="Close details">
            Close
          </button>
        </div>
      </header>

      <pre className="sql mono">{stats.fingerprint.query}</pre>
      <TableTags tables={stats.fingerprint.tables} />

      <div className="tiles">
        <Tile label="Total time" value={fmtMs(nsToMs(stats.total))} />
        <Tile label="Calls" value={fmtCount(stats.count)} />
        <Tile label="Mean" value={fmtMs(nsToMs(stats.mean))} />
        <Tile label="p50" value={fmtMs(nsToMs(stats.p50))} />
        <Tile label="p95" value={<SpeedChip ms={nsToMs(stats.p95)} />} />
        <Tile label="p99" value={fmtMs(nsToMs(stats.p99))} />
        <Tile label="Max" value={fmtMs(nsToMs(stats.max))} />
        <Tile label="Rows / call" value={fmtCount(stats.mean_rows)} />
        <Tile label="Errors" value={stats.errors ? <span className="err">{fmtCount(stats.errors)}</span> : '0'} />
      </div>

      <div className="section">
        <h3>Latency over time</h3>
        <p className="caption">Per {per}. A widening gap between p50 and p95 means some calls are much slower than usual.</p>
        {b.times.length ? (
          <LineChart
            times={b.times}
            data={{ p50: b.p50, p95: b.p95 }}
            series={[
              { key: 'p50', label: 'p50', color: 'var(--series-1)' },
              { key: 'p95', label: 'p95', color: 'var(--series-2)' },
            ]}
            format={fmtMs}
            ariaLabel={`Latency of this query over time, p50 and p95, per ${per}`}
          />
        ) : (
          <p className="dim">{series.error ? `Couldn’t load: ${series.error}` : 'Loading…'}</p>
        )}
      </div>

      <div className="section">
        <h3>Calls</h3>
        <p className="caption">Per {per}.</p>
        {b.times.length > 0 && (
          <BarChart
            times={b.times}
            values={b.calls}
            errors={b.errors}
            color="var(--series-1)"
            format={(v) => fmtCount(v)}
            ariaLabel={`Number of calls per ${per}`}
          />
        )}
      </div>

      {stats.last_error && (
        <div className="section">
          <h3>Last error</h3>
          <div className="error-box">
            <strong>{fmtCount(stats.errors)} failed</strong> · <span className="mono">{stats.last_error}</span>
          </div>
        </div>
      )}
    </aside>
  )
}

function Tile({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="tile">
      <div className="label">{label}</div>
      <div className="value num">{value}</div>
    </div>
  )
}
