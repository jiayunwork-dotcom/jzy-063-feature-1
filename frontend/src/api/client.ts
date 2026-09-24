// Typed client for the configuration center backend.

export type Format = 'json' | 'yaml' | 'properties' | 'toml'
export type Layer = 'public' | 'namespace' | 'group'
export type ReleaseStatus = 'gray' | 'promoted' | 'rolled_back'

export interface Tenant {
  id: string
  name: string
  max_namespaces: number
  max_items_per_group: number
  version_retention: number
}

export interface Namespace {
  id: string
  name: string
}

export interface Group {
  id: string
  name: string
}

export interface EnvValue {
  value: string
  version: number
  updated_at: string
  updated_by: string
}

export interface Item {
  id: string
  namespace_id: string
  group_id: string
  layer: Layer
  key: string
  format: Format
  schema?: string
  values?: Record<string, EnvValue>
  updated_at: string
}

export interface Version {
  id: number
  version: number
  value: string
  operator: string
  change_type: string
  note?: string
  created_at: string
}

export interface DiffLine {
  op: 'equal' | 'add' | 'delete' | 'change'
  left?: string
  right?: string
}

export interface GrayRequest {
  strategy: 'ip' | 'percent'
  ips?: string[]
  percent?: number
}

export interface Release {
  id: string
  namespace_id: string
  group_id: string
  item_id: string
  env: string
  version: number
  prev_version: number
  strategy: 'ip' | 'percent'
  ips?: string[]
  percent?: number
  status: ReleaseStatus
  operator: string
  started_at: string
  promoted_at?: string
  ended_at?: string
}

export interface ReleaseView extends Release {
  delivered_count: number
  total_instances: number
}

export interface FieldSource {
  path: string
  source: Layer
  source_version: number
}

export interface EffectiveEntry {
  key: string
  format: Format
  value: string
  source: Layer
  source_version: number
  fields: FieldSource[]
  version: number
}

export interface EffectiveSnapshot {
  namespace_id: string
  group_id?: string
  env: string
  version: number
  entries?: EffectiveEntry[]
  groups?: Record<string, { group_id: string; entries: EffectiveEntry[]; version: number }>
}

export interface ValidationError {
  line?: number
  column?: number
  field?: string
  rule?: string
  message: string
}

export interface NamespaceStat {
  namespace_id: string
  connections: number
  last_push_at?: string
  last_push_env?: string
  last_version?: number
}

export interface InstanceView {
  instance_id: string
  ip: string
  websocket: boolean
  connected_at: string
}

const TENANT_KEY = 'cc.tenant'
const OPERATOR_KEY = 'cc.operator'

export const session = {
  get tenant() {
    return localStorage.getItem(TENANT_KEY) || 'demo'
  },
  set tenant(v: string) {
    localStorage.setItem(TENANT_KEY, v)
  },
  get operator() {
    return localStorage.getItem(OPERATOR_KEY) || 'admin'
  },
  set operator(v: string) {
    localStorage.setItem(OPERATOR_KEY, v)
  },
}

export class ApiError extends Error {
  status: number
  code?: string
  errors?: ValidationError[]
  constructor(status: number, message: string, code?: string, errors?: ValidationError[]) {
    super(message)
    this.status = status
    this.code = code
    this.errors = errors
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-Tenant-ID': session.tenant,
      'X-Operator': session.operator,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : {}
  if (!res.ok) {
    throw new ApiError(res.status, data.error || res.statusText, data.code, data.errors)
  }
  return data as T
}

export const api = {
  listTenants: () => request<{ tenants: Tenant[] }>('GET', '/tenants'),
  createTenant: (id: string, name: string) => request<Tenant>('POST', '/tenants', { id, name }),

  listNamespaces: () => request<{ namespaces: Namespace[] }>('GET', '/namespaces'),
  createNamespace: (id: string, name: string) =>
    request<Namespace>('POST', '/namespaces', { id, name }),
  listGroups: (ns: string) => request<{ groups: Group[] }>('GET', `/namespaces/${ns}/groups`),
  createGroup: (ns: string, id: string, name: string) =>
    request<Group>('POST', `/namespaces/${ns}/groups`, { id, name }),

  listItems: (ns: string, g: string) =>
    request<{ items: Item[] }>('GET', `/namespaces/${ns}/groups/${g}/items`),
  createItem: (ns: string, g: string, key: string, format: Format) =>
    request<Item>('POST', `/namespaces/${ns}/groups/${g}/items`, { key, format }),
  getItem: (id: string) => request<Item>('GET', `/items/${id}`),
  setSchema: (id: string, schema: string) =>
    request('PUT', `/items/${id}/schema`, { schema }),
  listVersions: (id: string, env: string) =>
    request<{ versions: Version[] }>('GET', `/items/${id}/versions?env=${encodeURIComponent(env)}`),
  commit: (
    id: string,
    env: string,
    value: string,
    expectedVersion: number,
    gray?: GrayRequest,
  ) =>
    request<{ version: Version; release?: Release }>('POST', `/items/${id}/values`, {
      env,
      value,
      expected_version: expectedVersion,
      gray,
    }),
  rollback: (id: string, env: string, version: number) =>
    request<{ version: Version }>('POST', `/items/${id}/rollback`, { env, version }),
  diff: (id: string, env: string, from: number, to: number) =>
    request<{ lines: DiffLine[] }>(
      'GET',
      `/items/${id}/diff?env=${encodeURIComponent(env)}&from=${from}&to=${to}`,
    ),

  listReleases: (ns: string) =>
    request<{ releases: Release[] }>('GET', `/releases?namespace=${encodeURIComponent(ns)}`),
  getRelease: (id: string) => request<ReleaseView>('GET', `/releases/${id}`),
  promote: (id: string) => request<Release>('POST', `/releases/${id}/promote`),
  rollbackGray: (id: string) =>
    request<{ release: Release; version: Version }>('POST', `/releases/${id}/rollback-gray`),

  effective: (ns: string, g: string, env: string) =>
    request<EffectiveSnapshot>(
      'GET',
      `/effective?namespace=${encodeURIComponent(ns)}&group=${encodeURIComponent(g)}&env=${encodeURIComponent(env)}`,
    ),
  stats: () => request<{ namespaces: NamespaceStat[] }>('GET', '/stats'),
  instances: (ns: string) =>
    request<{ instances: InstanceView[]; total: number }>(
      'GET',
      `/instances?namespace=${encodeURIComponent(ns)}`,
    ),
}
