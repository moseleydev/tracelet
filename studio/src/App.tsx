import type { CSSProperties } from 'react'

// Placeholder shell for Tracelet Studio. The live topology, query explorer
// and plan views replace this in Phase 3 (see docs/ROADMAP.md).
export default function App() {
  return (
    <main style={styles.page}>
      <header style={styles.header}>
        <img src="/tracelet.svg" alt="" width={28} height={28} />
        <strong>Tracelet Studio</strong>
        <span style={styles.status}>
          <span style={styles.dot} /> waiting for queries
        </span>
      </header>

      <section style={styles.empty}>
        <h1 style={styles.title}>No traffic yet</h1>
        <p style={styles.muted}>
          Start the proxy and point your app at it:
        </p>
        <p>
          <code>tracelet up --db postgres://localhost:5432/app</code>
        </p>
        <p style={styles.muted}>
          Then change your app&apos;s database port to <code>6433</code>.
        </p>
      </section>
    </main>
  )
}

const styles: Record<string, CSSProperties> = {
  page: { minHeight: '100vh', display: 'flex', flexDirection: 'column' },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
    padding: '14px 20px',
    borderBottom: '1px solid var(--border)',
  },
  status: {
    marginLeft: 'auto',
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    color: 'var(--muted)',
    fontSize: 14,
  },
  dot: {
    width: 8,
    height: 8,
    borderRadius: '50%',
    background: 'var(--slow)',
  },
  empty: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    textAlign: 'center',
    padding: 16,
  },
  title: { margin: '0 0 8px', fontSize: 28 },
  muted: { color: 'var(--muted)', margin: '8px 0' },
}
