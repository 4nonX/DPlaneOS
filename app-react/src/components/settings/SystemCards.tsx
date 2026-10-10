/**
 * Settings cards: kernel tuning, secrets key check, NixOS config backup.
 *
 *   GET/POST /api/system/tuning          {arc_limit_gb, swappiness} (+ live on GET)
 *   GET      /api/system/secrets/status  {checked, healthy, undecryptable[], resealed, fallback_key}
 *   POST     /api/nixos/backup-config    → {success, message}
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface Tuning { arc_limit_gb: number; swappiness: number }

export function TuningCard() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['system', 'tuning'],
    queryFn: ({ signal }) => api.get<Tuning & { success: boolean; live: Tuning; warning?: string }>('/api/system/tuning', signal),
  })
  // Edits override the loaded values until saved.
  const [arcEdit, setArc] = useState<number | null>(null)
  const [swapEdit, setSwap] = useState<number | null>(null)
  const arc = arcEdit ?? q.data?.arc_limit_gb ?? 0
  const swap = swapEdit ?? q.data?.swappiness ?? 60

  const save = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean }>('/api/system/tuning', { arc_limit_gb: arc, swappiness: swap })),
    onSuccess: () => { toast.success('Tuning applied'); setArc(null); setSwap(null); qc.invalidateQueries({ queryKey: ['system', 'tuning'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ padding: 20 }}>
      <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>Memory tuning</h3>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
        Applied to the running system and kept across reboots.
        {q.data?.live && <> Now in effect: ARC limit {q.data.live.arc_limit_gb ? `${q.data.live.arc_limit_gb} GB` : 'automatic'}, swappiness {q.data.live.swappiness}.</>}
      </p>
      {q.data?.warning && <div style={{ color: 'var(--warning)', fontSize: 'var(--text-sm)', marginBottom: 12 }}>{q.data.warning}</div>}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <label className="field">
          <span className="field-label">ZFS cache (ARC) limit, GB</span>
          <input type="number" min={0} className="input" value={arc} onChange={e => setArc(Math.max(0, Number(e.target.value) || 0))} />
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>0 = automatic (half of the RAM). Lower it when apps or VMs need the memory.</span>
        </label>
        <label className="field">
          <span className="field-label">Swappiness (0-100)</span>
          <input type="number" min={0} max={100} className="input" value={swap} onChange={e => setSwap(Math.min(100, Math.max(0, Number(e.target.value) || 0)))} />
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>How readily the kernel swaps; 10 is a common value for a NAS, 60 the Linux default.</span>
        </label>
      </div>
      <button className="btn btn-primary" style={{ marginTop: 16 }} disabled={save.isPending || q.isLoading} onClick={() => save.mutate()}>
        <Icon name="save" size={14} />{save.isPending ? 'Applying…' : 'Apply'}
      </button>
    </div>
  )
}

export function SecretsStatusCard() {
  const q = useQuery({
    queryKey: ['system', 'secrets-status'],
    queryFn: ({ signal }) => api.get<{ success: boolean; checked: boolean; healthy?: boolean; undecryptable?: string[] | null; resealed?: number; fallback_key?: boolean }>('/api/system/secrets/status', signal),
  })
  const d = q.data
  if (!d) return null
  const bad = d.checked && d.healthy === false
  return (
    <div className="card" style={{ padding: 20, borderColor: bad ? 'var(--error-border)' : undefined }}>
      <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>Stored secrets</h3>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-sm)' }}>
        <Icon name={!d.checked ? 'help' : bad ? 'error' : 'verified_user'} size={18}
          style={{ color: !d.checked ? 'var(--text-tertiary)' : bad ? 'var(--error)' : 'var(--success)' }} />
        {!d.checked ? 'Not checked yet (the check runs when the service starts).'
          : bad ? 'Some stored secrets cannot be decrypted with the current key.'
          : 'All stored secrets (passwords, tokens, keys) decrypt with the current key.'}
      </div>
      {bad && (
        <ul style={{ margin: '10px 0 0', paddingLeft: 20, fontSize: 'var(--text-sm)' }}>
          {(d.undecryptable ?? []).map(u => <li key={u} style={{ fontFamily: 'var(--font-mono)' }}>{u}</li>)}
        </ul>
      )}
      {bad && <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 10 }}>Enter these secrets again in their settings; the old values are lost with the key they were encrypted with.</p>}
      {d.fallback_key && <p style={{ fontSize: 'var(--text-sm)', color: 'var(--warning)', marginTop: 10 }}>A fallback key was used at startup{d.resealed ? ` and ${d.resealed} values were re-encrypted with the current key` : ''}.</p>}
    </div>
  )
}

export function NixOSBackupButton() {
  const backup = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; message?: string }>('/api/nixos/backup-config', {})),
    onSuccess: r => toast.success(r.message ?? 'Configuration backed up'),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <button className="btn btn-ghost" disabled={backup.isPending} onClick={() => backup.mutate()}
      title="Commits /etc/nixos and pushes it to the NixOS repository chosen under GitOps">
      <Icon name="cloud_upload" size={14} />{backup.isPending ? 'Backing up…' : 'Back up config to Git'}
    </button>
  )
}
