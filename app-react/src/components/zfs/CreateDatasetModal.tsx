/**
 * components/zfs/CreateDatasetModal.tsx
 *
 * Create a child dataset from a preset. Shared by PoolsPage and DatasetsPage.
 *
 *   POST /api/zfs/datasets { name, mountpoint, quota, compression, dedup,
 *                            atime, recordsize, xattr, acltype, casesensitivity }
 *
 * Presets only pre-fill the form; every value stays editable under
 * "Advanced". OpenZFS on Linux supports POSIX ACLs only, so all presets use
 * acltype=posix with xattr=sa (ACLs and Samba's DOS attributes live in
 * system-attribute xattrs). Case sensitivity is fixed at creation.
 */

import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'

type PresetId = 'generic' | 'smb' | 'apps' | 'media'

interface DatasetProps {
  compression: string
  atime: string
  recordsize: string
  casesensitivity: 'sensitive' | 'insensitive'
}

const PRESETS: { id: PresetId; label: string; icon: string; hint: string; props: DatasetProps }[] = [
  {
    id: 'generic', label: 'Generic', icon: 'folder',
    hint: 'Linux-style defaults. Good for NFS exports and general storage.',
    props: { compression: 'lz4', atime: 'off', recordsize: '128K', casesensitivity: 'sensitive' },
  },
  {
    id: 'smb', label: 'SMB share', icon: 'folder_shared',
    hint: 'Case-insensitive names, as Windows and macOS clients expect.',
    props: { compression: 'lz4', atime: 'off', recordsize: '128K', casesensitivity: 'insensitive' },
  },
  {
    id: 'apps', label: 'Apps / containers', icon: 'deployed_code',
    hint: 'For Docker bind mounts and application data (databases, configs).',
    props: { compression: 'lz4', atime: 'off', recordsize: '16K', casesensitivity: 'sensitive' },
  },
  {
    id: 'media', label: 'Media', icon: 'movie',
    hint: 'Large sequential files: video, photos, backups. 1M records.',
    props: { compression: 'lz4', atime: 'off', recordsize: '1M', casesensitivity: 'sensitive' },
  },
]

const ZSTD_LEVELS = Array.from({ length: 19 }, (_, i) => `zstd-${i + 1}`)
const RECORDSIZES = ['4K', '8K', '16K', '32K', '64K', '128K', '256K', '512K', '1M']

