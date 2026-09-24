import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  api,
  type Format,
  type Group,
  type Item,
  type Namespace,
  type Tenant,
} from './api/client'
import { TreeNav, type Selection } from './components/TreeNav'
import { ConfigEditor } from './components/ConfigEditor'
import { VersionTimeline } from './components/VersionTimeline'
import { GrayPanel } from './components/GrayPanel'
import { Dashboard } from './components/Dashboard'
import { EffectiveView } from './components/EffectiveView'
import { FORMATS } from './components/layerLabels'

const ENVS = ['dev', 'staging', 'prod']
const PUBLIC_NS = '_public'

export default function App() {
  const [tenants, setTenants] = useState<Tenant[]>([])
  const [tenantId, setTenantId] = useState(localStorage.getItem('cc.tenant') || 'demo')
  const [namespaces, setNamespaces] = useState<Namespace[]>([])
  const [groupsByNs, setGroupsByNs] = useState<Record<string, Group[]>>({})
  const [itemsByGroup, setItemsByGroup] = useState<Record<string, Item[]>>({})
  const [selection, setSelection] = useState<Selection | null>(null)
  const [env, setEnv] = useState('dev')
  const [tab, setTab] = useState<'edit' | 'effective' | 'gray' | 'dashboard'>('edit')
  const [live, setLive] = useState(false)
  const [tick, setTick] = useState(0)
  const [toast, setToast] = useState('')

  const showToast = (m: string) => {
    setToast(m)
    setTimeout(() => setToast(''), 2500)
  }

  // ---- bootstrap tenants ----
  useEffect(() => {
    api.listTenants().then((r) => setTenants(r.tenants)).catch(() => {})
  }, [])

  const reloadAll = useCallback(async () => {
    const nsResp = await api.listNamespaces()
    setNamespaces(nsResp.namespaces)
    const groupsMap: Record<string, Group[]> = {}
    const itemsMap: Record<string, Item[]> = {}
    await Promise.all(
      nsResp.namespaces.map(async (ns) => {
        const g = await api.listGroups(ns.id)
        groupsMap[ns.id] = g.groups
        await Promise.all(
          g.groups.map(async (gr) => {
            const it = await api.listItems(ns.id, gr.id)
            itemsMap[`${ns.id}/${gr.id}`] = it.items
          }),
        )
      }),
    )
    setGroupsByNs(groupsMap)
    setItemsByGroup(itemsMap)
  }, [])

  useEffect(() => {
    localStorage.setItem('cc.tenant', tenantId)
    reloadAll().catch((e) => showToast(e.message))
  }, [tenantId, reloadAll])

  // ---- realtime websocket (dashboard-ish namespace subscription) ----
  useEffect(() => {
    if (!selection?.namespace) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(
      `${proto}://${location.host}/api/ws?namespace=${encodeURIComponent(selection.namespace)}`,
    )
    ws.onopen = () => setLive(true)
    ws.onclose = () => setLive(false)
    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data)
        if (msg.type === 'snapshot') return
        setTick((n) => n + 1)
        showToast(`收到实时变更：${msg.key || msg.item_id} v${msg.version}`)
        reloadAll()
      } catch {
        /* ignore */
      }
    }
    return () => ws.close()
  }, [selection?.namespace, reloadAll])

  const selectedItem = useMemo(() => {
    if (!selection?.itemId) return null
    for (const list of Object.values(itemsByGroup)) {
      const found = list.find((i) => i.id === selection.itemId)
      if (found) return found
    }
    return null
  }, [selection, itemsByGroup])

  // ---- mutations ----
  const addNamespace = async () => {
    const id = prompt('新命名空间 ID（对应业务线，如 trade）')
    if (!id) return
    const name = prompt('显示名称', id) || id
    try {
      await api.createNamespace(id, name)
      await reloadAll()
      showToast(`命名空间 ${id} 已创建`)
    } catch (e) {
      showToast((e as Error).message)
    }
  }

  const addGroup = async (ns: string) => {
    const id = prompt('新分组 ID（对应服务模块）')
    if (!id) return
    const name = prompt('显示名称', id) || id
    try {
      await api.createGroup(ns, id, name)
      await reloadAll()
      showToast(`分组 ${id} 已创建`)
    } catch (e) {
      showToast((e as Error).message)
    }
  }

  const addItem = async () => {
    if (!selection) return
    const key = prompt('配置项 Key')
    if (!key) return
    const fmt = prompt('格式：json / yaml / properties / toml', 'json') as Format
    if (!FORMATS.some((f) => f.value === fmt)) {
      showToast('不支持的格式')
      return
    }
    try {
      const item = await api.createItem(selection.namespace, selection.group, key, fmt)
      await reloadAll()
      setSelection({ ...selection, itemId: item.id })
    } catch (e) {
      showToast((e as Error).message)
    }
  }

  return (
    <div className="app">
      <div className="topbar">
        <h1>⚙️ 配置治理平台</h1>
        <select value={tenantId} onChange={(e) => setTenantId(e.target.value)}>
          {tenants.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}（{t.id}）
            </option>
          ))}
        </select>
        <span className={live ? 'ws-live' : 'ws-off'}>
          {live ? '🟢 WebSocket 实时通道已连接' : '⚪ 实时通道未连接'}
        </span>
        <div className="spacer" />
        <label className="muted">
          环境{' '}
          <select value={env} onChange={(e) => setEnv(e.target.value)}>
            {ENVS.map((e) => (
              <option key={e}>{e}</option>
            ))}
          </select>
        </label>
        <label className="muted">
          操作者 <input
            defaultValue={localStorage.getItem('cc.operator') || 'admin'}
            onChange={(e) => localStorage.setItem('cc.operator', e.target.value)}
            style={{ width: 100 }}
          />
        </label>
      </div>

      <div className="layout">
        <aside className="sidebar">
          <TreeNav
            namespaces={namespaces}
            groupsByNs={groupsByNs}
            itemsByGroup={itemsByGroup}
            selection={selection}
            onSelect={(s) => {
              setSelection(s)
              setTab(s.itemId ? 'edit' : s.namespace === PUBLIC_NS ? 'edit' : 'dashboard')
            }}
            onAddNamespace={addNamespace}
            onAddGroup={addGroup}
          />
        </aside>

        <main className="main">
          {toast && (
            <div
              style={{
                position: 'fixed',
                top: 56,
                right: 20,
                background: '#0ea5e9',
                color: '#0f172a',
                padding: '8px 14px',
                borderRadius: 8,
                fontWeight: 600,
                zIndex: 100,
              }}
            >
              {toast}
            </div>
          )}

          {!selection && (
            <div className="card">
              <h2>欢迎使用</h2>
              <p className="muted">
                左侧按「命名空间 → 分组 → 配置项」三级组织浏览。选择业务分组可查看合并生效值与连接看板；选择具体配置项可编辑、查看版本时间线并发起灰度。
              </p>
            </div>
          )}

          {selection && (
            <>
              <div className="tabs">
                {selection.itemId && (
                  <div className={`tab ${tab === 'edit' ? 'active' : ''}`} onClick={() => setTab('edit')}>
                    配置编辑
                  </div>
                )}
                {selection.itemId && (
                  <div className={`tab ${tab === 'gray' ? 'active' : ''}`} onClick={() => setTab('gray')}>
                    灰度面板
                  </div>
                )}
                {selection.group !== '_defaults' && (
                  <div
                    className={`tab ${tab === 'effective' ? 'active' : ''}`}
                    onClick={() => setTab('effective')}
                  >
                    合并结果
                  </div>
                )}
                <div
                  className={`tab ${tab === 'dashboard' ? 'active' : ''}`}
                  onClick={() => setTab('dashboard')}
                >
                  连接看板
                </div>
                {selection.itemId && selection.namespace !== PUBLIC_NS && (
                  <button className="secondary" style={{ marginLeft: 'auto' }} onClick={addItem}>
                    + 新建配置项
                  </button>
                )}
              </div>

              {tab === 'edit' && selectedItem && (
                <>
                  <ConfigEditor
                    key={`${selectedItem.id}-${env}-${tick}`}
                    item={selectedItem}
                    env={env}
                    envs={ENVS}
                    onEnvChange={setEnv}
                    onSaved={() => {
                      reloadAll()
                      setTick((n) => n + 1)
                      showToast('已保存，变更已推送')
                    }}
                  />
                  <VersionTimeline
                    item={selectedItem}
                    env={env}
                    reloadToken={tick}
                    onRolledBack={() => {
                      reloadAll()
                      setTick((n) => n + 1)
                      showToast('已回退（产生新版本）')
                    }}
                  />
                </>
              )}

              {tab === 'edit' && !selectedItem && (
                <div className="card">
                  <h2>{selection.namespace === PUBLIC_NS ? '公共层' : '分组'}配置</h2>
                  <p className="muted">
                    这里是
                    {selection.namespace === PUBLIC_NS
                      ? '所有服务共享的公共配置'
                      : selection.group === '_defaults'
                        ? '该业务线内所有服务共享的命名空间层配置'
                        : '该服务独有的分组层配置'}
                    。
                  </p>
                  {selection.namespace !== PUBLIC_NS && (
                    <button onClick={addItem}>+ 新建配置项</button>
                  )}
                  {selection.namespace === PUBLIC_NS && (
                    <button onClick={addItem}>+ 新建公共配置项</button>
                  )}
                </div>
              )}

              {tab === 'gray' && (
                <GrayPanel
                  namespace={selection.namespace}
                  reloadToken={tick}
                  onChanged={() => {
                    reloadAll()
                    setTick((n) => n + 1)
                  }}
                />
              )}

              {tab === 'effective' && (
                <EffectiveView
                  namespace={selection.namespace}
                  group={selection.group}
                  env={env}
                  tick={tick}
                />
              )}

              {tab === 'dashboard' && (
                <Dashboard namespace={selection.namespace} tick={tick} />
              )}
            </>
          )}
        </main>
      </div>
    </div>
  )
}
