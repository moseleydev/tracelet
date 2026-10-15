import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'

// Small, dependency-free SVG charts. Text is drawn at real pixel sizes: the
// SVG is sized to its container rather than scaled.

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [width, setWidth] = useState(0)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setWidth(Math.round(e.contentRect.width)))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, width] as const
}

/** Rounds max up to a 1/2/5 step and returns evenly spaced ticks from 0. */
function niceTicks(max: number, count = 4): number[] {
  if (max <= 0) return [0, 1]
  const raw = max / count
  const mag = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw
  const ticks: number[] = []
  for (let v = 0; v <= max + step * 0.001; v += step) ticks.push(v)
  if (ticks[ticks.length - 1] < max) ticks.push(ticks[ticks.length - 1] + step)
  return ticks
}

const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })
const timeFmtSec = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })

export interface Series {
  key: string
  label: string
  color: string // CSS value, e.g. var(--series-1)
}

interface Margins {
  top: number
  right: number
  bottom: number
  left: number
}

// The hover target starts half a step left of the first point, so the
// nearest point index is (offset - step/2) / step, rounded.
function useHover(n: number, step: number) {
  const [idx, setIdx] = useState<number | null>(null)
  const onMove = (e: React.MouseEvent<SVGRectElement>) => {
    if (n <= 1) return setIdx(n ? 0 : null)
    const offset = e.clientX - e.currentTarget.getBoundingClientRect().left
    setIdx(Math.max(0, Math.min(n - 1, Math.round((offset - step / 2) / step))))
  }
  return { idx, onMove, clear: () => setIdx(null) }
}

interface LineProps {
  times: number[] // unix ms
  data: Record<string, (number | null)[]> // series key -> values (null = no data)
  series: Series[]
  format: (v: number) => string
  height?: number
  ariaLabel: string
}

