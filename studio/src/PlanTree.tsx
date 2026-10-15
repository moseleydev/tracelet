import { fmtCount } from './api'
import type { PlanNode, PlanRecord } from './api'

const BIG_TABLE = 10_000

/** Renders an EXPLAIN plan as an indented tree, costliest steps emphasized. */
export function PlanTree({ record }: { record: PlanRecord | null | undefined }) {
  if (record === undefined) return <p className="dim">Loading…</p>
  if (record === null) {
    return <p className="dim">Not explained yet. Tracelet explains the costliest queries first, a few every 15 seconds.</p>
  }
  if (record.error || !record.plan) {
    return (
      <p className="dim">
        Couldn’t explain this query: <span className="mono">{record.error}</span>
      </p>
    )
  }
  const root = record.plan
  return (
    <div className="plan" role="tree" aria-label="Query plan">
      <Node node={root} rootCost={root.total_cost || 1} depth={0} />
      <p className="caption">
        Estimated by the planner {new Date(record.explained_at).toLocaleTimeString()}. Costs are relative; the bar shows each
        step’s share.
      </p>
    </div>
  )
}

function Node({ node, rootCost, depth }: { node: PlanNode; rootCost: number; depth: number }) {
  const share = Math.min(1, node.total_cost / rootCost)
  const fullScan = node.node === 'Seq Scan' && (node.table_rows ?? 0) >= BIG_TABLE
  const target = node.relation ? `${node.relation}${node.index ? ` using ${node.index}` : ''}` : node.index
  return (
    <div className="plan-node" role="treeitem" style={{ paddingLeft: depth * 16 }}>
      <div className={`plan-row ${fullScan ? 'hot' : ''}`}>
        <div className="plan-main">
          <span className="plan-op">{node.node}</span>
          {target && <span className="mono plan-target">{target}</span>}
          {fullScan && <span className="plan-flag">Full table scan</span>}
        </div>
        <div className="plan-meta num">
          <span>~{fmtCount(node.est_rows)} rows out</span>
          {node.table_rows !== undefined && node.table_rows > 0 && <span>table ~{fmtCount(node.table_rows)} rows</span>}
          <span className="plan-cost" aria-label={`${Math.round(share * 100)}% of cost`}>
            <i style={{ width: `${Math.max(2, share * 100)}%` }} />
          </span>
        </div>
        {(node.filter || node.index_cond) && (
          <div className="plan-detail mono">{node.index_cond ? `Index cond: ${node.index_cond}` : `Filter: ${node.filter}`}</div>
        )}
        {node.sort_key && <div className="plan-detail mono">Sort: {node.sort_key.join(', ')}</div>}
      </div>
      {node.children?.map((c, i) => <Node key={i} node={c} rootCost={rootCost} depth={depth + 1} />)}
    </div>
  )
}
