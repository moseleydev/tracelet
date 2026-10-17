import { useEffect, useState } from 'react'
import { api, fmtCount } from './api'
import type { RecordState } from './api'
import { usePoll } from './hooks'
import { CopyButton } from './Insights'

function fmtElapsed(since: string) {
  const s = Math.max(0, Math.floor((Date.now() - new Date(since).getTime()) / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

function fmtBytes(n: number) {
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

const replayCommand = (path: string) => `tracelet replay ${path} --target postgres://…/staging`

/** Record button and recorded sessions, shown when a proxy is running. */
export function Recording() {
  const polled = usePoll(api.record, [], 1000)
  const [state, setState] = useState<RecordState | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  const [, tick] = useState(0)

  useEffect(() => {
    if (polled.data) setState(polled.data)
  }, [polled.data])

  const recording = state?.status.recording ?? false
  useEffect(() => {
    if (!recording) return
    const t = window.setInterval(() => tick((n) => n + 1), 1000)
    return () => window.clearInterval(t)
  }, [recording])

  if (!state?.available) return null
  const current = state.status.current

  const toggle = async () => {
    setBusy(true)
    setError(null)
    try {
      const next = await api.setRecording(recording ? 'stop' : 'start')
      setState(next)
      if (recording) setOpen(true) // show the new session and how to replay it
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="recording">
      <button
        className={`rec-btn ${recording ? 'on' : ''}`}
        onClick={toggle}
        disabled={busy}
        title={recording ? 'Stop recording' : 'Record traffic to replay later. Session files contain query values.'}
      >
        <span className="rec-dot" aria-hidden />
        {recording && current ? (
          <span className="num">
            Stop · {fmtElapsed(current.started)} · {fmtCount(current.entries)} queries
          </span>
        ) : (
          'Record'
        )}
      </button>
      {state.sessions.length > 0 && (
        <button className="icon-btn" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          Sessions ({state.sessions.length})
        </button>
      )}
      {error && <span className="err">{error}</span>}
      {open && (
        <div className="sessions-pop" role="dialog" aria-label="Recorded sessions">
          <header>
            <strong>Recorded sessions</strong>
            <button className="icon-btn" onClick={() => setOpen(false)}>
              Close
            </button>
          </header>
          <p className="dim">
            Session files contain real query values, so treat them like a database dump. Replay against a staging copy: read mode
            (the default) can't write, and <code>--mode rollback</code> rolls every write back. Add <code>--scale 10</code> to
            simulate 10× the traffic.
          </p>
          <ul>
            {state.sessions.map((s) => (
              <li key={s.path}>
                <div className="sess-meta">
                  <span className="mono">{s.name}</span>
                  <span className="dim num">
                    {fmtBytes(s.size)} · {new Date(s.modified).toLocaleString()}
                  </span>
                </div>
                <div className="fix">
                  <div className="fix-head">
                    <span>Replay</span>
                    <CopyButton text={replayCommand(s.path)} />
                  </div>
                  <pre className="mono">{replayCommand(s.path)}</pre>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
