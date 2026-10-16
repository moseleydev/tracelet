import { useState } from 'react'
import { fmtCount, fmtMs, fmtSpeedup, nsToMs, severityLabel } from './api'
import type { Finding, Severity, Simulation } from './api'
import { SpeedChip } from './QueryTable'

export function SeverityChip({ severity }: { severity: Severity }) {
  return (
    <span className={`sev ${severity}`}>
      <span className="sev-icon" aria-hidden>
        {severity === 'info' ? 'i' : '!'}
      </span>
      {severityLabel[severity]}
    </span>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      className="icon-btn"
      onClick={async (e) => {
        e.stopPropagation()
        try {
          await navigator.clipboard.writeText(text)
          setCopied(true)
          window.setTimeout(() => setCopied(false), 1500)
        } catch {
          /* clipboard blocked; the text is selectable */
        }
      }}
    >
      {copied ? 'Copied' : label}
    </button>
  )
}

/** The result of testing a suggested index as a hypothetical index. */
export function WhatIf({ sim }: { sim: Simulation }) {
  if (sim.unavailable) {
    return <p className="whatif-note dim">No speedup estimate: {sim.unavailable}</p>
  }
  if (!sim.uses_index || !sim.speedup || !sim.cost_before || !sim.cost_after) {
    return (
      <div className="whatif">
        <div className="whatif-head">What-if</div>
        <p>
          With this index simulated, PostgreSQL still would not use it for this query. Check the suggestion before creating it.
        </p>
      </div>
    )
  }
  const after = Math.max(0.5, (sim.cost_after / sim.cost_before) * 100)
  return (
    <div className="whatif">
      <div className="whatif-head">
        What-if
        <span className="dim"> · simulated with a hypothetical index, nothing was created</span>
      </div>
      <p className="whatif-headline">
        About <b>{fmtSpeedup(sim.speedup)}</b> less work per call, by the planner’s estimate
      </p>
      <div className="whatif-bars num" role="img" aria-label={`Planner cost ${Math.round(sim.cost_before)} now, ${sim.cost_after.toFixed(1)} with the index`}>
        <span className="lbl">Now</span>
        <span className="track">
          <i className="now" style={{ width: '100%' }} />
        </span>
        <span className="val">{Math.round(sim.cost_before).toLocaleString()}</span>
        <span className="lbl">With index</span>
        <span className="track">
          <i className="after" style={{ width: `${after}%` }} />
        </span>
        <span className="val">{sim.cost_after.toFixed(sim.cost_after < 10 ? 1 : 0)}</span>
      </div>
      <p className="caption">
        Planner cost, not a measurement. Real speedups are usually smaller, because every call also has fixed costs such as the
        network round trip. Once the index exists, the latency chart shows the measured change.
      </p>
    </div>
  )
}

export function FindingBody({ f, compact = false }: { f: Finding; compact?: boolean }) {
  return (
    <>
      <p className="explain">{f.explanation}</p>
      <p className="suggest">
        <strong>What to do: </strong>
        {f.suggestion}
      </p>
      {f.simulation && <WhatIf sim={f.simulation} />}
      {f.fix && (
        <div className="fix">
          <div className="fix-head">
            <span>{compact ? 'Fix' : 'Suggested fix'}</span>
            <CopyButton text={f.fix} label="Copy SQL" />
          </div>
          <pre className="mono">{f.fix}</pre>
        </div>
      )}
    </>
  )
}

interface Props {
  findings: Finding[] | null
  analyze: boolean
  live: boolean
  onSelect: (hash: string) => void
}

export function Insights({ findings, analyze, live, onSelect }: Props) {
  if (!analyze && live) {
    return (
      <div className="insights">
        <div className="notice">
          The analyzer is off. Restart with <code>tracelet up</code> (without <code>--analyze=false</code>) to get suggestions.
        </div>
      </div>
    )
  }
  if (!findings) return <div className="insights dim">Loading…</div>
  if (findings.length === 0) {
    return (
      <div className="insights">
        <div className="notice">
          <strong>Nothing to fix so far.</strong> Tracelet explains your costliest queries every 15 seconds and checks them for missing
          indexes, full table scans and N+1 loops. Suggestions appear here as soon as it finds something.
        </div>
      </div>
    )
  }

  return (
    <div className="insights">
      <p className="intro dim">
        {findings.length} suggestion{findings.length === 1 ? '' : 's'}, most urgent first. Plans come from EXPLAIN, which plans queries
        without running them.
      </p>
      {findings.map((f) => (
        <article className={`card ${f.severity}`} key={`${f.hash}:${f.detector}`}>
          <header>
            <SeverityChip severity={f.severity} />
            <h3>{f.title}</h3>
            {f.simulation?.uses_index && (f.simulation.speedup ?? 0) > 0 && (
              <span className="speedup-chip num" title="Planner estimate with a hypothetical index (HypoPG)">
                ~{fmtSpeedup(f.simulation.speedup ?? 0)} less work (est.)
              </span>
            )}
          </header>
          <div className="impact-line num">
            <span className="share-bar" aria-hidden>
              <i style={{ width: `${Math.max(2, f.share_of_time * 100)}%` }} />
            </span>
            <span>
              <b>{(f.share_of_time * 100).toFixed(f.share_of_time < 0.1 ? 1 : 0)}%</b> of database time
            </span>
            <span className="dim">·</span>
            <span>{fmtCount(f.stats.count)} calls</span>
            <span className="dim">·</span>
            <span>
              p95 <SpeedChip ms={nsToMs(f.stats.p95)} />
            </span>
            <span className="dim">·</span>
            <span>{fmtMs(nsToMs(f.stats.total))} total</span>
          </div>
          <button className="query-link mono" onClick={() => onSelect(f.hash)} title="Open query details">
            {f.stats.fingerprint.query}
          </button>
          <FindingBody f={f} />
        </article>
      ))}
    </div>
  )
}
