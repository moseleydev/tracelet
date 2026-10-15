import { useEffect, useRef, useState } from 'react'
import { fmtCount, fmtMs, nsToMs, speedLabel, speedOf, usToMs } from './api'
import type { LiveEvent, Speed, Stats, TableStats } from './api'
import { usePrefersReducedMotion } from './hooks'

interface Props {
  stats: Stats[]
  tables: TableStats[]
  subscribe: (l: (e: LiveEvent) => void) => () => void
  live: boolean
  selected: string | null
  onSelect: (hash: string) => void
  loaded: boolean
}

// ---------- scene model ----------

interface QueryNode {
  kind: 'query'
  hash: string | null // null for the "other queries" node
  stats: Stats | null
  label: string
  meta: string
  speed: Speed
  x: number
  y: number
  ty: number // target y; y eases toward it when the ranking changes
  w: number
  h: number
  heat: number
  tables: string[]
}

interface TableNode {
  kind: 'table'
  name: string
  stats: TableStats
  speed: Speed
  x: number
  y: number
  ty: number
  heat: number
}

interface Point {
  x: number
  y: number
}

interface Particle {
  segments: [Point, Point, Point, Point][] // cubic bezier control points
  seg: number
  t: number
  speed: Speed
  start: number
  onArrive: (seg: number) => void
}

interface Scene {
  width: number
  height: number
  app: Point & { w: number; h: number }
  queries: QueryNode[]
  byHash: Map<string, QueryNode>
  other: QueryNode | null
  tables: TableNode[]
  byTable: Map<string, TableNode>
}

const SEGMENT_MS = 650
const MAX_PARTICLES = 900
const NODE_H = 40

function bezier(a: Point, b: Point): [Point, Point, Point, Point] {
  const dx = Math.max(40, (b.x - a.x) * 0.5)
  return [a, { x: a.x + dx, y: a.y }, { x: b.x - dx, y: b.y }, b]
}

function at(c: [Point, Point, Point, Point], t: number): Point {
  const u = 1 - t
  const a = u * u * u
  const b = 3 * u * u * t
  const d = 3 * u * t * t
  const e = t * t * t
  return { x: a * c[0].x + b * c[1].x + d * c[2].x + e * c[3].x, y: a * c[0].y + b * c[1].y + d * c[2].y + e * c[3].y }
}

const ease = (t: number) => (t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2)

function truncate(ctx: CanvasRenderingContext2D, text: string, max: number): string {
  if (ctx.measureText(text).width <= max) return text
  let lo = 0
  let hi = text.length
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1
    if (ctx.measureText(text.slice(0, mid) + '…').width <= max) lo = mid
    else hi = mid - 1
  }
  return text.slice(0, lo) + '…'
}

const MONO = '12px ui-monospace, "SF Mono", Menlo, monospace'
const SANS = 'system-ui, -apple-system, "Segoe UI", sans-serif'

