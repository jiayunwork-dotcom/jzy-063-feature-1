import { useEffect, useState } from 'react'
import { api, type EffectiveSnapshot } from '../api/client'
import { layerLabel } from './layerLabels'

interface Props {
  namespace: string
  group: string
  env: string
  tick: number
}

export function EffectiveView({ namespace, group, env, tick }: Props) {
  const [snap, setSnap] = useState<EffectiveSnapshot | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    if (!group || group === '_defaults') {
      setSnap(null)
      return
    }
    api
      .effective(namespace, group, env)
      .then(setSnap)
      .catch((e) => setErr(e.message))
  }, [namespace, group, env, tick])

  if (!group || group === '_defaults') {
    return (
      <div className="card">
        <h2>🧬 三级合并结果与来源溯源</h2>
        <p className="muted">请选择一个具体的业务分组，查看 公共层 → 命名空间层 → 分组层 的合并生效值。</p>
      </div>
    )
  }

  const entries = snap?.entries || []

  return (
    <div className="card">
      <h2>
        🧬 合并生效值 <span className="muted">/{namespace}/{group} · {env} · 快照 v{snap?.version ?? 0}</span>
      </h2>
      {err && <div className="errors">{err}</div>}
      {entries.length === 0 && <p className="muted">该分组在此环境下暂无生效配置。</p>}
      {entries.map((e) => (
        <div key={e.key} style={{ marginBottom: 16 }}>
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <strong>
              {e.key} <span className="muted">({e.format})</span>
            </strong>
            <span className={`src-tag ${e.source}`}>
              主键来源：{layerLabel(e.source)} v{e.source_version}
            </span>
          </div>
          <div className="editor-line-wrap" style={{ marginTop: 6 }}>
            <pre
              style={{
                margin: 0,
                padding: '8px 12px',
                fontFamily: 'ui-monospace, Menlo, monospace',
                fontSize: 12.5,
                lineHeight: 1.55,
                width: '100%',
                overflow: 'auto',
              }}
            >
              {e.value}
            </pre>
          </div>
          <table style={{ marginTop: 6 }}>
            <thead>
              <tr>
                <th>叶子字段</th>
                <th>来源层</th>
                <th>来源版本</th>
              </tr>
            </thead>
            <tbody>
              {e.fields.map((f) => (
                <tr key={f.path}>
                  <td>
                    <code>{f.path}</code>
                  </td>
                  <td>
                    <span className={`src-tag ${f.source}`}>{layerLabel(f.source)}</span>
                  </td>
                  <td>v{f.source_version}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
    </div>
  )
}
