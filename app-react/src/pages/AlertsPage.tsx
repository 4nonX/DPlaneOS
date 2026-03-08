/**
 * pages/AlertsPage.tsx — Alert Channels (Phase 6)
 *
 * Tabs: Telegram | SMTP | Webhooks
 *
 * Calls:
 *   GET  /api/alerts/telegram              → { success, bot_token, chat_id, enabled }
 *   POST /api/alerts/telegram              → save config
 *   POST /api/alerts/telegram/test         → send test message
 *
 *   GET  /api/alerts/smtp                  → { success, host, port, username, password, from, tls, enabled }
 *   POST /api/alerts/smtp                  → save config
 *   POST /api/alerts/smtp/test             → send test email
 *
 *   GET  /api/alerts/webhooks              → { success, webhooks: Webhook[] }
 *   POST /api/alerts/webhooks              → { name, url, method, headers, body_template } → { success, id }
 *   DELETE /api/alerts/webhooks/{id}
 *   POST /api/alerts/webhooks/{id}/test    → fire test payload
 */

import { useState, useEffect } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface TelegramConfig {
  success?:   boolean
  bot_token?: string
  chat_id?:   string
  enabled?:   boolean
}

interface SMTPConfig {
  success?:   boolean
  host?:      string
  port?:      number
  username?:  string
  password?:  string
  from?:      string
  tls?:       boolean
  enabled?:   boolean
}

interface Webhook {
  id:               number | string
  name:             string
  url:              string
  method?:          string
  headers?:         Record<string, string>
  body_template?:   string
  last_status?:     string
}

interface WebhooksResponse { success: boolean; webhooks: Webhook[] }

// ---------------------------------------------------------------------------

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <label className="field-label">{label}</label>
      {children}
      {hint && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{hint}</span>}
    </div>
  )
}

