import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { fetchHealth, fetchSnapshot, type Health, type Snapshot } from './api.ts'
import { bytes, percent, rate } from './format.ts'

// A one-shot view of the latest snapshot. Live updates over the WebSocket
// stream come in #13.
export default function App() {
  const [snap, setSnap] = useState<Snapshot | null>(null)
  const [health, setHealth] = useState<Health | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback((signal?: AbortSignal) => {
    Promise.all([fetchSnapshot(signal), fetchHealth(signal)])
      .then(([s, h]) => {
        setSnap(s)
        setHealth(h)
        setError(null)
      })
      .catch((e: unknown) => {
        if (signal?.aborted) return
        setError(e instanceof Error ? e.message : String(e))
      })
  }, [])

  useEffect(() => {
    const ctrl = new AbortController()
    load(ctrl.signal)
    return () => ctrl.abort()
  }, [load])

  return (
    <main>
      <header>
        <h1>piway</h1>
        {health && <span className="muted">{health.version}</span>}
        <button type="button" onClick={() => load()}>
          Refresh
        </button>
      </header>

      {error && <p className="error">{error}</p>}
      {!snap && !error && <p className="muted">Loading…</p>}
      {snap && <SnapshotView s={snap} />}
    </main>
  )
}

function SnapshotView({ s }: { s: Snapshot }) {
  const temp = s.thermal?.temp_celsius
  const throttled = s.thermal?.throttle?.now
  const root = s.disk?.filesystems.find((f) => f.path === '/')

  return (
    <>
      <p className="muted">
        Round #{s.seq} at {new Date(s.time).toLocaleTimeString()}
      </p>

      <section className="cards">
        <Card title="CPU" value={s.cpu ? percent(s.cpu.total) : null}>
          {s.cpu && <span>{s.cpu.per_core.map((c) => percent(c)).join(' · ')}</span>}
        </Card>
        <Card title="Memory" value={s.memory ? bytes(s.memory.used_bytes) : null}>
          {s.memory && <span>of {bytes(s.memory.total_bytes)}</span>}
        </Card>
        <Card title="Temperature" value={temp != null ? `${temp.toFixed(1)} °C` : null}>
          {throttled && (throttled.throttled || throttled.under_voltage) && (
            <span className="error">
              {throttled.under_voltage ? 'Under-voltage' : 'Throttled'}
            </span>
          )}
        </Card>
        <Card title="Disk /" value={root ? percent(root.used_percent) : null}>
          {root && <span>{bytes(root.avail_bytes)} free</span>}
        </Card>
      </section>

      {s.network && s.network.length > 0 && (
        <section>
          <h2>Network</h2>
          <table>
            <thead>
              <tr>
                <th>Interface</th>
                <th>Receive</th>
                <th>Send</th>
              </tr>
            </thead>
            <tbody>
              {s.network.map((n) => (
                <tr key={n.name}>
                  <td>{n.name}</td>
                  <td>{rate(n.rx_bytes_per_sec)}</td>
                  <td>{rate(n.tx_bytes_per_sec)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      {s.errors && (
        <section>
          <h2>Collector errors</h2>
          <ul>
            {Object.entries(s.errors).map(([name, msg]) => (
              <li key={name}>
                <strong>{name}</strong>: <span className="error">{msg}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  )
}

function Card(props: { title: string; value: string | null; children?: ReactNode }) {
  return (
    <div className="card">
      <h2>{props.title}</h2>
      {/* null: unavailable, failed, or no baseline yet (first round) */}
      <div className="value">{props.value ?? '—'}</div>
      <div className="muted">{props.children}</div>
    </div>
  )
}
