import { useEffect, useState } from 'react'
import { api, type ImpactView, type Item, type RefNodeView } from '../api/client'
import { layerLabel } from './layerLabels'

interface Props {
  item: Item
  env: string
  reloadToken: number
}

function refLabel(n: RefNodeView) {
  return `${n.namespace_id}/${n.group_id}/${n.key}[${n.env}]`
}

export function ReferencePanel({ item, env, reloadToken }: Props) {
  const [outbound, setOutbound] = useState<RefNodeView[]>([])
  const [impact, setImpact] = useState<ImpactView | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    setErr('')
    Promise.all([api.itemRefs(item.id, env), api.impact(item.id, env)])
      .then(([refs, imp]) => {
        setOutbound(refs.references)
        setImpact(imp)
      })
      .catch((e) => setErr(e.message))
  }, [item.id, env, reloadToken])

  return (
    <div className="card">
      <h2>🔗 引用关系与改动影响</h2>
      {err && <div className="errors">{err}</div>}

      <h3 style={{ marginTop: 12 }}>引用去向（这个键引用了谁）</h3>
      {outbound.length === 0 ? (
        <p className="muted">当前环境值不包含任何引用占位符。</p>
      ) : (
        <ul>
          {outbound.map((n) => (
            <li key={`${n.item_id}-${n.env}`}>
              <code>{refLabel(n)}</code>{' '}
              <span className={`src-tag ${n.layer}`}>{layerLabel(n.layer)}</span>
            </li>
          ))}
        </ul>
      )}

      <h3 style={{ marginTop: 16 }}>改动影响预览（谁依赖这个键）</h3>
      {impact && impact.direct.length === 0 && impact.transitive.length === 0 ? (
        <p className="muted">没有其它键引用它，改动只影响本键订阅者。</p>
      ) : (
        <>
          <div className="muted" style={{ marginBottom: 6 }}>
            直接依赖：{impact?.direct.length ?? 0} · 传递闭包（爆炸半径）：
            {impact?.transitive.length ?? 0}
          </div>
          <table>
            <thead>
              <tr>
                <th>键</th>
                <th>位置</th>
                <th>环境</th>
                <th>层级</th>
                <th>关系</th>
              </tr>
            </thead>
            <tbody>
              {(impact?.transitive || []).map((n) => {
                const direct = impact?.direct.some((d) => d.item_id === n.item_id && d.env === n.env)
                return (
                  <tr key={`${n.item_id}-${n.env}`}>
                    <td>
                      <code>{n.key}</code>
                    </td>
                    <td>
                      {n.namespace_id}/{n.group_id}
                    </td>
                    <td>{n.env}</td>
                    <td>
                      <span className={`src-tag ${n.layer}`}>{layerLabel(n.layer)}</span>
                    </td>
                    <td>{direct ? '直接依赖' : '间接依赖'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}
