import { useMemo, useState } from 'react'
import { fmtCount, fmtMs, nsToMs, speedLabel, speedOf } from './api'
import type { Finding, Stats } from './api'
import { SeverityChip } from './Insights'

export function SpeedChip({ ms }: { ms: number }) {
  const s = speedOf(ms)
  return (
    <span className={`chip ${s}`}>
      <span className="swatch" aria-hidden />
      {fmtMs(ms)}
      <span className="sr-only"> ({speedLabel[s]})</span>
    </span>
  )
}

export function TableTags({ tables }: { tables: string[] | null }) {
  if (!tables?.length) return null
  return (
    <div className="tables">
      {tables.map((t) => (
        <span className="table-tag mono" key={t}>
          {t}
        </span>
      ))}
    </div>
  )
}

type SortKey = 'total' | 'count' | 'mean' | 'p95' | 'max' | 'mean_rows' | 'errors'

const columns: { key: SortKey; label: string }[] = [
  { key: 'total', label: 'Total time' },
  { key: 'count', label: 'Calls' },
  { key: 'mean', label: 'Mean' },
  { key: 'p95', label: 'p95' },
  { key: 'max', label: 'Max' },
  { key: 'mean_rows', label: 'Rows' },
  { key: 'errors', label: 'Errors' },
]

interface Props {
  stats: Stats[]
  selected: string | null
  onSelect: (hash: string) => void
  issues: Map<string, Finding[]>
}

export function QueryTable({ stats, selected, onSelect, issues }: Props) {
  const [sort, setSort] = useState<SortKey>('total')
  const [filter, setFilter] = useState('')

  const grandTotal = useMemo(() => stats.reduce((s, q) => s + q.total, 0), [stats])
  const rows = useMemo(() => {
    const f = filter.trim().toLowerCase()
    const list = f
      ? stats.filter(
          (s) =>
            s.fingerprint.query.toLowerCase().includes(f) ||
            s.fingerprint.tables?.some((t) => t.toLowerCase().includes(f)),
        )
      : stats
    return [...list].sort((a, b) => b[sort] - a[sort])
  }, [stats, sort, filter])

  return (
    <div className="explorer">
      <div className="toolbar">
        <input
          className="search"
          type="search"
          placeholder="Filter by query text or table"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          aria-label="Filter queries"
        />
        <span className="count num">
          {rows.length} of {stats.length} query shapes
        </span>
      </div>
      <div className="scroll">
        <table className="qtable">
          <thead>
            <tr>
              <th scope="col">Query</th>
              {columns.map((c) => (
                <th scope="col" key={c.key} aria-sort={sort === c.key ? 'descending' : undefined}>
                  <button onClick={() => setSort(c.key)}>
                    {c.label}
                    {sort === c.key ? ' ↓' : ''}
                  </button>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => {
              const share = grandTotal ? s.total / grandTotal : 0
              return (
                <tr
                  key={s.fingerprint.hash}
                  tabIndex={0}
                  aria-selected={selected === s.fingerprint.hash}
                  onClick={() => onSelect(s.fingerprint.hash)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      onSelect(s.fingerprint.hash)
                    }
                  }}
                >
                  <td className="query">
                    <code>{s.fingerprint.query || '(empty query)'}</code>
                    <TableTags tables={s.fingerprint.tables} />
                    {issues.get(s.fingerprint.hash)?.map((f) => (
                      <div className="issue-line" key={f.detector}>
                        <SeverityChip severity={f.severity} />
                        <span>{f.title}</span>
                      </div>
                    ))}
                  </td>
                  <td className="num">
                    <div className="impact">
                      <span>{fmtMs(nsToMs(s.total))}</span>
                      <span className="bar" aria-hidden>
                        <i style={{ width: `${Math.max(2, share * 100)}%` }} />
                      </span>
                      <span className="share">{(share * 100).toFixed(share < 0.1 ? 1 : 0)}% of time</span>
                    </div>
                  </td>
                  <td className="num">{fmtCount(s.count)}</td>
                  <td className="num">{fmtMs(nsToMs(s.mean))}</td>
                  <td className="num">
                    <SpeedChip ms={nsToMs(s.p95)} />
                  </td>
                  <td className="num">{fmtMs(nsToMs(s.max))}</td>
                  <td className="num">{fmtCount(s.mean_rows)}</td>
                  <td className="num">{s.errors ? <span className="err">{fmtCount(s.errors)}</span> : <span className="dim">–</span>}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {rows.length === 0 && stats.length > 0 && <p className="dim">No queries match “{filter}”.</p>}
      </div>
    </div>
  )
}