function buildScene(
  ctx: CanvasRenderingContext2D,
  width: number,
  height: number,
  stats: Stats[],
  tableStats: TableStats[],
  prev: Scene | null,
): Scene {
  const narrow = width < 640
  const top = 28
  const bottom = 64 // room for the legend
  const usable = Math.max(120, height - top - bottom)

  const maxQ = Math.max(3, Math.min(12, Math.floor(usable / (NODE_H + 12))))
  const shown = stats.slice(0, stats.length > maxQ ? maxQ - 1 : maxQ)
  const rest = stats.slice(shown.length)

  const app = { x: narrow ? 16 : Math.max(24, width * 0.04), y: top + usable / 2, w: narrow ? 44 : 96, h: 44 }
  const qx = app.x + app.w + (narrow ? 24 : Math.max(60, width * 0.08))
  const qw = Math.min(narrow ? width * 0.5 : 460, width * (narrow ? 0.5 : 0.42))
  const tx = Math.min(width - (narrow ? 90 : 170), qx + qw + (narrow ? 40 : Math.max(90, width * 0.12)))

  const n = shown.length + (rest.length ? 1 : 0)
  const gap = usable / Math.max(1, n)

  ctx.font = MONO
  const queries: QueryNode[] = shown.map((s, i) => {
    const old = prev?.byHash.get(s.fingerprint.hash)
    const ty = top + gap * (i + 0.5) - NODE_H / 2
    return {
      kind: 'query',
      hash: s.fingerprint.hash,
      stats: s,
      label: truncate(ctx, s.fingerprint.query.replace(/\s+/g, ' '), qw - 26),
      meta: `p95 ${fmtMs(nsToMs(s.p95))} · ${fmtCount(s.count)} calls · ${fmtMs(nsToMs(s.total))} total`,
      speed: speedOf(nsToMs(s.p95)),
      x: qx,
      y: old && prev?.width === width ? old.y : ty,
      ty,
      w: qw,
      h: NODE_H,
      heat: old?.heat ?? 0,
      tables: s.fingerprint.tables ?? [],
    }
  })

  let other: QueryNode | null = null
  if (rest.length) {
    const ty = top + gap * (n - 0.5) - NODE_H / 2
    const total = rest.reduce((s, q) => s + q.total, 0)
    const calls = rest.reduce((s, q) => s + q.count, 0)
    other = {
      kind: 'query',
      hash: null,
      stats: null,
      label: `${rest.length} more query shape${rest.length === 1 ? '' : 's'}`,
      meta: `${fmtCount(calls)} calls · ${fmtMs(nsToMs(total))} total`,
      speed: 'fast',
      x: qx,
      y: prev?.other && prev.width === width ? prev.other.y : ty,
      ty,
      w: qw,
      h: NODE_H,
      heat: prev?.other?.heat ?? 0,
      tables: [],
    }
    queries.push(other)
  }

  // Tables touched by the visible queries, busiest first.
  const wanted = new Set(queries.flatMap((q) => q.tables))
  const maxT = Math.max(3, Math.min(12, Math.floor(usable / 40)))
  const shownTables = tableStats.filter((t) => wanted.has(t.table)).slice(0, maxT)
  const tgap = usable / Math.max(1, shownTables.length)
  const worst = new Map<string, number>()
  for (const s of stats)
    for (const t of s.fingerprint.tables ?? []) worst.set(t, Math.max(worst.get(t) ?? 0, nsToMs(s.p95)))

  const tables: TableNode[] = shownTables.map((t, i) => {
    const old = prev?.byTable.get(t.table)
    const ty = top + tgap * (i + 0.5)
    return {
      kind: 'table',
      name: t.table,
      stats: t,
      speed: speedOf(worst.get(t.table) ?? 0),
      x: tx,
      y: old && prev?.width === width ? old.y : ty,
      ty,
      heat: old?.heat ?? 0,
    }
  })

  return {
    width,
    height,
    app,
    queries,
    byHash: new Map(queries.filter((q) => q.hash).map((q) => [q.hash as string, q])),
    other,
    tables,
    byTable: new Map(tables.map((t) => [t.name, t])),
  }
}

// ---------- component ----------

interface Hover {
  x: number
  y: number
  node: QueryNode | TableNode
}

