import { useMemo, useRef, useState } from 'react'
import {
  ApiError,
  api,
  type EnvValue,
  type Format,
  type GrayRequest,
  type Item,
} from '../api/client'
import { highlightLine } from './highlight'

interface Props {
  item: Item
  env: string
  envs: string[]
  onEnvChange: (env: string) => void
  onSaved: () => void
}

export function ConfigEditor({ item, env, envs, onEnvChange, onSaved }: Props) {
  const current: EnvValue | undefined = item.values?.[env]
  const [value, setValue] = useState(current?.value ?? '')
  const [expected, setExpected] = useState(current?.version ?? 0)
  const [errors, setErrors] = useState<ApiError | null>(null)
  const [saving, setSaving] = useState(false)
  const [grayMode, setGrayMode] = useState<'none' | 'ip' | 'percent'>('none')
  const [ipText, setIpText] = useState('')
  const [percent, setPercent] = useState(20)

  // Sync local draft when switching items/envs.
  const stateKey = `${item.id}:${env}`
  const lastKey = useRef('')
  if (lastKey.current !== stateKey) {
    lastKey.current = stateKey
    queueMicrotask(() => {
      setValue(current?.value ?? '')
      setExpected(current?.version ?? 0)
      setErrors(null)
    })
  }

  const lines = useMemo(() => value.split('\n'), [value])
  const preRef = useRef<HTMLPreElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  const syncScroll = () => {
    if (preRef.current && taRef.current) {
      preRef.current.scrollTop = taRef.current.scrollTop
      preRef.current.scrollLeft = taRef.current.scrollLeft
    }
  }

  const buildGray = (): GrayRequest | undefined => {
    if (grayMode === 'ip') {
      const ips = ipText.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean)
      return { strategy: 'ip', ips }
    }
    if (grayMode === 'percent') return { strategy: 'percent', percent }
    return undefined
  }

  const save = async () => {
    setSaving(true)
    setErrors(null)
    try {
      await api.commit(item.id, env, value, expected, buildGray())
      onSaved()
    } catch (e) {
      setErrors(e as ApiError)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="card">
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <h2 style={{ margin: 0 }}>
          🔑 {item.key} <span className="muted">({item.format})</span>
        </h2>
        <div className="row">
          {envs.map((e) => (
            <button
              key={e}
              className={e === env ? '' : 'secondary'}
              onClick={() => onEnvChange(e)}
              style={e === env ? {} : {}}
            >
              {e}
              {item.values?.[e] ? ` · v${item.values[e]!.version}` : ''}
            </button>
          ))}
        </div>
      </div>

      {current && (
        <p className="muted">
          当前 v{current.version}，由 {current.updated_by} 更新于{' '}
          {new Date(current.updated_at).toLocaleString()}
        </p>
      )}

      <div className="editor-line-wrap">
        <div className="gutter">
          {lines.map((_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        <div className="editor-pane" style={{ position: 'relative' }}>
          <pre
            ref={preRef}
            aria-hidden
            style={{
              margin: 0,
              position: 'absolute',
              inset: 0,
              padding: '8px 10px',
              pointerEvents: 'none',
              overflow: 'hidden',
              fontFamily: 'ui-monospace, Menlo, monospace',
              fontSize: 13,
              lineHeight: 1.55,
            }}
          >
            {lines.map((ln, i) => (
              <div key={i}>
                {highlightLine(item.format as Format, ln).map((t, j) => (
                  <span key={j} className={t.cls}>
                    {t.text}
                  </span>
                ))}
                {'\n'}
              </div>
            ))}
          </pre>
          <textarea
            ref={taRef}
            className="code-editor"
            value={value}
            spellCheck={false}
            onChange={(e) => setValue(e.target.value)}
            onScroll={syncScroll}
            style={{
              position: 'relative',
              background: 'transparent',
              color: 'transparent',
              caretColor: '#e2e8f0',
              width: '100%',
              minHeight: 340,
              border: 'none',
              outline: 'none',
            }}
          />
        </div>
      </div>

      {errors && (
        <div className="errors">
          <strong>保存被拒绝：</strong>
          {errors.errors && errors.errors.length > 0 ? (
            <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
              {errors.errors.map((er, i) => (
                <li key={i} className="err-line">
                  {er.line ? <code>第 {er.line} 行</code> : null}
                  {er.field ? <code>字段 {er.field}</code> : null}
                  {er.rule ? <code>{er.rule}</code> : null} {er.message}
                </li>
              ))}
            </ul>
          ) : (
            <div>{errors.message}</div>
          )}
        </div>
      )}

      <h3>灰度发布（可选）</h3>
      <div className="row">
        <label>
          <input
            type="radio"
            checked={grayMode === 'none'}
            onChange={() => setGrayMode('none')}
          />{' '}
          直接全量
        </label>
        <label>
          <input type="radio" checked={grayMode === 'ip'} onChange={() => setGrayMode('ip')} />{' '}
          按实例 IP 名单
        </label>
        <label>
          <input
            type="radio"
            checked={grayMode === 'percent'}
            onChange={() => setGrayMode('percent')}
          />{' '}
          按百分比
        </label>
      </div>
      {grayMode === 'ip' && (
        <div className="field">
          <label>IP 名单（逗号或换行分隔）</label>
          <textarea
            value={ipText}
            onChange={(e) => setIpText(e.target.value)}
            placeholder="10.0.0.1, 10.0.0.2"
            style={{ minHeight: 60 }}
          />
        </div>
      )}
      {grayMode === 'percent' && (
        <div className="field">
          <label>灰度比例：{percent}%</label>
          <input
            type="range"
            min={1}
            max={100}
            value={percent}
            onChange={(e) => setPercent(Number(e.target.value))}
          />
        </div>
      )}

      <div className="row" style={{ marginTop: 10 }}>
        <button onClick={save} disabled={saving}>
          {saving ? '保存中…' : grayMode === 'none' ? '保存并全量下发' : '保存并开始灰度'}
        </button>
        <span className="muted">
          保存前会按 {item.format.toUpperCase()} 语法校验{item.schema ? '与 JSON Schema 校验' : ''}
          ，不合法不会落库。
        </span>
      </div>
    </div>
  )
}
