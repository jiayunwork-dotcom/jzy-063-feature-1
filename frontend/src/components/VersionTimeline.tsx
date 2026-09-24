import { useEffect, useState } from 'react'
import { api, type DiffLine, type Item, type Version } from '../api/client'

interface Props {
  item: Item
  env: string
  reloadToken: number
  onRolledBack: () => void
}

export function VersionTimeline({ item, env, reloadToken, onRolledBack }: Props) {
  const [versions, setVersions] = useState<Version[]>([])
  const [from, setFrom] = useState<number | ''>('')
  const [to, setTo] = useState<number | ''>('')
  const [diff, setDiff] = useState<DiffLine[] | null>(null)
  const [busy, setBusy] = useState<number | null>(null)

  useEffect(() => {
    api.listVersions(item.id, env).then((r) => setVersions(r.versions)).catch(() => {})
  }, [item.id, env, reloadToken])

  const runDiff = async () => {
    if (from === '' || to === '') return
    const r = await api.diff(item.id, env, from, to)
    setDiff(r.lines)
  }

  const rollback = async (v: number) => {
    if (!confirm(`确认回退到版本 v${v}？回退本身会生成一条新版本，不删除中间历史。`)) return
    setBusy(v)
    try {
      await api.rollback(item.id, env, v)
      onRolledBack()
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="card">
      <h2>🕘 版本历史时间线 · {env}</h2>
      {versions.length === 0 && <p className="muted">该环境还没有任何版本。</p>}
      <div className="timeline">
        {versions.map((v) => (
          <div key={v.id} className={`tl-item ${v.change_type}`}>
            <div className="row" style={{ justifyContent: 'space-between' }}>
              <div>
                <span className="tl-ver">v{v.version}</span>{' '}
                <span className="badge group">{changeLabel(v.change_type)}</span>{' '}
                {v.note && <span className="muted">{v.note}</span>}
              </div>
              <div>
                <button
                  className="secondary"
                  onClick={() => rollback(v.version)}
                  disabled={busy === v.version || v.version === versions[0]?.version}
                  title={
                    v.version === versions[0]?.version ? '已经是最新版本' : '回退到这个版本'
                  }
                >
                  ↩ 回退
                </button>
              </div>
            </div>
            <pre
              style={{
                background: 'rgba(0,0,0,.25)',
                borderRadius: 6,
                padding: 8,
                fontSize: 12,
                maxHeight: 120,
                overflow: 'auto',
                margin: '6px 0',
              }}
            >
              {v.value}
            </pre>
            <div className="tl-meta">
              {v.operator} · {new Date(v.created_at).toLocaleString()}
            </div>
          </div>
        ))}
      </div>

      <h3>任意两版本行级对比</h3>
      <div className="row">
        <label>
          旧版{' '}
          <select value={from} onChange={(e) => setFrom(Number(e.target.value))}>
            <option value="">选择</option>
            {versions.map((v) => (
              <option key={v.id} value={v.version}>
                v{v.version}
              </option>
            ))}
          </select>
        </label>
        <label>
          新版{' '}
          <select value={to} onChange={(e) => setTo(Number(e.target.value))}>
            <option value="">选择</option>
            {versions.map((v) => (
              <option key={v.id} value={v.version}>
                v{v.version}
              </option>
            ))}
          </select>
        </label>
        <button className="secondary" onClick={runDiff} disabled={from === '' || to === ''}>
          对比
        </button>
      </div>
      {diff && (
        <pre className="diff" style={{ marginTop: 10 }}>
          {diff.map((l, i) => (
            <DiffRow key={i} line={l} />
          ))}
        </pre>
      )}
    </div>
  )
}

function DiffRow({ line }: { line: DiffLine }) {
  if (line.op === 'equal') return <div className="diff-line equal">  {line.left}</div>
  if (line.op === 'add') return <div className="diff-line add">+ {line.right}</div>
  if (line.op === 'delete') return <div className="diff-line del">- {line.left}</div>
  return (
    <>
      <div className="diff-line change">- {line.left}</div>
      <div className="diff-line change">+ {line.right}</div>
    </>
  )
}

function changeLabel(t: string): string {
  switch (t) {
    case 'create':
      return '创建'
    case 'update':
      return '更新'
    case 'rollback':
      return '回退'
    case 'gray_rollback':
      return '灰度撤回'
    default:
      return t
  }
}
