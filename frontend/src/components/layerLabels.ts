import type { Layer } from '../api/client'

export const domain = {
  public: '公共层',
  namespace: '命名空间层',
  group: '分组层',
} as const

export function layerLabel(l: Layer): string {
  return domain[l]
}

export const FORMATS: { value: 'json' | 'yaml' | 'properties' | 'toml'; label: string }[] = [
  { value: 'json', label: 'JSON' },
  { value: 'yaml', label: 'YAML' },
  { value: 'properties', label: 'Properties' },
  { value: 'toml', label: 'TOML' },
]