export function LineChart({ times, data, series, format, height = 180, ariaLabel }: LineProps) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const m: Margins = { top: 10, right: 44, bottom: 22, left: 52 }
  const w = Math.max(0, width - m.left - m.right)
  const h = height - m.top - m.bottom
  const n = times.length
  const step = n > 1 ? w / (n - 1) : 0
  const x = (i: number) => m.left + (n > 1 ? i * step : w / 2)

  const max = Math.max(0, ...series.flatMap((s) => data[s.key].map((v) => v ?? 0)))
  const ticks = niceTicks(max)
  const top = ticks[ticks.length - 1]
  const y = (v: number) => m.top + h - (v / top) * h

  const hover = useHover(n, step)
  const hi = hover.idx

  const path = (vals: (number | null)[]) => {
    let d = ''
    let pen = false
    vals.forEach((v, i) => {
      if (v === null) {
        pen = false
        return
      }
      d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`
      pen = true
    })
    return d
  }

  const lastIdx = (vals: (number | null)[]) => {
    for (let i = vals.length - 1; i >= 0; i--) if (vals[i] !== null) return i
    return -1
  }

  // Direct labels at the line ends, nudged apart if they would collide.
  const ends = series
    .map((s) => {
      const i = lastIdx(data[s.key])
      return i < 0 ? null : { s, yy: y(data[s.key][i] as number) }
    })
    .filter((e): e is { s: Series; yy: number } => !!e)
    .sort((a, b) => a.yy - b.yy)
  for (let i = 1; i < ends.length; i++) if (ends[i].yy - ends[i - 1].yy < 13) ends[i].yy = ends[i - 1].yy + 13

  return (
    <div className="chart" ref={ref}>
      <div className="chart-legend">
        {series.map((s) => (
          <span key={s.key}>
            <i style={{ background: s.color }} />
            {s.label}
          </span>
        ))}
      </div>
      {width > 0 && (
        <svg height={height} role="img" aria-label={ariaLabel}>
          {ticks.map((t) => (
            <g key={t}>
              <line className={t === 0 ? 'baseline' : 'gridline'} x1={m.left} x2={m.left + w} y1={y(t)} y2={y(t)} />
              <text className="axis" x={m.left - 8} y={y(t)} textAnchor="end" dominantBaseline="middle">
                {format(t)}
              </text>
            </g>
          ))}
          {n > 0 && <TimeAxis times={times} x={x} y={m.top + h + 15} />}

          {series.map((s) =>
            n > 1 ? (
              <path key={s.key} d={path(data[s.key])} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
            ) : null,
          )}
          {/* Single points (or isolated buckets) still need a visible mark. */}
          {series.map((s) =>
            data[s.key].map((v, i) => {
              const isolated = v !== null && (n === 1 || ((data[s.key][i - 1] ?? null) === null && (data[s.key][i + 1] ?? null) === null))
              return isolated ? <circle key={`${s.key}${i}`} cx={x(i)} cy={y(v as number)} r={4} fill={s.color} stroke="var(--surface)" strokeWidth={2} /> : null
            }),
          )}
          {ends.map(({ s, yy }) => (
            <text key={s.key} className="series-label" x={m.left + w + 6} y={yy} dominantBaseline="middle">
              {s.label}
            </text>
          ))}

          {hi !== null && (
            <g>
              <line className="crosshair" x1={x(hi)} x2={x(hi)} y1={m.top} y2={m.top + h} />
              {series.map((s) => {
                const v = data[s.key][hi]
                return v === null ? null : <circle key={s.key} cx={x(hi)} cy={y(v)} r={4} fill={s.color} stroke="var(--surface)" strokeWidth={2} />
              })}
            </g>
          )}
          <rect
            x={m.left - step / 2}
            y={m.top}
            width={w + step}
            height={h}
            fill="transparent"
            onMouseMove={hover.onMove}
            onMouseLeave={hover.clear}
          />
        </svg>
      )}
      {hi !== null && width > 0 && (
        <Tip left={x(hi)} width={width} title={timeFmtSec.format(times[hi])}>
          {series.map((s) => (
            <div className="r" key={s.key}>
              <i style={{ background: s.color }} />
              {s.label}
              <b className="num">{data[s.key][hi] === null ? 'no calls' : format(data[s.key][hi] as number)}</b>
            </div>
          ))}
        </Tip>
      )}
    </div>
  )
}

interface BarProps {
  times: number[]
  values: number[]
  errors?: number[]
  color: string
  format: (v: number) => string
  height?: number
  ariaLabel: string
}

export function BarChart({ times, values, errors, color, format, height = 120, ariaLabel }: BarProps) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const m: Margins = { top: 8, right: 44, bottom: 22, left: 52 }
  const w = Math.max(0, width - m.left - m.right)
  const h = height - m.top - m.bottom
  const n = values.length
  const slot = n ? w / n : 0
  const barW = Math.max(1, slot - 2) // 2px surface gap between bars
  const ticks = niceTicks(Math.max(0, ...values), 3)
  const top = ticks[ticks.length - 1] || 1
  const y = (v: number) => m.top + h - (v / top) * h
  const [hi, setHi] = useState<number | null>(null)

  return (
    <div className="chart" ref={ref}>
      {width > 0 && (
        <svg height={height} role="img" aria-label={ariaLabel}>
          {ticks.map((t) => (
            <g key={t}>
              <line className={t === 0 ? 'baseline' : 'gridline'} x1={m.left} x2={m.left + w} y1={y(t)} y2={y(t)} />
              <text className="axis" x={m.left - 8} y={y(t)} textAnchor="end" dominantBaseline="middle">
                {format(t)}
              </text>
            </g>
          ))}
          {n > 0 && <TimeAxis times={times} x={(i) => m.left + slot * (i + 0.5)} y={m.top + h + 15} />}
          {values.map((v, i) => {
            if (v <= 0) return null
            const bh = Math.max(1, m.top + h - y(v))
            const r = Math.min(4, barW / 2, bh)
            const bx = m.left + slot * i + 1
            const by = m.top + h - bh
            // Rounded data end (top), square at the baseline.
            const d = `M${bx},${m.top + h}V${by + r}Q${bx},${by} ${bx + r},${by}H${bx + barW - r}Q${bx + barW},${by} ${bx + barW},${by + r}V${m.top + h}Z`
            return <path key={i} d={d} fill={color} opacity={hi === null || hi === i ? 1 : 0.55} />
          })}
          {values.map((_, i) => (
            <rect
              key={i}
              x={m.left + slot * i}
              y={m.top}
              width={slot}
              height={h}
              fill="transparent"
              onMouseEnter={() => setHi(i)}
              onMouseLeave={() => setHi(null)}
            />
          ))}
        </svg>
      )}
      {hi !== null && width > 0 && (
        <Tip left={m.left + slot * (hi + 0.5)} width={width} title={timeFmtSec.format(times[hi])}>
          <div className="r">
            Calls <b className="num">{format(values[hi])}</b>
          </div>
          {errors && errors[hi] > 0 && (
            <div className="r">
              Errors <b className="num err">{format(errors[hi])}</b>
            </div>
          )}
        </Tip>
      )}
    </div>
  )
}

function TimeAxis({ times, x, y }: { times: number[]; x: (i: number) => number; y: number }) {
  const n = times.length
  const idx = n === 1 ? [0] : n === 2 ? [0, 1] : [0, Math.floor((n - 1) / 2), n - 1]
  const span = times[n - 1] - times[0]
  const fmt = span < 10 * 60_000 ? timeFmtSec : timeFmt
  return (
    <>
      {idx.map((i, k) => (
        <text
          key={i}
          className="axis"
          x={x(i)}
          y={y}
          textAnchor={idx.length > 1 && k === 0 ? 'start' : k === idx.length - 1 && idx.length > 1 ? 'end' : 'middle'}
        >
          {fmt.format(times[i])}
        </text>
      ))}
    </>
  )
}

function Tip({ left, width, title, children }: { left: number; width: number; title: string; children: ReactNode }) {
  const flip = left > width - 170
  return (
    <div className="chart-tip" style={flip ? { right: width - left + 10 } : { left: left + 10 }}>
      <div className="t">{title}</div>
      {children}
    </div>
  )
}
