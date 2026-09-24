import { useMemo, useState } from 'react'
import type { Group, Item, Namespace } from '../api/client'
import { domain as labels } from './layerLabels'

export interface Selection {
  namespace: string
  group: string
  itemId?: string
  layer: 'public' | 'namespace' | 'group'
}

interface Props {
  namespaces: Namespace[]
  groupsByNs: Record<string, Group[]>
  itemsByGroup: Record<string, Item[]>
  selection: Selection | null
  onSelect: (s: Selection) => void
  onAddNamespace: () => void
  onAddGroup: (ns: string) => void
}

const PUBLIC_NS = '_public'
const DEFAULT_GROUP = '_defaults'

export function TreeNav(props: Props) {
  const [expandedNs, setExpandedNs] = useState<Record<string, boolean>>({})
  const [expandedGrp, setExpandedGrp] = useState<Record<string, boolean>>({})

  const toggleNs = (ns: string) => setExpandedNs((s) => ({ ...s, [ns]: !s[ns] }))
  const toggleGrp = (key: string) => setExpandedGrp((s) => ({ ...s, [key]: !s[key] }))

  const ordered = useMemo(
    () => [...props.namespaces].sort((a, b) => (a.id === PUBLIC_NS ? -1 : a.id.localeCompare(b.id))),
    [props.namespaces],
  )

  return (
    <div>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: 8 }}>
        <strong style={{ fontSize: 13 }}>命名空间 / 分组 / 配置</strong>
        <button className="secondary" onClick={props.onAddNamespace}>
          + 命名空间
        </button>
      </div>
      {ordered.map((ns) => {
        const isPublic = ns.id === PUBLIC_NS
        const groups = props.groupsByNs[ns.id] || []
        const sortedGroups = [...groups].sort((a, b) => {
          if (a.id === DEFAULT_GROUP) return -1
          if (b.id === DEFAULT_GROUP) return 1
          return a.id.localeCompare(b.id)
        })
        const nsOpen = expandedNs[ns.id] ?? false
        return (
          <div key={ns.id} className="tree-node">
            <div className="tree-row" onClick={() => toggleNs(ns.id)}>
              <span className="caret">{nsOpen ? '▾' : '▸'}</span>
              <span>📦 {ns.name || ns.id}</span>
              {isPublic && <span className="badge public">公共层</span>}
              <span className="sub">{ns.id}</span>
            </div>
            {nsOpen && (
              <div className="tree-children">
                {sortedGroups.map((g) => {
                  const key = `${ns.id}/${g.id}`
                  const layer: Selection['layer'] = isPublic
                    ? 'public'
                    : g.id === DEFAULT_GROUP
                      ? 'namespace'
                      : 'group'
                  const gOpen = expandedGrp[key] ?? false
                  const items = props.itemsByGroup[key] || []
                  return (
                    <div key={g.id}>
                      <div
                        className="tree-row"
                        onClick={(e) => {
                          e.stopPropagation()
                          toggleGrp(key)
                          props.onSelect({ namespace: ns.id, group: g.id, layer })
                        }}
                      >
                        <span className="caret">{gOpen ? '▾' : '▸'}</span>
                        <span>🗂️ {g.name || g.id}</span>
                        <span className={`badge ${layer}`}>{labels[layer]}</span>
                      </div>
                      {gOpen && (
                        <div className="tree-children">
                          {items
                            .slice()
                            .sort((a, b) => a.key.localeCompare(b.key))
                            .map((item) => (
                              <div
                                key={item.id}
                                className={`tree-row ${
                                  props.selection?.itemId === item.id ? 'active' : ''
                                }`}
                                onClick={() =>
                                  props.onSelect({
                                    namespace: ns.id,
                                    group: g.id,
                                    itemId: item.id,
                                    layer,
                                  })
                                }
                              >
                                <span className="caret">🔑</span>
                                <span>{item.key}</span>
                                <span className="sub">{item.format}</span>
                              </div>
                            ))}
                          {!isPublic && (
                            <div
                              className="tree-row muted"
                              onClick={() => props.onAddGroup(ns.id)}
                              style={{ display: items.length ? 'none' : 'flex' }}
                            >
                              + 新建分组
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  )
                })}
                {!isPublic && (
                  <div className="tree-row muted" onClick={() => props.onAddGroup(ns.id)}>
                    + 新建分组
                  </div>
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
