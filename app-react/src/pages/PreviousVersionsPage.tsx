/**
 * pages/PreviousVersionsPage.tsx - browse snapshots and restore files.
 *
 *   GET  /api/zfs/datasets                          → { data: [{ name }] }
 *   GET  /api/timemachine/versions?dataset=         → { versions: Snapshot[] }
 *   GET  /api/timemachine/browse?snapshot=&path=    → { entries: Entry[] }
 *   POST /api/timemachine/restore {snapshot, source_path, dest_path?, overwrite}
 */
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { fmtDateTime } from '@/lib/fmt'

interface Version { name: string; snap_name: string; creation: string; used: string; refer: string }
interface Entry { name: string; path: string; is_dir: boolean; size: number; mod_time: string }

function fmtBytes(b: number): string {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}

export function PreviousVersionsPage() {
  const { confirm, ConfirmDialog } = useConfirm()
  const [dataset, setDataset] = useState('')
  const [snapshot, setSnapshot] = useState('')
  const [path, setPath] = useState('/')

  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets', 'names'],
    queryFn: ({ signal }) => api.get<{ success: boolean; data: Array<{ name: string }> }>('/api/zfs/datasets', signal),
  })
  const versionsQ = useQuery({
    queryKey: ['timemachine', 'versions', dataset],
    queryFn: ({ signal }) => api.get<{ success: boolean; versions: Version[] }>(`/api/timemachine/versions?dataset=${encodeURIComponent(dataset)}`, signal),
    enabled: !!dataset,
  })
  const browseQ = useQuery({
    queryKey: ['timemachine', 'browse', snapshot, path],
    queryFn: ({ signal }) => api.get<{ success: boolean; entries: Entry[] | null }>(`/api/timemachine/browse?snapshot=${encodeURIComponent(snapshot)}&path=${encodeURIComponent(path)}`, signal),
    enabled: !!snapshot,
  })

  const restore = useMutation({
    mutationFn: async (v: { source: string; dest?: string; overwrite: boolean }) => {
      const r = await api.post<{ success: boolean; error?: string; destination?: string }>('/api/timemachine/restore', {
        snapshot, source_path: v.source, dest_path: v.dest, overwrite: v.overwrite,
      })
      if (!r.success) throw new Error(r.error ?? 'Restore failed')
      return r
    },
    onSuccess: r => toast.success(`Restored to ${r.destination}`),
    onError: (e: Error) => toast.error(e.message),
  })

  async function restoreFile(e: Entry) {
    const overwrite = await confirm({
      title: `Restore ${e.name}?`,
      message: 'Replace the current file with this version? Cancel to keep both (the old version is restored next to it with the snapshot name in its file name).',
      confirmLabel: 'Replace current file',
      danger: true,
    })
    if (overwrite) {
      restore.mutate({ source: e.path, overwrite: true })
    } else {
      const snap = snapshot.split('@')[1] ?? 'snapshot'
      const dot = e.path.lastIndexOf('.')
      const dest = dot > e.path.lastIndexOf('/') ? `${e.path.slice(0, dot)} (${snap})${e.path.slice(dot)}` : `${e.path} (${snap})`
      restore.mutate({ source: e.path, dest, overwrite: false })
    }
  }

  const crumbs = path.split('/').filter(Boolean)
  const versions = [...(versionsQ.data?.versions ?? [])].reverse() // newest first

  return (
    <div style={{ maxWidth: 1100 }}>
      <div className="page-header">
        <div>
          <h1 className="page-title">Previous Versions</h1>
          <p className="page-subtitle">Browse snapshots of a dataset and restore individual files</p>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(220px, 300px) 1fr', gap: 20 }}>
        <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 16 }}>
          <label className="field">
            <span className="field-label">Dataset</span>
            <select value={dataset} onChange={e => { setDataset(e.target.value); setSnapshot(''); setPath('/') }} className="input">
              <option value="">Choose a dataset…</option>
              {(datasetsQ.data?.data ?? []).map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
            </select>
          </label>
          {versionsQ.isLoading && <Skeleton height={160} />}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 10, maxHeight: 520, overflowY: 'auto' }}>
            {versions.map(v => (
              <button key={v.name} onClick={() => { setSnapshot(v.name); setPath('/') }}
                className={`btn ${snapshot === v.name ? 'btn-primary' : 'btn-ghost'}`}
                style={{ justifyContent: 'flex-start', textAlign: 'left', flexDirection: 'column', alignItems: 'flex-start', gap: 2 }}>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>@{v.snap_name}</span>
                <span style={{ fontSize: 'var(--text-2xs)', opacity: 0.8 }}>{v.creation}</span>
              </button>
            ))}
            {dataset && !versionsQ.isLoading && versions.length === 0 && (
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No snapshots of this dataset.</div>
            )}
          </div>
        </div>

        <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 16 }}>
          {!snapshot ? (
            <div style={{ color: 'var(--text-tertiary)', textAlign: 'center', padding: '60px 0' }}>Choose a dataset and a snapshot.</div>
          ) : (
            <>
              <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap', marginBottom: 12, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>
                <button className="btn btn-xs btn-ghost" onClick={() => setPath('/')}>{snapshot}</button>
                {crumbs.map((c, i) => (
                  <span key={i} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                    /<button className="btn btn-xs btn-ghost" onClick={() => setPath('/' + crumbs.slice(0, i + 1).join('/'))}>{c}</button>
                  </span>
                ))}
              </div>
              {browseQ.isLoading && <Skeleton height={200} />}
              {browseQ.isError && <ErrorState error={browseQ.error} onRetry={() => browseQ.refetch()} />}
              <table className="table" style={{ width: '100%' }}>
                <tbody>
                  {crumbs.length > 0 && (
                    <tr><td colSpan={4}><button className="btn btn-xs btn-ghost" onClick={() => setPath('/' + crumbs.slice(0, -1).join('/'))}><Icon name="arrow_upward" size={14} /> ..</button></td></tr>
                  )}
                  {(browseQ.data?.entries ?? []).sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.name.localeCompare(b.name)).map(e => (
                    <tr key={e.path}>
                      <td>
                        {e.is_dir
                          ? <button className="btn btn-xs btn-ghost" onClick={() => setPath(e.path)}><Icon name="folder" size={14} /> {e.name}</button>
                          : <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}><Icon name="description" size={14} /> {e.name}</span>}
                      </td>
                      <td style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>{e.is_dir ? '' : fmtBytes(e.size)}</td>
                      <td style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>{fmtDateTime(e.mod_time)}</td>
                      <td style={{ textAlign: 'right' }}>
                        {!e.is_dir && (
                          <button className="btn btn-xs btn-ghost" disabled={restore.isPending} onClick={() => restoreFile(e)}>
                            <Icon name="restore" size={14} /> Restore
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                  {browseQ.data && (browseQ.data.entries ?? []).length === 0 && (
                    <tr><td colSpan={4} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>Empty folder</td></tr>
                  )}
                </tbody>
              </table>
            </>
          )}
        </div>
      </div>
      <ConfirmDialog />
    </div>
  )
}