export function Topology({ stats, tables, subscribe, live, selected, onSelect, loaded }: Props) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const sceneRef = useRef<Scene | null>(null)
  const particles = useRef<Particle[]>([])
  const pending = useRef<{ at: number; e: LiveEvent }[]>([])
  const hoverRef = useRef<QueryNode | TableNode | null>(null)
  const selectedRef = useRef(selected)
  const [hover, setHover] = useState<Hover | null>(null)
  const [size, setSize] = useState({ w: 0, h: 0 })
  const reduced = usePrefersReducedMotion()

  selectedRef.current = selected

  // Track container size.
  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const ro = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect
      setSize({ w: Math.round(width), h: Math.round(height) })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // Rebuild the scene when data or size changes, keeping node heat.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || !size.w || !size.h) return
    const dpr = window.devicePixelRatio || 1
    canvas.width = size.w * dpr
    canvas.height = size.h * dpr
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    const prev = sceneRef.current
    sceneRef.current = buildScene(ctx, size.w, size.h, stats, tables, prev)
    // Particles follow paths of the old layout; drop them if it moved.
    if (!prev || prev.width !== size.w || prev.height !== size.h) particles.current = []
  }, [stats, tables, size])

  // Queue live events, spread over the batch interval so they don't clump.
  useEffect(() => {
    if (!live) return
    return subscribe((e) => {
      pending.current.push({ at: performance.now() + Math.random() * 100, e })
      if (pending.current.length > 2000) pending.current.splice(0, pending.current.length - 2000)
    })
  }, [subscribe, live])

  // Animation loop.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    let raf = 0
    let last = performance.now()
    let colors = readColors(canvas)
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)')
    const onTheme = () => (colors = readColors(canvas))
    mq?.addEventListener('change', onTheme)

    const spawn = (scene: Scene, e: LiveEvent, now: number) => {
      const q = scene.byHash.get(e.h) ?? scene.other
      if (!q) return
      const speed = speedOf(usToMs(e.d))
      const entry = { x: q.x, y: q.y + q.h / 2 }
      const exit = { x: q.x + q.w, y: q.y + q.h / 2 }
      const segments: Particle['segments'] = [bezier({ x: scene.app.x + scene.app.w, y: scene.app.y }, entry)]
      const targets = (e.t ?? []).map((t) => scene.byTable.get(t)).filter((t): t is TableNode => !!t)
      const target = targets.length ? targets[Math.floor(Math.random() * targets.length)] : null
      if (target) segments.push(bezier(exit, { x: target.x - 8, y: target.y }))

      if (reduced || particles.current.length >= MAX_PARTICLES) {
        q.heat = Math.min(1, q.heat + 0.12)
        if (target) target.heat = Math.min(1, target.heat + 0.15)
        return
      }
      particles.current.push({
        segments,
        seg: 0,
        t: 0,
        speed,
        start: now,
        onArrive: (seg) => {
          if (seg === 0) q.heat = Math.min(1, q.heat + 0.12)
          else if (target) target.heat = Math.min(1, target.heat + 0.15)
        },
      })
    }

    const frame = (now: number) => {
      const dt = Math.min(100, now - last)
      last = now
      const scene = sceneRef.current
      if (scene) {
        // Release queued events whose time has come.
        const q = pending.current
        let i = 0
        while (i < q.length && q[i].at <= now) spawn(scene, q[i++].e, now)
        if (i) q.splice(0, i)

        // Advance particles.
        const alive: Particle[] = []
        for (const p of particles.current) {
          p.t += dt / SEGMENT_MS
          if (p.t >= 1) {
            p.onArrive(p.seg)
            p.seg++
            p.t = 0
          }
          if (p.seg < p.segments.length) alive.push(p)
        }
        particles.current = alive

        const decay = Math.exp(-dt / 900)
        const glide = reduced ? 1 : 1 - Math.exp(-dt / 180)
        for (const n of [...scene.queries, ...scene.tables]) {
          n.heat *= decay
          n.y += (n.ty - n.y) * glide
        }

        draw(ctx, scene, particles.current, colors, hoverRef.current, selectedRef.current)
      }
      raf = requestAnimationFrame(frame)
    }
    raf = requestAnimationFrame(frame)
    return () => {
      cancelAnimationFrame(raf)
      mq?.removeEventListener('change', onTheme)
    }
  }, [reduced])

  const hitTest = (x: number, y: number): QueryNode | TableNode | null => {
    const scene = sceneRef.current
    if (!scene) return null
    for (const q of scene.queries) {
      if (x >= q.x && x <= q.x + q.w && y >= q.y && y <= q.y + q.h) return q
    }
    for (const t of scene.tables) {
      if (Math.abs(y - t.y) <= 14 && x >= t.x - 14 && x <= t.x + 150) return t
    }
    return null
  }

  const pointer = (ev: React.MouseEvent) => {
    const rect = canvasRef.current?.getBoundingClientRect()
    return rect ? { x: ev.clientX - rect.left, y: ev.clientY - rect.top } : null
  }

  const onMove = (ev: React.MouseEvent) => {
    const p = pointer(ev)
    if (!p) return
    const { x, y } = p
    const node = hitTest(x, y)
    hoverRef.current = node
    if (canvasRef.current) canvasRef.current.style.cursor = node?.kind === 'query' && node.hash ? 'pointer' : 'default'
    setHover(node ? { x, y, node } : null)
  }

  const onClick = (ev: React.MouseEvent) => {
    const p = pointer(ev)
    const n = p ? hitTest(p.x, p.y) : null
    if (n?.kind === 'query' && n.hash) onSelect(n.hash)
    else if (n?.kind === 'table' && n.stats.fingerprints[0]) onSelect(n.stats.fingerprints[0])
  }

  const isEmpty = loaded && stats.length === 0

  return (
    <div className="topology" ref={wrapRef}>
      <canvas
        ref={canvasRef}
        onMouseMove={onMove}
        onMouseLeave={() => {
          hoverRef.current = null
          setHover(null)
        }}
        onClick={onClick}
        role="img"
        aria-label={`Topology: your app sends ${stats.length} query shapes to ${tables.length} tables. Use the Queries tab for a table view.`}
      />
      {isEmpty && (
        <div className="empty">
          <h2>Waiting for queries</h2>
          <p>Point your app at the Tracelet proxy and use it as usual.</p>
          <p>
            Want a demo? Run <code>go run ./examples/shop</code>
          </p>
        </div>
      )}
      {!isEmpty && (
        <div className="legend" aria-hidden>
          <span>Each dot is one query, colored by its latency:</span>
          {(['fast', 'slow', 'critical'] as Speed[]).map((s) => (
            <span key={s}>
              <i className="swatch" style={{ background: `var(--${s === 'fast' ? 'good' : s === 'slow' ? 'warning' : 'critical'})` }} />
              {speedLabel[s]} {s === 'fast' ? '<10ms' : s === 'slow' ? '10–100ms' : '≥100ms'}
            </span>
          ))}
        </div>
      )}
      {hover && <NodeTooltip hover={hover} width={size.w} />}
    </div>
  )
}

