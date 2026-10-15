import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from './api'
import type { Finding, Info, LiveBatch, LiveEvent, Stats, TableStats, WindowId } from './api'

/** Polls a fetcher every `ms` while mounted, keeping the last good value. */
export function usePoll<T>(fetcher: () => Promise<T>, deps: unknown[], ms = 2000) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined
    const tick = async () => {
      try {
        const v = await fetcher()
        if (!cancelled) {
          setData(v)
          setError(null)
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      }
      if (!cancelled) timer = window.setTimeout(tick, ms)
    }
    tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, error }
}

export function useInfo() {
  return usePoll<Info>(api.info, [], 10_000)
}

export function useStats(win: WindowId) {
  const top = usePoll<Stats[]>(() => api.top(win), [win])
  const tables = usePoll<TableStats[]>(() => api.tables(win), [win])
  return { top: top.data, tables: tables.data, error: top.error ?? tables.error }
}

export function useFindings(win: WindowId) {
  return usePoll<Finding[]>(() => api.findings(win), [win], 5000)
}

export type LiveStatus = 'connecting' | 'live' | 'offline' | 'disconnected'

export interface LiveMetrics {
  qps: number // queries per second over the last 5s
  p95: number // ms, over live events in the last 10s
  errors: number // in the last 60s
}

type Listener = (e: LiveEvent) => void

/**
 * Subscribes to the live query stream. Events go to listeners (the topology
 * animation) without causing React renders; summary metrics update once a
 * second.
 */
export function useLive() {
  const [status, setStatus] = useState<LiveStatus>('connecting')
  const [metrics, setMetrics] = useState<LiveMetrics>({ qps: 0, p95: 0, errors: 0 })
  const listeners = useRef(new Set<Listener>())

  useEffect(() => {
    const totals: { at: number; n: number }[] = []
    const recent: { at: number; ms: number; err: boolean }[] = []
    const es = new EventSource('/api/live')

    es.onopen = () => setStatus('live')
    es.addEventListener('offline', () => {
      setStatus('offline')
      es.close()
    })
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) setStatus((s) => (s === 'offline' ? s : 'disconnected'))
      else setStatus('connecting') // the browser retries automatically
    }
    es.onmessage = (msg) => {
      const batch = JSON.parse(msg.data) as LiveBatch
      const now = Date.now()
      totals.push({ at: now, n: batch.total })
      for (const e of batch.events) {
        recent.push({ at: now, ms: e.d / 1000, err: !!e.e })
        for (const l of listeners.current) l(e)
      }
    }

    const timer = window.setInterval(() => {
      const now = Date.now()
      while (totals.length && totals[0].at < now - 5000) totals.shift()
      while (recent.length && recent[0].at < now - 60_000) recent.shift()
      const n = totals.reduce((s, t) => s + t.n, 0)
      const last10 = recent.filter((r) => r.at >= now - 10_000).map((r) => r.ms).sort((a, b) => a - b)
      setMetrics({
        qps: n / 5,
        p95: last10.length ? last10[Math.min(last10.length - 1, Math.ceil(last10.length * 0.95) - 1)] : 0,
        errors: recent.filter((r) => r.err).length,
      })
    }, 1000)

    return () => {
      es.close()
      window.clearInterval(timer)
    }
  }, [])

  const subscribe = useCallback((l: Listener) => {
    listeners.current.add(l)
    return () => {
      listeners.current.delete(l)
    }
  }, [])

  return { status, metrics, subscribe }
}

export function usePrefersReducedMotion() {
  const [reduced, setReduced] = useState(
    () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false,
  )
  useEffect(() => {
    const mq = window.matchMedia?.('(prefers-reduced-motion: reduce)')
    if (!mq) return
    const on = () => setReduced(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return reduced
}
