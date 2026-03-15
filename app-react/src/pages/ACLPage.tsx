/**
 * pages/ACLPage.tsx — POSIX ACL Manager (Phase 4)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/acl/get?path=   → { success, acl: string (getfacl output), stat: string }
 *   POST /api/acl/set         → { path, entries: AclEntry[] }
 *
 * The daemon returns raw getfacl text. We parse it client-side.
 * setfacl operations send structured entries back to the daemon.
 */

import { useState, useEffect } from 'react'
import type React from 'react'
import { useMutation } from '@tanstack/react-query'
import { useSearch } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface AclEntry {
  type:  'user' | 'group' | 'other' | 'mask'
  name:  string   // empty = default (owner user/group)
  read:  boolean
  write: boolean
  exec:  boolean
}

interface AclGetResponse {
  success: boolean
  acl:     string   // raw getfacl output
  stat?:   string   // owner/group info
}

// ---------------------------------------------------------------------------
// Parser: getfacl text → AclEntry[]
// ---------------------------------------------------------------------------

function parseAcl(text: string): AclEntry[] {
  return text
    .split('\n')
    .map(l => l.trim())
    .filter(l => l && !l.startsWith('#') && l.includes(':'))
    .map(line => {
      const parts = line.split(':')
      const type = parts[0] as AclEntry['type']
      const name = parts[1] ?? ''
      const perms = parts[2] ?? '---'
      return {
        type,
        name,
        read:  perms[0] === 'r',
        write: perms[1] === 'w',
        exec:  perms[2] === 'x',
      }
    })
    .filter(e => ['user', 'group', 'other', 'mask'].includes(e.type))
}

function entriesToAclString(entries: AclEntry[]): string {
  return entries.map(e => `${e.type}:${e.name}:${e.read ? 'r' : '-'}${e.write ? 'w' : '-'}${e.exec ? 'x' : '-'}`).join('\n')
}

// ---------------------------------------------------------------------------
// Type badge colors
// ---------------------------------------------------------------------------

function typeStyle(type: AclEntry['type']): React.CSSProperties {
  const colors: Record<string, string> = {
    user:  'var(--info)',
    group: 'var(--primary)',
    other: 'var(--text-secondary)',
    mask:  'var(--error)',
  }
  const c = colors[type] ?? 'var(--text-tertiary)'
  return { padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: `${c}18`, border: `1px solid ${c}30`, color: c, fontSize: 'var(--text-xs)', fontWeight: 700, textTransform: 'uppercase', flexShrink: 0 }
}

// ---------------------------------------------------------------------------
// AclEntryRow
// ---------------------------------------------------------------------------

