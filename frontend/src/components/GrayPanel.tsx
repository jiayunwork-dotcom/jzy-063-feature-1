import { useEffect, useRef, useState } from 'react'
import { api, type Release, type ReleaseView } from '../api/client'

interface Props {
  namespace: string
  reloadToken: number
  onChanged: () => void
}

function duration(start: string, end?: string): string {
  const ms = new Date(end || Date.now()).getTime() - new Date(start).getTime()
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m${s % 60}s`
}

export function GrayPanel({ namespace, reloadToken, onChanged }: Props) {
  const [releases, setReleases] = useState<Release[]>([])
  const [views, setViews] = useState<Record<string, ReleaseView>>({})
  const [, forceTick] = useState(0)
  const timer = useRef<number>()

  const load = async () => {
    const r = await api.listReleases(namespace).catch(() => ({ releases: [] }))
    setReleases(r.releases)
    const active = r.releases.filter((x) => x.status === 'gray')
    const entries = await Promise.all(
      active.map((x) => api.getRelease(x.id).then((v) => [x.id, v] as const).catch(() => null)),
    )
    const map: Record<string, ReleaseView> = {}
    entries.forEach((e) => e && (map[e[0]] = e[1]))
    setViews(map)
  }

  useEffect(() => {
    load()
    timer.current = window.setInterval(() => {
      forceTick((n) => n + 1)
      load()
    }, 1500)
    return () => window.clearInterval(timer.current)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [namespace, reloadToken])

  const promote = async (id: string) => {
    if (!confirm('确认灰度验证通过，全量推送给所有实例？')) return
    await api.promote(id)
    onChanged()
  }
  const recall = async (id: string) => {
    if (!confirm('发现问题？将撤回灰度：旧值会作为新版本下发，未入灰度的实例不受影响。')) return
    await api.rollbackGray(id)
    onChanged()
  }

  if (releases.length === 0) return null

  return (
    <div className="card">
      <h2>🧪 灰度操作面板</h2>
      <table>
        <thead>
          <tr>
            <th>状态</th>
            <th>环境</th>
            <th>版本</th>
            <th>策略</th>
            <th>下发/总数</th>
            <th>开始时间 / 持续</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {releases.map((r) => {
            const view = views[r.id]
            return (
              <tr key={r.id}>
                <td>
                  <span className={`badge ${r.status}`}>{statusLabel(r.status)}</span>
                </td>
                <td>{r.env}</td>
                <td>
                  v{r.version} <span className="muted">(从 v{r.prev_version})</span>
                </td>
                <td>
                  {r.strategy === 'ip'
                    ? `IP 名单 (${r.ips?.length ?? 0})`
                    : `百分比 ${r.percent}%`}
                </td>
                <td>
                  {r.status === 'gray' && view
                    ? `${view.delivered_count} / ${view.total_instances}`
                    : '—'}
                </td>
                <td>
                  {new Date(r.started_at).toLocaleTimeString()}
                  <br />
                  <span className="muted">
                    {duration(
                      r.started_at,
                      r.status === 'gray' ? undefined : r.ended_at || r.promoted_at,
                    )}
                  </span>
                </td>
                <td>
                  {r.status === 'gray' && (
                    <>
                      <button onClick={() => promote(r.id)}>✅ 全量推送</button>
                      <button className="danger" onClick={() => recall(r.id)}>
                        ↩ 灰度回退
                      </button>
                    </>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function statusLabel(s: string): string {
  return s === 'gray' ? '灰度中' : s === 'promoted' ? '已全量' : '已撤回'
}