function SectionCard({ icon, title, children }: { icon: string; title: string; children: React.ReactNode }) {
  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: '24px 28px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
        <div style={{ width: 42, height: 42, background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Icon name={icon} size={22} style={{ color: 'var(--primary)' }} />
        </div>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>{title}</div>
      </div>
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// TelegramTab
// ---------------------------------------------------------------------------

function TelegramTab() {
  const qc = useQueryClient()

  const configQ = useQuery({
    queryKey: ['alerts', 'telegram'],
    queryFn:  ({ signal }) => api.get<TelegramConfig>('/api/alerts/telegram', signal),
  })

  const [token,   setToken]   = useState('')
  const [chatId,  setChatId]  = useState('')
  const [enabled, setEnabled] = useState(false)
  const [seeded,  setSeeded]  = useState(false)

  useEffect(() => {
    if (configQ.data && !seeded) {
      setToken(configQ.data.bot_token ?? '')
      setChatId(configQ.data.chat_id  ?? '')
      setEnabled(!!configQ.data.enabled)
      setSeeded(true)
    }
  }, [configQ.data, seeded])

  const save = useMutation({
    mutationFn: () => api.post('/api/alerts/telegram', { bot_token: token, chat_id: chatId, enabled }),
    onSuccess: () => { toast.success('Telegram config saved'); qc.invalidateQueries({ queryKey: ['alerts', 'telegram'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: () => api.post('/api/alerts/telegram/test', {}),
    onSuccess: () => toast.success('Test message sent'),
    onError: (e: Error) => toast.error(e.message),
  })

  if (configQ.isLoading) return <Skeleton height={240} />
  if (configQ.isError)   return <ErrorState error={configQ.error} />

  return (
    <SectionCard icon="send" title="Telegram Notifications">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 520 }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
          <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)}
            style={{ width: 16, height: 16, accentColor: 'var(--primary)', cursor: 'pointer' }} />
          <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>Enable Telegram alerts</span>
        </label>

        <Field label="Bot Token" hint="From @BotFather — starts with numbers:letters">
          <input value={token} onChange={e => setToken(e.target.value)}
            placeholder="1234567890:ABCdefGHIjklMNOpqrSTUvwxYZ"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} type="password" autoComplete="off" />
        </Field>

        <Field label="Chat ID" hint="Numeric ID of the chat or channel to send alerts to">
          <input value={chatId} onChange={e => setChatId(e.target.value)}
            placeholder="-1001234567890" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </Field>

        <div style={{ display: 'flex', gap: 8, paddingTop: 4 }}>
          <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
            <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save'}
          </button>
          <button onClick={() => test.mutate()} disabled={test.isPending || !token || !chatId} className="btn btn-ghost">
            <Icon name="send" size={14} />{test.isPending ? 'Sending…' : 'Send Test'}
          </button>
        </div>
      </div>
    </SectionCard>
  )
}

// ---------------------------------------------------------------------------
// SMTPTab
// ---------------------------------------------------------------------------

function SMTPTab() {
  const qc = useQueryClient()

  const configQ = useQuery({
    queryKey: ['alerts', 'smtp'],
    queryFn:  ({ signal }) => api.get<SMTPConfig>('/api/alerts/smtp', signal),
  })

  const [host,     setHost]     = useState('')
  const [port,     setPort]     = useState('587')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [from,     setFrom]     = useState('')
  const [tls,      setTls]      = useState(true)
  const [enabled,  setEnabled]  = useState(false)
  const [seeded,   setSeeded]   = useState(false)

  useEffect(() => {
    if (configQ.data && !seeded) {
      const c = configQ.data
      setHost(c.host     ?? '')
      setPort(String(c.port ?? 587))
      setUsername(c.username ?? '')
      setPassword(c.password ?? '')
      setFrom(c.from     ?? '')
      setTls(c.tls       ?? true)
      setEnabled(!!c.enabled)
      setSeeded(true)
    }
  }, [configQ.data, seeded])

  const save = useMutation({
    mutationFn: () => api.post('/api/alerts/smtp', { host, port: Number(port), username, password, from, tls, enabled }),
    onSuccess: () => { toast.success('SMTP config saved'); qc.invalidateQueries({ queryKey: ['alerts', 'smtp'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: () => api.post('/api/alerts/smtp/test', {}),
    onSuccess: () => toast.success('Test email sent'),
    onError: (e: Error) => toast.error(e.message),
  })

  if (configQ.isLoading) return <Skeleton height={340} />
  if (configQ.isError)   return <ErrorState error={configQ.error} />

  return (
    <SectionCard icon="mail" title="SMTP Email Alerts">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 560 }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
          <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)}
            style={{ width: 16, height: 16, accentColor: 'var(--primary)', cursor: 'pointer' }} />
          <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>Enable SMTP email alerts</span>
        </label>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 12 }}>
          <Field label="SMTP Host">
            <input value={host} onChange={e => setHost(e.target.value)} placeholder="smtp.gmail.com" className="input" />
          </Field>
          <Field label="Port">
            <input type="number" value={port} onChange={e => setPort(e.target.value)} className="input" min={1} max={65535} />
          </Field>
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <Field label="Username">
            <input value={username} onChange={e => setUsername(e.target.value)} placeholder="user@example.com" className="input" autoComplete="off" />
          </Field>
          <Field label="Password">
            <input type="password" value={password} onChange={e => setPassword(e.target.value)} className="input" autoComplete="new-password" />
          </Field>
        </div>

        <Field label="From Address" hint="Sender address shown in received emails">
          <input value={from} onChange={e => setFrom(e.target.value)} placeholder="dplaneos@example.com" className="input" />
        </Field>

        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
          <input type="checkbox" checked={tls} onChange={e => setTls(e.target.checked)}
            style={{ width: 15, height: 15, accentColor: 'var(--primary)', cursor: 'pointer' }} />
          <span style={{ fontSize: 'var(--text-sm)' }}>Use TLS/STARTTLS</span>
        </label>

        <div style={{ display: 'flex', gap: 8, paddingTop: 4 }}>
          <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
            <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save'}
          </button>
          <button onClick={() => test.mutate()} disabled={test.isPending || !host} className="btn btn-ghost">
            <Icon name="mail" size={14} />{test.isPending ? 'Sending…' : 'Send Test Email'}
          </button>
        </div>
      </div>
    </SectionCard>
  )
}

// ---------------------------------------------------------------------------
// WebhookModal — create / view
// ---------------------------------------------------------------------------

function WebhookModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name,     setName]     = useState('')
  const [url,      setUrl]      = useState('')
  const [method,   setMethod]   = useState('POST')
  const [bodyTpl,  setBodyTpl]  = useState('{"event":"{{event}}","message":"{{message}}","timestamp":"{{timestamp}}"}')
  const [hdrKey,   setHdrKey]   = useState('')
  const [hdrVal,   setHdrVal]   = useState('')
  const [headers,  setHeaders]  = useState<Record<string, string>>({
    'Content-Type': 'application/json',
  })

  function addHeader() {
    if (!hdrKey.trim()) return
    setHeaders(prev => ({ ...prev, [hdrKey.trim()]: hdrVal.trim() }))
    setHdrKey(''); setHdrVal('')
  }

  function removeHeader(k: string) {
    setHeaders(prev => { const n = { ...prev }; delete n[k]; return n })
  }

  const create = useMutation({
    mutationFn: () => {
      if (!name.trim() || !url.trim()) throw new Error('Name and URL are required')
      return api.post('/api/alerts/webhooks', { name: name.trim(), url: url.trim(), method, headers, body_template: bodyTpl })
    },
    onSuccess: () => { toast.success('Webhook created'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title="Create Webhook" onClose={onClose} size="lg">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <Field label="Name">
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Slack alert" className="input" autoFocus />
        </Field>

        <div style={{ display: 'grid', gridTemplateColumns: '80px 1fr', gap: 10 }}>
          <Field label="Method">
            <select value={method} onChange={e => setMethod(e.target.value)} className="input" style={{ appearance: 'none' }}>
              {['POST', 'GET', 'PUT', 'PATCH'].map(m => <option key={m} value={m}>{m}</option>)}
            </select>
          </Field>
          <Field label="URL">
            <input value={url} onChange={e => setUrl(e.target.value)}
              placeholder="https://hooks.slack.com/services/..." className="input" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }} />
          </Field>
        </div>

        {/* Headers */}
        <div>
          <div style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: 8 }}>Headers</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 8 }}>
            {Object.entries(headers).map(([k, v]) => (
              <div key={k} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)' }}>
                <code style={{ fontSize: 11, color: 'var(--primary)', flex: '0 0 auto' }}>{k}</code>
                <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>:</span>
                <code style={{ fontSize: 11, color: 'var(--text-secondary)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }}>{v}</code>
                <button onClick={() => removeHeader(k)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 2, display: 'flex' }}
                  onMouseEnter={e => (e.currentTarget.style.color = 'var(--error)')}
                  onMouseLeave={e => (e.currentTarget.style.color = 'var(--text-tertiary)')}>
                  <Icon name="close" size={14} />
                </button>
              </div>
            ))}
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr auto', gap: 8 }}>
            <input value={hdrKey} onChange={e => setHdrKey(e.target.value)} placeholder="Header-Name" className="input" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }} />
            <input value={hdrVal} onChange={e => setHdrVal(e.target.value)} placeholder="value" className="input" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }} onKeyDown={e => e.key === 'Enter' && addHeader()} />
            <button onClick={addHeader} className="btn btn-ghost"><Icon name="add" size={14} /></button>
          </div>
        </div>

        {/* Body template */}
        <Field label="Body Template" hint="Variables: {{event}}, {{message}}, {{timestamp}}, {{severity}}">
          <textarea value={bodyTpl} onChange={e => setBodyTpl(e.target.value)}
            rows={4} className="input" style={{ fontFamily: 'var(--font-mono)', fontSize: 11, resize: 'vertical', lineHeight: 1.6 }} />
        </Field>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={() => create.mutate()} disabled={create.isPending} className="btn btn-primary">
            <Icon name="add" size={15} />{create.isPending ? 'Creating…' : 'Create'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// WebhooksTab
// ---------------------------------------------------------------------------

function WebhooksTab() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)

  const hooksQ = useQuery({
    queryKey: ['alerts', 'webhooks'],
    queryFn:  ({ signal }) => api.get<WebhooksResponse>('/api/alerts/webhooks', signal),
  })

  const deleteHook = useMutation({
    mutationFn: (id: number | string) => api.delete(`/api/alerts/webhooks/${id}`),
    onSuccess: () => { toast.success('Webhook deleted'); qc.invalidateQueries({ queryKey: ['alerts', 'webhooks'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const testHook = useMutation({
    mutationFn: (id: number | string) => api.post(`/api/alerts/webhooks/${id}/test`, {}),
    onSuccess: () => toast.success('Test payload fired'),
    onError: (e: Error) => toast.error(e.message),
  })

  const hooks = hooksQ.data?.webhooks ?? []

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <button onClick={() => setShowCreate(true)} className="btn btn-primary"><Icon name="add" size={15} />New Webhook</button>
      </div>

      {hooksQ.isLoading && <Skeleton height={180} />}
      {hooksQ.isError   && <ErrorState error={hooksQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['alerts', 'webhooks'] })} />}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {hooks.map(hook => (
          <div key={hook.id} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '16px 20px' }}>
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
              <Icon name="webhook" size={20} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 2 }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 700, marginBottom: 4 }}>{hook.name}</div>
                <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                  <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-xs)', background: 'var(--surface)', border: '1px solid var(--border)', fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>{hook.method ?? 'POST'}</span>
                  <code style={{ fontSize: 11, color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 380 }}>{hook.url}</code>
                </div>
                {hook.last_status && (
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>Last: {hook.last_status}</div>
                )}
              </div>
              <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
                <button onClick={() => testHook.mutate(hook.id)} disabled={testHook.isPending} className="btn btn-ghost">
                  <Icon name="send" size={13} />Test
                </button>
                <button onClick={() => { if (window.confirm(`Delete webhook "${hook.name}"?`)) deleteHook.mutate(hook.id) }}
                  className="btn btn-danger">
                  <Icon name="delete" size={13} />
                </button>
              </div>
            </div>
          </div>
        ))}
        {!hooksQ.isLoading && hooks.length === 0 && (
          <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No webhooks configured</div>
        )}
      </div>

      {showCreate && (
        <WebhookModal
          onClose={() => setShowCreate(false)}
          onDone={() => qc.invalidateQueries({ queryKey: ['alerts', 'webhooks'] })}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// AlertsPage
// ---------------------------------------------------------------------------

type Tab = 'telegram' | 'smtp' | 'webhooks'

export function AlertsPage() {
  const [tab, setTab] = useState<Tab>('telegram')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'telegram', label: 'Telegram', icon: 'send' },
    { id: 'smtp',     label: 'SMTP / Email', icon: 'mail' },
    { id: 'webhooks', label: 'Webhooks', icon: 'webhook' },
  ]

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">Alerts</h1>
        <p className="page-subtitle">Configure notification channels for system events</p>
      </div>

      <div className="tabs-underline">
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'telegram' && <TelegramTab />}
      {tab === 'smtp'     && <SMTPTab />}
      {tab === 'webhooks' && <WebhooksTab />}
    </div>
  )
}