function AclEntryRow({ entry, onChange, onRemove }: {
  entry: AclEntry
  onChange: (e: AclEntry) => void
  onRemove: () => void
}) {
  function toggle(field: 'read' | 'write' | 'exec') {
    onChange({ ...entry, [field]: !entry[field] })
  }

  return (
    <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 16px', borderRadius: 'var(--radius-sm)' }}>
      <span style={typeStyle(entry.type)}>{entry.type}</span>
      <span style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', color: entry.name ? 'var(--text)' : 'var(--text-tertiary)' }}>
        {entry.name || '(default)'}
      </span>
      {/* rwx toggles */}
      <div style={{ display: 'flex', gap: 2 }}>
        {(['read', 'write', 'exec'] as const).map((bit, i) => (
          <button key={bit} onClick={() => toggle(bit)}
            style={{ width: 28, height: 28, borderRadius: 'var(--radius-xs)', border: '1px solid var(--border)', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 700, background: entry[bit] ? 'var(--success-bg)' : 'var(--surface)', color: entry[bit] ? 'var(--success)' : 'var(--text-tertiary)', borderColor: entry[bit] ? 'var(--success-border)' : 'var(--border)' }}>
            {'rwx'[i]}
          </button>
        ))}
      </div>
      <span style={{ fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)', color: 'var(--text-tertiary)', width: 32, textAlign: 'center' }}>
        {(entry.read ? 'r' : '-') + (entry.write ? 'w' : '-') + (entry.exec ? 'x' : '-')}
      </span>
      <button onClick={onRemove}
        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 4, display: 'flex', borderRadius: 'var(--radius-xs)', transition: 'color 0.15s, background 0.15s' }}
        onMouseEnter={e => { (e.currentTarget as HTMLButtonElement).style.color = 'var(--error)'; (e.currentTarget as HTMLButtonElement).style.background = 'var(--error-bg)' }}
        onMouseLeave={e => { (e.currentTarget as HTMLButtonElement).style.color = 'var(--text-tertiary)'; (e.currentTarget as HTMLButtonElement).style.background = 'transparent' }}
      >
        <Icon name="close" size={16} />
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// AddEntryForm
// ---------------------------------------------------------------------------

function AddEntryForm({ onAdd }: { onAdd: (e: AclEntry) => void }) {
  const [type, setType] = useState<AclEntry['type']>('user')
  const [name, setName] = useState('')
  const [read, setRead] = useState(true)
  const [write, setWrite] = useState(false)
  const [exec, setExec] = useState(false)

  function add() {
    onAdd({ type, name, read, write, exec })
    setName('')
    setRead(true); setWrite(false); setExec(false)
  }

  return (
    <div className="card" style={{ background: 'var(--surface)',  display: 'flex', alignItems: 'center', gap: 10, padding: '12px 16px', borderRadius: 'var(--radius-sm)', flexWrap: 'wrap' }}>
      <select value={type} onChange={e => setType(e.target.value as AclEntry['type'])}
        className="card" style={{ background: 'var(--surface)', borderRadius: 'var(--radius-xs)', padding: '6px 10px', color: 'var(--text)', fontSize: 'var(--text-sm)', outline: 'none' }}>
        <option value="user">user</option>
        <option value="group">group</option>
        <option value="other">other</option>
        <option value="mask">mask</option>
      </select>
      <input value={name} onChange={e => setName(e.target.value)} placeholder="username or empty"
        className="input" style={{ width: 180, padding: '6px 10px' }} />
      {(['read', 'write', 'exec'] as const).map((bit, i) => (
        <label key={bit} style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          <input type="checkbox" checked={bit === 'read' ? read : bit === 'write' ? write : exec}
            onChange={e => bit === 'read' ? setRead(e.target.checked) : bit === 'write' ? setWrite(e.target.checked) : setExec(e.target.checked)}
            style={{ accentColor: 'var(--primary)' }} />
          {'rwx'[i]}
        </label>
      ))}
      <button onClick={add} className="btn btn-primary"><Icon name="add" size={15} />Add</button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ACLPage
// ---------------------------------------------------------------------------

export function ACLPage() {
  const search = useSearch({ from: '/protected/acl' }) as { path?: string }
  const [pathInput, setPathInput] = useState(search.path || '/mnt/')
  const [loadedPath, setLoadedPath] = useState<string | null>(null)
  const [entries, setEntries] = useState<AclEntry[]>([])
  const [statInfo, setStatInfo] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const loadMutation = useMutation({
    mutationFn: () => api.get<AclGetResponse>(`/api/acl/get?path=${encodeURIComponent(pathInput)}`),
    onMutate: () => setLoading(true),
    onSuccess: data => {
      setEntries(parseAcl(data.acl ?? ''))
      setStatInfo(data.stat ?? null)
      setLoadedPath(pathInput)
    },
    onError: (e: Error) => toast.error(e.message),
    onSettled: () => setLoading(false),
  })

  const setMutation = useMutation({
    mutationFn: () => api.post('/api/acl/set', { path: loadedPath, acl: entriesToAclString(entries) }),
    onSuccess: () => toast.success('ACL applied'),
    onError: (e: Error) => toast.error(e.message),
  })

  function updateEntry(idx: number, e: AclEntry) {
    setEntries(prev => prev.map((en, i) => i === idx ? e : en))
  }
  function removeEntry(idx: number) {
    setEntries(prev => prev.filter((_, i) => i !== idx))
  }

  // Auto-load if path is in search params
  useEffect(() => {
    if (search.path && !loadedPath && !loading) {
      loadMutation.mutate()
    }
  }, [search.path, loadedPath, loading, loadMutation])

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">ACL Manager</h1>
        <p className="page-subtitle">POSIX Access Control Lists — getfacl / setfacl</p>
      </div>

      {/* Path input */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 24 }}>
        <input value={pathInput} onChange={e => setPathInput(e.target.value)}
          placeholder="/mnt/tank/share" className="input" style={{ flex: 1 }}
          onKeyDown={e => e.key === 'Enter' && loadMutation.mutate()} />
        <button onClick={() => loadMutation.mutate()} disabled={loading} className="btn btn-primary">
          <Icon name="manage_search" size={16} />{loading ? 'Loading…' : 'Load ACL'}
        </button>
      </div>

      {loading && <Skeleton height={200} style={{ borderRadius: 'var(--radius-lg)' }} />}

      {loadedPath && !loading && (
        <>
          {/* Path + stat header */}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <div>
              <div style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-md)' }}>{loadedPath}</div>
              {statInfo && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{statInfo}</div>}
            </div>
            <button onClick={() => setMutation.mutate()} disabled={setMutation.isPending} className="btn btn-primary">
              <Icon name="save" size={15} />{setMutation.isPending ? 'Applying…' : 'Apply ACL'}
            </button>
          </div>

          {/* Entries */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 16 }}>
            {entries.length === 0 && (
              <div style={{ textAlign: 'center', padding: '32px 0', color: 'var(--text-tertiary)' }}>
                No ACL entries found
              </div>
            )}
            {entries.map((entry, idx) => (
              <AclEntryRow key={idx} entry={entry}
                onChange={e => updateEntry(idx, e)}
                onRemove={() => removeEntry(idx)}
              />
            ))}
          </div>

          {/* Add entry */}
          <div style={{ marginBottom: 24 }}>
            <div style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 8 }}>Add Entry</div>
            <AddEntryForm onAdd={e => setEntries(prev => [...prev, e])} />
          </div>

          {/* Raw preview */}
          <details>
            <summary style={{ cursor: 'pointer', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 8 }}>Raw ACL preview</summary>
            <pre className="card" style={{ background: 'var(--surface)', borderRadius: 'var(--radius-sm)', padding: '12px 14px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', whiteSpace: 'pre-wrap' }}>
              {entriesToAclString(entries) || '(empty)'}
            </pre>
          </details>
        </>
      )}

      {!loadedPath && !loading && (
        <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="manage_accounts" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>Enter a path to load its ACL</div>
          <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Supports directories and files under /mnt</div>
        </div>
      )}
    </div>
  )
}