function NodeTooltip({ hover, width }: { hover: Hover; width: number }) {
  const n = hover.node
  const left = Math.min(hover.x + 16, width - 390)
  const style = { left: Math.max(8, left), top: hover.y + 16 }
  if (n.kind === 'table') {
    return (
      <div className="tooltip" style={style}>
        <code>{n.name}</code>
        <div className="row">
          Calls <b className="num">{fmtCount(n.stats.count)}</b>
        </div>
        <div className="row">
          Time spent <b className="num">{fmtMs(nsToMs(n.stats.total))}</b>
        </div>
        <div className="row">
          Query shapes <b className="num">{n.stats.fingerprints.length}</b>
        </div>
      </div>
    )
  }
  if (!n.stats) {
    return (
      <div className="tooltip" style={style}>
        <code>{n.label}</code>
        <div className="row">{n.meta}</div>
        <div className="row">See them all in the Queries tab.</div>
      </div>
    )
  }
  const s = n.stats
  return (
    <div className="tooltip" style={style}>
      <code>{s.fingerprint.query}</code>
      <div className="row">
        Calls <b className="num">{fmtCount(s.count)}</b>
      </div>
      <div className="row">
        Total time <b className="num">{fmtMs(nsToMs(s.total))}</b>
      </div>
      <div className="row">
        Mean / p95 <b className="num">{fmtMs(nsToMs(s.mean))} / {fmtMs(nsToMs(s.p95))}</b>
      </div>
      {s.errors > 0 && (
        <div className="row">
          Errors <b className="num err">{fmtCount(s.errors)}</b>
        </div>
      )}
      <div className="row dim">Click for details</div>
    </div>
  )
}

// ---------- drawing ----------

interface Colors {
  surface: string
  surface2: string
  border: string
  baseline: string
  text: string
  text2: string
  muted: string
  accent: string
  speed: Record<Speed, string>
  glow: boolean
}

function readColors(el: Element): Colors {
  const cs = getComputedStyle(el)
  const v = (name: string) => cs.getPropertyValue(name).trim()
  return {
    surface: v('--surface'),
    surface2: v('--surface-2'),
    border: v('--border'),
    baseline: v('--baseline'),
    text: v('--text'),
    text2: v('--text-2'),
    muted: v('--muted'),
    accent: v('--accent'),
    speed: { fast: v('--good'), slow: v('--warning'), critical: v('--critical') },
    glow: v('--particle-glow') === '1',
  }
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath()
  ctx.roundRect(x, y, w, h, r)
}