export function CreateDatasetModal({ parentName, onClose, onCreated }: {
  parentName: string
  onClose: () => void
  onCreated: () => void
}) {
  const [childName, setChildName] = useState('')
  const [preset, setPreset] = useState<PresetId>('generic')
  const [props, setProps] = useState<DatasetProps>(PRESETS[0].props)
  const [dedup, setDedup] = useState('off')
  const [quota, setQuota] = useState('')
  const [advanced, setAdvanced] = useState(false)

  const fullName = `${parentName}/${childName}`

  const mutation = useMutation({
    mutationFn: async () => {
      const res = await api.post<{ success?: boolean; error?: string }>('/api/zfs/datasets', {
        name: fullName,
        mountpoint: `/${fullName}`,
        quota: quota.trim(),
        compression: props.compression,
        dedup,
        atime: props.atime,
        recordsize: props.recordsize,
        xattr: 'sa',
        acltype: 'posix',
        casesensitivity: props.casesensitivity,
      })
      // The create endpoint reports ZFS failures as 200 { success: false }.
      if (res && res.success === false) throw new Error(res.error || 'Dataset creation failed')
    },
    onSuccess: () => { toast.success(`Dataset ${fullName} created`); onCreated(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function pickPreset(id: PresetId) {
    setPreset(id)
    setProps(PRESETS.find(p => p.id === id)!.props)
  }

  function setProp<K extends keyof DatasetProps>(key: K, value: DatasetProps[K]) {
    setProps(p => ({ ...p, [key]: value }))
  }

  function submit() {
    if (!childName.trim()) { toast.error('Dataset name required'); return }
    if (!/^[a-zA-Z0-9_-]+$/.test(childName)) { toast.error('Name: letters, numbers, - and _ only'); return }
    if (quota.trim() && !/^[0-9]+[KMGTP]?$/.test(quota.trim())) { toast.error('Quota: a number with optional K, M, G, T or P (e.g. 500G)'); return }
    mutation.mutate()
  }

  return (
    <Modal title={<>New Dataset under <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>{parentName}</span></>} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxHeight: '70vh', overflowY: 'auto', paddingRight: 4 }}>
        <label className="field">
          <span className="field-label">Dataset Name</span>
          <input value={childName} onChange={e => setChildName(e.target.value)} placeholder="e.g. photos"
            className="input" onKeyDown={e => e.key === 'Enter' && submit()} autoFocus />
        </label>

        <div className="field">
          <span className="field-label">Preset</span>
          <div role="radiogroup" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 8 }}>
            {PRESETS.map(p => (
              <label
                key={p.id}
                style={{
                  display: 'flex', gap: 10, alignItems: 'flex-start', cursor: 'pointer', padding: '10px 12px',
                  borderRadius: 'var(--radius-sm)',
                  border: `1px solid ${preset === p.id ? 'var(--primary)' : 'var(--border-subtle)'}`,
                  background: preset === p.id ? 'var(--primary-bg)' : 'transparent',
                }}
              >
                <input type="radio" name="dataset-preset" checked={preset === p.id} onChange={() => pickPreset(p.id)} style={{ marginTop: 3 }} />
                <Icon name={p.icon} size={18} style={{ color: preset === p.id ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0, marginTop: 1 }} />
                <span>
                  <span style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 600 }}>{p.label}</span>
                  <span style={{ display: 'block', fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{p.hint}</span>
                </span>
              </label>
            ))}
          </div>
        </div>

        <label className="field">
          <span className="field-label">Quota (optional, e.g. 100G)</span>
          <input value={quota} onChange={e => setQuota(e.target.value)} placeholder="no limit" className="input" />
        </label>

        <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => setAdvanced(a => !a)}>
          <Icon name="settings" size={14} />{advanced ? 'Hide' : 'Show'} advanced properties
        </button>

        {advanced && (
          <div className="card" style={{ padding: 14, display: 'flex', flexDirection: 'column', gap: 12, background: 'var(--bg-elevated)' }}>
            <label className="field">
              <span className="field-label">Case sensitivity (cannot be changed later)</span>
              <select value={props.casesensitivity} onChange={e => setProp('casesensitivity', e.target.value as DatasetProps['casesensitivity'])} className="input">
                <option value="sensitive">Sensitive (Linux, NFS)</option>
                <option value="insensitive">Insensitive (Windows, macOS via SMB)</option>
              </select>
            </label>
            <label className="field">
              <span className="field-label">Compression</span>
              <select value={props.compression} onChange={e => setProp('compression', e.target.value)} className="input">
                <option value="lz4">LZ4 (recommended)</option>
                <option value="zstd">ZSTD (default level)</option>
                {ZSTD_LEVELS.map(z => <option key={z} value={z}>{z}</option>)}
                <option value="gzip">GZIP</option>
                <option value="off">Off</option>
              </select>
            </label>
            <label className="field">
              <span className="field-label">Record size</span>
              <select value={props.recordsize} onChange={e => setProp('recordsize', e.target.value)} className="input">
                {RECORDSIZES.map(rs => <option key={rs} value={rs}>{rs}</option>)}
              </select>
            </label>
            <label className="field">
              <span className="field-label">Access time (atime)</span>
              <select value={props.atime} onChange={e => setProp('atime', e.target.value)} className="input">
                <option value="off">Off (recommended, fewer writes)</option>
                <option value="on">On</option>
              </select>
            </label>
            <label className="field">
              <span className="field-label">Deduplication</span>
              <select value={dedup} onChange={e => setDedup(e.target.value)} className="input">
                <option value="off">Off (recommended)</option>
                <option value="on">On (SHA-256)</option>
                <option value="verify">Verify (byte-for-byte)</option>
                <option value="sha512">SHA-512</option>
              </select>
            </label>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>
              All presets use POSIX ACLs with xattr=sa. NFSv4 ACLs are not available in OpenZFS on Linux.
            </div>
          </div>
        )}
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Creating…' : 'Create Dataset'}
        </button>
      </div>
    </Modal>
  )
}
