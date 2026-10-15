import { useState } from 'react'
import { fmtCount, fmtMs, nsToMs, severityLabel } from './api'
import type { Finding, Severity } from './api'
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

export function FindingBody({ f, compact = false }: { f: Finding; compact?: boolean }) {
  return (
    <>
      <p className="explain">{f.explanation}</p>
      <p className="suggest">
        <strong>What to do: </strong>
        {f.suggestion}
      </p>
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
