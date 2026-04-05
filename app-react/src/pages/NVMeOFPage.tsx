/**
 * NVMe-oF targets — kernel nvmet over NVMe/TCP, backed by ZFS zvols.
 *
 * Complements ZFS send/recv (dataset replication): this path exports a zvol as a network block device.
 *
 * GET  /api/nvmet/status
 * GET  /api/nvmet/targets
 * GET  /api/nvmet/zvols
 * POST /api/nvmet/targets
 * PUT  /api/nvmet/targets
 * DELETE /api/nvmet/targets?subsystem_nqn=...
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

interface ZvolRow { name: string; size: string; dev: string }
interface NVMeExport {
  subsystem_nqn: string
  zvol: string
  transport?: string
  listen_addr?: string
  listen_port?: number
  namespace_id?: number
  allow_any_host?: boolean
  host_nqns?: string[]
}

export function NVMeOFPage() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()

  const statusQ = useQuery({
    queryKey: ['nvmet', 'status'],
    queryFn: ({ signal }) => api.get<{ success: boolean; ready?: boolean; nvmet_root?: string }>('/api/nvmet/status', signal),
    refetchInterval: 30_000,
  })

  const targetsQ = useQuery({
    queryKey: ['nvmet', 'targets'],
    queryFn: ({ signal }) => api.get<{ success: boolean; targets: NVMeExport[] }>('/api/nvmet/targets', signal),
  })

  const zvolsQ = useQuery({
    queryKey: ['nvmet', 'zvols'],
    queryFn: ({ signal }) => api.get<{ success: boolean; zvols: ZvolRow[] }>('/api/nvmet/zvols', signal),
  })

  const [nqn, setNqn] = useState('nqn.2024-01.io.dplane:')
  const [zvol, setZvol] = useState('')
  const [listenAddr, setListenAddr] = useState('0.0.0.0')
  const [listenPort, setListenPort] = useState('4420')
  const [allowAny, setAllowAny] = useState(false)
  const [hostNqnsText, setHostNqnsText] = useState('')
  const [editingNqn, setEditingNqn] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: () => {
      const host_nqns = hostNqnsText
        .split('\n')
        .map(s => s.trim())
        .filter(Boolean)
      const body: Record<string, unknown> = {
        subsystem_nqn: nqn.trim(),
        zvol: zvol.trim(),
        transport: 'tcp',
        listen_addr: listenAddr.trim() || '0.0.0.0',
        listen_port: parseInt(listenPort, 10) || 4420,
        allow_any_host: allowAny,
        host_nqns: allowAny ? [] : host_nqns,
      }
      const url = editingNqn ? '/api/nvmet/targets' : '/api/nvmet/targets'
      return editingNqn ? api.put(url, body) : api.post(url, body)
    },
    onSuccess: () => {
      toast.success(editingNqn ? 'Target updated' : 'Target created')
      setNqn('nqn.2024-01.io.dplane:')
      setZvol('')
      setHostNqnsText('')
      setEditingNqn(null)
      qc.invalidateQueries({ queryKey: ['nvmet'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const remove = useMutation({
    mutationFn: (subsystem: string) =>
      api.delete(`/api/nvmet/targets?subsystem_nqn=${encodeURIComponent(subsystem)}`),
    onSuccess: () => {
      toast.success('Target removed')
      qc.invalidateQueries({ queryKey: ['nvmet'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const targets = targetsQ.data?.targets ?? []
  const zvols = zvolsQ.data?.zvols ?? []
  const ready = statusQ.data?.ready

  function startEdit(t: NVMeExport) {
    setEditingNqn(t.subsystem_nqn)
    setNqn(t.subsystem_nqn)
    setZvol(t.zvol)
    setListenAddr(t.listen_addr || '0.0.0.0')
    setListenPort(String(t.listen_port || 4420))
    setAllowAny(!!t.allow_any_host)
    setHostNqnsText((t.host_nqns || []).join('\n'))
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  function cancelEdit() {
    setEditingNqn(null)
    setNqn('nqn.2024-01.io.dplane:')
    setZvol('')
    setListenAddr('0.0.0.0')
    setListenPort('4420')
    setAllowAny(false)
    setHostNqnsText('')
  }

  return (
    <div style={{ maxWidth: 960 }}>
      <div className="page-header">
        <h1 className="page-title">NVMe-oF</h1>
        <p className="page-subtitle">Export ZFS zvols over NVMe/TCP — optional block fabric alongside ZFS replication</p>
      </div>

      {statusQ.data && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 12,
            padding: '11px 18px',
            background: 'var(--bg-card)',
            border: `1px solid ${ready ? 'rgba(16,185,129,0.25)' : 'var(--border)'}`,
            borderRadius: 'var(--radius-lg)',
            marginBottom: 18,
          }}
        >
          <span
            style={{
              width: 10,
              height: 10,
              borderRadius: '50%',
              background: ready ? 'var(--success)' : 'var(--text-tertiary)',
              boxShadow: ready ? '0 0 6px var(--success)' : 'none',
            }}
          />
          <span style={{ fontWeight: 700, color: ready ? 'var(--success)' : 'var(--text-tertiary)' }}>
            {ready ? 'nvmet configfs ready' : 'nvmet not available (load nvmet / nvmet-tcp, mount configfs)'}
          </span>
        </div>
      )}

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: 18, marginBottom: 22, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        <div style={{ fontWeight: 700, marginBottom: 8, color: 'var(--text-primary)' }}>When to use this</div>
        <p style={{ margin: '0 0 8px' }}>
          <strong>ZFS send/recv</strong> (Replication page, GitOps) replicates datasets and snapshots. <strong>NVMe-oF</strong> exposes a{' '}
          <em>zvol</em> as a remote NVMe namespace so another machine can mount a VMFS, ext4, or clustered app on top.
        </p>
        <p style={{ margin: 0 }}>
          Declarative option: add a <code style={{ fontSize: 12 }}>fabrics.nvme</code> list in <code style={{ fontSize: 12 }}>state.yaml</code>; GitOps apply
          syncs the same JSON the UI edits. Omit <code style={{ fontSize: 12 }}>fabrics:</code> entirely if you only manage targets from the UI.
        </p>
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 22, marginBottom: 22, border: editingNqn ? '1px solid var(--primary)' : undefined }}>
        <div style={{ fontWeight: 700, marginBottom: 14 }}>{editingNqn ? 'Edit target' : 'Create target'}</div>
        <div style={{ display: 'grid', gap: 12 }}>
          <label className="field">
            <span className="field-label">Subsystem NQN</span>
            <input
              value={nqn}
              onChange={e => setNqn(e.target.value)}
              className="input"
              style={{ fontFamily: 'var(--font-mono)' }}
              disabled={!!editingNqn}
            />
          </label>
          <label className="field">
            <span className="field-label">ZFS zvol (dataset name)</span>
            {zvols.length > 0 ? (
              <select value={zvol} onChange={e => setZvol(e.target.value)} className="input" style={{ appearance: 'none' }}>
                <option value="">Select zvol…</option>
                {zvols.map(z => (
                  <option key={z.name} value={z.name}>
                    {z.name} ({z.size})
                  </option>
                ))}
              </select>
            ) : (
              <input value={zvol} onChange={e => setZvol(e.target.value)} placeholder="tank/nvme/vol0" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            )}
          </label>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 10 }}>
            <label className="field">
              <span className="field-label">Listen address</span>
              <input value={listenAddr} onChange={e => setListenAddr(e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
            <label className="field">
              <span className="field-label">TCP port</span>
              <input value={listenPort} onChange={e => setListenPort(e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
          </div>
          <label className="field" style={{ flexDirection: 'row', alignItems: 'center', gap: 10 }}>
            <input type="checkbox" checked={allowAny} onChange={e => setAllowAny(e.target.checked)} />
            <span className="field-label" style={{ margin: 0 }}>Allow any host NQN (insecure on untrusted networks)</span>
          </label>
          {!allowAny && (
            <label className="field">
              <span className="field-label">Initiator host NQNs (one per line)</span>
              <textarea
                value={hostNqnsText}
                onChange={e => setHostNqnsText(e.target.value)}
                className="input"
                rows={4}
                placeholder="nqn.2014-08.org.nvmexpress:uuid:…"
                style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}
              />
            </label>
          )}
          <div style={{ display: 'flex', gap: 8 }}>
            {editingNqn && (
              <button type="button" onClick={cancelEdit} className="btn btn-ghost">
                Cancel
              </button>
            )}
            <button
              type="button"
              onClick={() => save.mutate()}
              disabled={!nqn.trim() || !zvol.trim() || save.isPending}
              className="btn btn-primary"
            >
              <Icon name={editingNqn ? 'save' : 'add'} size={14} />
              {save.isPending ? 'Saving…' : editingNqn ? 'Update' : 'Create'}
            </button>
          </div>
        </div>
      </div>

      {targetsQ.isLoading && <Skeleton height={160} />}
      {targetsQ.isError && <ErrorState error={targetsQ.error} onRetry={() => targetsQ.refetch()} />}

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <table className="data-table">
          <thead>
            <tr>
              {['Subsystem NQN', 'Zvol', 'Listen', 'Access', 'Actions'].map(h => (
                <th key={h}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {targets.map(t => (
              <tr key={t.subsystem_nqn}>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--primary)' }}>{t.subsystem_nqn}</td>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{t.zvol}</td>
                <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                  {t.listen_addr || '0.0.0.0'}:{t.listen_port || 4420}
                </td>
                <td style={{ fontSize: 'var(--text-xs)' }}>{t.allow_any_host ? 'Any host' : `${(t.host_nqns || []).length} host(s)`}</td>
                <td>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <button type="button" onClick={() => startEdit(t)} className="btn btn-sm btn-ghost">
                      <Icon name="edit" size={13} />
                      Edit
                    </button>
                    <button
                      type="button"
                      onClick={async () => {
                        if (
                          await confirm({
                            title: 'Remove NVMe target?',
                            message: t.subsystem_nqn,
                            danger: true,
                            confirmLabel: 'Remove',
                          })
                        )
                          remove.mutate(t.subsystem_nqn)
                      }}
                      className="btn btn-sm btn-danger"
                    >
                      <Icon name="delete" size={13} />
                      Remove
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {!targetsQ.isLoading && targets.length === 0 && (
              <tr>
                <td colSpan={5} style={{ padding: '36px 16px', textAlign: 'center', color: 'var(--text-tertiary)' }}>
                  No NVMe-oF targets — create one above or define <code>fabrics.nvme</code> in GitOps.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="card" style={{ marginTop: 22, padding: 18, borderRadius: 'var(--radius-lg)', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        <div style={{ fontWeight: 700, marginBottom: 8, color: 'var(--text-primary)' }}>Initiator (Linux) quick test</div>
        <pre style={{ margin: 0, padding: 12, background: 'var(--surface-2)', borderRadius: 8, fontSize: 12, overflow: 'auto' }}>
          {`modprobe nvme-fabrics
nvme discover -t tcp -a <this-host-ip> -s 4420
nvme connect -t tcp -n <subsystem_nqn> -a <this-host-ip> -s 4420`}
        </pre>
        <p style={{ margin: '12px 0 0', fontSize: 'var(--text-xs)' }}>Open firewall TCP {listenPort || '4420'} on this NAS if clients are remote.</p>
      </div>

      <ConfirmDialog />
    </div>
  )
}