function draw(
  ctx: CanvasRenderingContext2D,
  s: Scene,
  particles: Particle[],
  c: Colors,
  hover: QueryNode | TableNode | null,
  selected: string | null,
) {
  ctx.clearRect(0, 0, s.width, s.height)
  const focusHash = hover?.kind === 'query' ? hover.hash : selected
  const focusTable = hover?.kind === 'table' ? hover.name : null

  // Edges.
  ctx.lineWidth = 1
  for (const q of s.queries) {
    const lit = (focusHash && q.hash === focusHash) || (focusTable && q.tables.includes(focusTable))
    ctx.strokeStyle = lit ? c.accent : c.baseline
    ctx.globalAlpha = lit ? 0.9 : 0.55
    const entry = { x: q.x, y: q.y + q.h / 2 }
    strokeCurve(ctx, bezier({ x: s.app.x + s.app.w, y: s.app.y }, entry))
    for (const name of q.tables) {
      const t = s.byTable.get(name)
      if (t) strokeCurve(ctx, bezier({ x: q.x + q.w, y: q.y + q.h / 2 }, { x: t.x - 8, y: t.y }))
    }
  }
  ctx.globalAlpha = 1

  // App node.
  roundRect(ctx, s.app.x, s.app.y - s.app.h / 2, s.app.w, s.app.h, 10)
  ctx.fillStyle = c.surface
  ctx.fill()
  ctx.strokeStyle = c.baseline
  ctx.stroke()
  ctx.fillStyle = c.text
  ctx.font = `600 13px ${SANS}`
  ctx.textAlign = 'center'
  ctx.textBaseline = 'middle'
  ctx.fillText(s.app.w > 60 ? 'Your app' : 'App', s.app.x + s.app.w / 2, s.app.y)

  // Query nodes.
  ctx.textAlign = 'left'
  for (const q of s.queries) {
    const isSel = q.hash !== null && q.hash === selected
    const isHover = hover === q
    roundRect(ctx, q.x, q.y, q.w, q.h, 8)
    ctx.fillStyle = c.surface
    ctx.fill()
    if (q.heat > 0.01) {
      ctx.globalAlpha = Math.min(0.28, q.heat * 0.3)
      ctx.fillStyle = c.speed[q.speed]
      ctx.fill()
      ctx.globalAlpha = 1
    }
    ctx.lineWidth = isSel ? 2 : 1
    ctx.strokeStyle = isSel ? c.accent : isHover ? c.text2 : c.baseline
    ctx.stroke()
    ctx.lineWidth = 1

    // Status bar, with the latency class also written in the meta line.
    if (q.stats) {
      roundRect(ctx, q.x + 6, q.y + 8, 3, q.h - 16, 2)
      ctx.fillStyle = c.speed[q.speed]
      ctx.fill()
    }
    ctx.font = MONO
    ctx.fillStyle = q.stats ? c.text : c.text2
    ctx.textBaseline = 'alphabetic'
    ctx.fillText(q.label, q.x + 16, q.y + 17)
    ctx.font = `11px ${SANS}`
    ctx.fillStyle = c.muted
    ctx.fillText(q.meta, q.x + 16, q.y + 32)
  }

  // Table nodes.
  for (const t of s.tables) {
    const lit = focusTable === t.name || (focusHash && s.byHash.get(focusHash)?.tables.includes(t.name))
    if (t.heat > 0.02) {
      ctx.beginPath()
      ctx.arc(t.x, t.y, 8 + t.heat * 10, 0, Math.PI * 2)
      ctx.globalAlpha = t.heat * 0.35
      ctx.fillStyle = c.speed[t.speed]
      ctx.fill()
      ctx.globalAlpha = 1
    }
    ctx.beginPath()
    ctx.arc(t.x, t.y, 7, 0, Math.PI * 2)
    ctx.fillStyle = c.surface
    ctx.fill()
    ctx.lineWidth = 2
    ctx.strokeStyle = c.speed[t.speed]
    ctx.stroke()
    ctx.lineWidth = 1
    ctx.font = `${lit ? '600 ' : ''}12px ui-monospace, "SF Mono", Menlo, monospace`
    ctx.fillStyle = lit ? c.text : c.text2
    ctx.textBaseline = 'middle'
    ctx.fillText(truncate(ctx, t.name, s.width - t.x - 24), t.x + 14, t.y - 7)
    ctx.font = `11px ${SANS}`
    ctx.fillStyle = c.muted
    ctx.fillText(`${fmtCount(t.stats.count)} calls`, t.x + 14, t.y + 8)
  }

  // Particles.
  for (const p of particles) {
    const pt = at(p.segments[p.seg], ease(p.t))
    const color = c.speed[p.speed]
    const r = p.speed === 'critical' ? 3.6 : 2.8
    if (c.glow) {
      ctx.beginPath()
      ctx.arc(pt.x, pt.y, r * 2.4, 0, Math.PI * 2)
      ctx.globalAlpha = 0.18
      ctx.fillStyle = color
      ctx.fill()
      ctx.globalAlpha = 1
    }
    ctx.beginPath()
    ctx.arc(pt.x, pt.y, r, 0, Math.PI * 2)
    ctx.fillStyle = color
    ctx.fill()
  }
}

function strokeCurve(ctx: CanvasRenderingContext2D, b: [Point, Point, Point, Point]) {
  ctx.beginPath()
  ctx.moveTo(b[0].x, b[0].y)
  ctx.bezierCurveTo(b[1].x, b[1].y, b[2].x, b[2].y, b[3].x, b[3].y)
  ctx.stroke()
}
