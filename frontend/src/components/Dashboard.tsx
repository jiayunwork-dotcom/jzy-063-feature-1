import { useEffect, useState } from 'react'
import { api, type InstanceView, type NamespaceStat } from '../api/client'

interface Props {
  namespace: string
  tick: number
}

export function Dashboard({ namespace, tick }: Props) {
  const [stats, setStats] = useState<NamespaceStat[]>([])
  const [instances, setInstances] = useState<InstanceView[]>([])

  useEffect(() => {
    api.stats().then((r) => setStats(r.namespaces)).catch(() => {})
    api
      .instances(namespace)
      .then((r) => setInstances(r.instances))
      .catch(() => setInstances([]))
  }, [namespace, tick])

  const mine = stats.find((s) => s.namespace_id === namespace)

  return (
    <div className="card">
      <h2>📡 连接看板</h2>
      <div className="stat-grid">
        <div className="stat-tile">
          <div className="num">{mine?.connections ?? 0}</div>
          <div className="lbl">{namespace} 当前连接数</div>
        </div>
        <div className="stat-tile">
          <div className="num">{instances.length}</div>
          <div className="lbl">在线实例数（去重）</div>
        </div>
        <div className="stat-tile">
          <div className="num">v{mine?.last_version ?? 0}</div>
          <div className="lbl">最近推送版本</div>
        </div>
        <div className="stat-tile">
          <div className="num" style={{ fontSize: 14, lineHeight: '26px' }}>
            {mine?.last_push_at ? new Date(mine.last_push_at).toLocaleTimeString() : '—'}
          </div>
          <div className="lbl">最近推送时间 {mine?.last_push_env ? `(${mine.last_push_env})` : ''}</div>
        </div>
      </div>

      <h3>各命名空间连接与最近推送</h3>
      <table>
        <thead>
          <tr>
            <th>命名空间</th>
            <th>连接数</th>
            <th>最近推送环境</th>
            <th>最近版本</th>
            <th>推送时间</th>
          </tr>
        </thead>
        <tbody>
          {stats.length === 0 && (
            <tr>
              <td colSpan={5} className="muted">
                暂无客户端连接（可通过 /api/poll 长轮询或 /api/ws 订阅）。
              </td>
            </tr>
          )}
          {stats.map((s) => (
            <tr key={s.namespace_id}>
              <td>{s.namespace_id}</td>
              <td>{s.connections}</td>
              <td>{s.last_push_env || '—'}</td>
              <td>{s.last_version ? `v${s.last_version}` : '—'}</td>
              <td>{s.last_push_at ? new Date(s.last_push_at).toLocaleString() : '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>

      {instances.length > 0 && (
        <>
          <h3>{namespace} 在线实例</h3>
          <table>
            <thead>
              <tr>
                <th>实例 ID</th>
                <th>IP</th>
                <th>通道</th>
                <th>连接时间</th>
              </tr>
            </thead>
            <tbody>
              {instances.map((i) => (
                <tr key={i.instance_id}>
                  <td>{i.instance_id}</td>
                  <td>{i.ip}</td>
                  <td>{i.websocket ? 'WebSocket' : '长轮询'}</td>
                  <td>{new Date(i.connected_at).toLocaleTimeString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}
