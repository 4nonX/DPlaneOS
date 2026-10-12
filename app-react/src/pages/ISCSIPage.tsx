/**
 * pages/ISCSIPage.tsx - iSCSI targets: ZFS volumes offered as disks.
 *
 * GET    /api/iscsi/status           → { success, target_count, served, sessions }
 * GET    /api/iscsi/targets          → { success, targets: Target[] }
 * POST   /api/iscsi/targets          → { iqn, zvol, portal_ip?, portal_port?, require_chap }
 * POST   /api/iscsi/targets/update   → same; the target is rebuilt, its access list stays
 * DELETE /api/iscsi/targets/{iqn}
 * GET    /api/iscsi/acls             → { success, acls: ACL[] }
 * POST   /api/iscsi/acls             → { target_iqn, initiator_iqn, chap_user?, chap_pass? }
 * DELETE /api/iscsi/acls             → { target_iqn, initiator_iqn }
 * GET    /api/iscsi/zvols            → { success, zvols: {name,size,dev}[] }
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { usePersistedState } from '@/hooks/usePersistedState'
import { useConfirm } from '@/components/ui/ConfirmDialog'

interface Target {
  iqn: string
  zvol: string
  size?: number
  portal_ip?: string
  portal_port?: number
  require_chap?: boolean
  acls?: { initiator_iqn: string; chap_user?: string }[]
  /** Served by this node right now (a target on a storage group's volume is served by the group's owner). */
  active?: boolean
  sessions?: number
}
interface ACL { target_iqn: string; initiator_iqn: string; chap_user?: string }

function fmtSize(b?: number) { if (!b) return '-'; const u=['B','KB','MB','GB','TB']; const i=Math.min(Math.floor(Math.log(b)/Math.log(1024)),4); return `${(b/1024**i).toFixed(1)} ${u[i]}` }

const NEW_IQN = `iqn.${new Date().getFullYear()}-01.lan.nas:`

type ITab = 'targets' | 'acls'

export function ISCSIPage() {
  const [tab, setTab] = usePersistedState<ITab>('iscsi.tab', 'targets')
  const TABS = [{ id:'targets' as ITab, label:'Targets', icon:'storage' }, { id:'acls' as ITab, label:'ACLs / Initiators', icon:'lock' }]
  const { confirm, ConfirmDialog } = useConfirm()
  const qc = useQueryClient()

  const statusQ  = useQuery({ queryKey:['iscsi','status'],  queryFn:({signal})=>api.get<{success:boolean;target_count?:number;served?:number;sessions?:number}>('/api/iscsi/status',signal), refetchInterval:20000 })
  const targetsQ = useQuery({ queryKey:['iscsi','targets'], queryFn:({signal})=>api.get<{success:boolean;targets:Target[]}>('/api/iscsi/targets',signal), refetchInterval:20000 })
  const zvolsQ   = useQuery({ queryKey:['iscsi','zvols'],   queryFn:({signal})=>api.get<{success:boolean;zvols:{name:string;size:string;dev:string}[]}>('/api/iscsi/zvols',signal) })
  const aclsQ    = useQuery({ queryKey:['iscsi','acls'],    queryFn:({signal})=>api.get<{success:boolean;acls:ACL[]}>('/api/iscsi/acls',signal) })

  // Target form
  const [iqn, setIqn] = useState(NEW_IQN)
  const [zvol, setZvol] = useState('')
  const [portalIp, setPortalIp] = useState('0.0.0.0')
  const [portalPort, setPortalPort] = useState('3260')
  const [requireChap, setRequireChap] = useState(true)
  const [editingIqn, setEditingIqn] = useState<string | null>(null)

  const resetForm = () => { setIqn(NEW_IQN); setZvol(''); setPortalIp('0.0.0.0'); setPortalPort('3260'); setRequireChap(true); setEditingIqn(null) }

  const saveTarget = useMutation({
    mutationFn: () => api.post(editingIqn ? '/api/iscsi/targets/update' : '/api/iscsi/targets', {
      iqn: iqn.trim(), zvol: zvol.trim(), portal_ip: portalIp.trim() || '0.0.0.0', portal_port: parseInt(portalPort, 10) || 3260, require_chap: requireChap,
    }),
    onSuccess: () => { toast.success(editingIqn ? 'Target updated' : 'Target created'); resetForm(); qc.invalidateQueries({ queryKey:['iscsi'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const deleteTarget = useMutation({
    mutationFn: (q: string) => api.delete(`/api/iscsi/targets/${encodeURIComponent(q)}`, {}),
    onSuccess: () => { toast.success('Target deleted'); qc.invalidateQueries({ queryKey:['iscsi'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  // Access list form
  const [aclIqn, setAclIqn] = useState('')
  const [initiator, setInitiator] = useState('')
  const [chapUser, setChapUser] = useState('')
  const [chapPass, setChapPass] = useState('')

  const addACL = useMutation({
    mutationFn: () => api.post('/api/iscsi/acls', { target_iqn: aclIqn, initiator_iqn: initiator.trim(), chap_user: chapUser.trim(), chap_pass: chapPass }),
    onSuccess: () => { toast.success('Initiator allowed'); setInitiator(''); setChapUser(''); setChapPass(''); qc.invalidateQueries({ queryKey:['iscsi'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const removeACL = useMutation({
    mutationFn: (a: ACL) => api.delete('/api/iscsi/acls', { target_iqn: a.target_iqn, initiator_iqn: a.initiator_iqn }),
    onSuccess: () => { toast.success('Initiator removed'); qc.invalidateQueries({ queryKey:['iscsi'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const targets = targetsQ.data?.targets ?? []
  const zvols   = zvolsQ.data?.zvols ?? []
  const acls    = aclsQ.data?.acls ?? []
  const st      = statusQ.data
  const serving = (st?.served ?? 0) > 0
  const aclTarget = targets.find(t => t.iqn === aclIqn)
  const chapNeeded = !!aclTarget?.require_chap
  const chapGiven = chapUser.trim() !== '' || chapPass !== ''
  const chapOK = chapGiven ? (chapUser.trim() !== '' && chapPass.length >= 12) : !chapNeeded

  const handleEdit = (t: Target) => {
    setIqn(t.iqn); setZvol(t.zvol); setPortalIp(t.portal_ip || '0.0.0.0'); setPortalPort(String(t.portal_port || 3260)); setRequireChap(!!t.require_chap)
    setEditingIqn(t.iqn)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  return (
    <div style={{ maxWidth: 1040 }}>
      <div className="page-header">
        <h1 className="page-title">iSCSI</h1>
        <p className="page-subtitle">ZFS volumes offered as disks to other machines - targets and the initiators that may use them</p>
      </div>

      {st && (
        <div style={{ display:'flex', alignItems:'center', gap:12, padding:'11px 18px', background:'var(--bg-card)', border:`1px solid ${serving ? 'rgba(16,185,129,0.25)' : 'var(--border)'}`, borderRadius:'var(--radius-lg)', marginBottom:22 }}>
          <span style={{ width:10, height:10, borderRadius:'50%', background:serving?'var(--success)':'var(--text-tertiary)', boxShadow:serving?'0 0 6px var(--success)':'none' }}/>
          <span style={{ fontWeight:700, color:serving?'var(--success)':'var(--text-tertiary)' }}>
            {(st.target_count ?? 0) === 0
              ? 'No targets'
              : `${st.served ?? 0} of ${st.target_count} target(s) served by this node, ${st.sessions ?? 0} initiator(s) connected`}
          </span>
        </div>
      )}

      <div className="tabs-underline" style={{ marginBottom:22 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={()=>setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16}/>{t.label}
          </button>
        ))}
      </div>

      {tab === 'targets' && (
        <>
          <div className="card" style={{ borderRadius:'var(--radius-xl)', padding:22, marginBottom:22, border: editingIqn ? '1px solid var(--primary)' : undefined }}>
            <div style={{ fontWeight:700, marginBottom:14 }}>{editingIqn ? 'Edit Target' : 'Create Target'}</div>
            <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
              <label className="field">
                <span className="field-label">Target name (IQN)</span>
                <input value={iqn} onChange={e=>setIqn(e.target.value)} className="input" style={{ fontFamily:'var(--font-mono)' }} disabled={!!editingIqn} />
              </label>
              <label className="field">
                <span className="field-label">Volume</span>
                {zvols.length > 0 ? (
                  <select value={zvol} onChange={e=>setZvol(e.target.value)} className="input" style={{ appearance:'none' }}>
                    <option value="">Select a volume…</option>
                    {zvols.map(z => <option key={z.name} value={z.name}>{z.name} ({z.size})</option>)}
                  </select>
                ) : (
                  <input value={zvol} onChange={e=>setZvol(e.target.value)} placeholder="tank/volumes/disk0 (create one under Block Volumes)" className="input" style={{ fontFamily:'var(--font-mono)' }}/>
                )}
              </label>
              <label className="field">
                <span className="field-label">Listen address (0.0.0.0: all addresses)</span>
                <input value={portalIp} onChange={e=>setPortalIp(e.target.value)} className="input" style={{ fontFamily:'var(--font-mono)' }}/>
              </label>
              <label className="field">
                <span className="field-label">TCP port</span>
                <input value={portalPort} onChange={e=>setPortalPort(e.target.value)} className="input" style={{ fontFamily:'var(--font-mono)' }}/>
              </label>
            </div>
            <label style={{ display:'flex', alignItems:'center', gap:8, cursor:'pointer', fontSize:'var(--text-sm)', margin:'14px 0 4px' }}>
              <input type="checkbox" checked={requireChap} onChange={e=>setRequireChap(e.target.checked)} />
              Require CHAP: initiators sign in with the user and secret set on the access list
            </label>
            {!requireChap && (
              <div style={{ fontSize:'var(--text-xs)', color:'var(--warning)', marginBottom:4 }}>
                Without CHAP only the initiator's name is checked, and a machine on the network can claim any name. Use this on a storage network you control.
              </div>
            )}
            {editingIqn && (
              <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', marginBottom:4 }}>
                Changing the volume, the address or CHAP rebuilds the target: connected initiators are disconnected and reconnect. The access list stays.
              </div>
            )}
            <div style={{ display:'flex', gap:8, marginTop:12 }}>
              {editingIqn && <button onClick={resetForm} className="btn btn-ghost">Cancel</button>}
              <button onClick={()=>saveTarget.mutate()} disabled={!iqn.trim()||!zvol.trim()||saveTarget.isPending} className="btn btn-primary">
                <Icon name={editingIqn ? 'save' : 'add'} size={14}/>{saveTarget.isPending?'Saving…':editingIqn?'Update':'Create'}
              </button>
            </div>
          </div>

          {targetsQ.isLoading && <Skeleton height={160}/>}
          {targetsQ.isError   && <ErrorState error={targetsQ.error}/>}

          <div className="card" style={{ borderRadius:'var(--radius-lg)', overflow:'hidden' }}>
            <table className="data-table">
              <thead><tr>
                {['Target','Volume','Size','Listens on','CHAP','Initiators','State','Actions'].map(h=>(
                  <th key={h}>{h}</th>
                ))}
              </tr></thead>
              <tbody>
                {targets.map(t=>(
                  <tr key={t.iqn}>
                    <td style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--primary)' }}>{t.iqn}</td>
                    <td style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>{t.zvol}</td>
                    <td style={{ color:'var(--text-secondary)' }}>{fmtSize(t.size)}</td>
                    <td style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>{t.portal_ip || '0.0.0.0'}:{t.portal_port || 3260}</td>
                    <td style={{ fontSize:'var(--text-xs)', color: t.require_chap ? 'var(--text-secondary)' : 'var(--warning)' }}>{t.require_chap ? 'Required' : 'Off'}</td>
                    <td style={{ fontSize:'var(--text-xs)' }}>
                      {(t.acls ?? []).length === 0
                        ? <span style={{ color:'var(--warning)' }} title="No initiator is on the access list: nobody can connect">None allowed</span>
                        : `${t.sessions ?? 0} of ${(t.acls ?? []).length} connected`}
                    </td>
                    <td style={{ fontSize:'var(--text-xs)', color: t.active ? 'var(--success)' : 'var(--text-tertiary)' }}
                      title={t.active ? 'This node serves the target' : 'This node does not serve the target: its volume is not here (the pool is not imported, or belongs to a storage group another node owns)'}>
                      {t.active ? 'Served here' : 'Not served here'}
                    </td>
                    <td>
                      <div style={{ display:'flex', gap:6 }}>
                        <button onClick={()=>handleEdit(t)} className="btn btn-sm btn-ghost"><Icon name="edit" size={13}/>Edit</button>
                        <button onClick={()=>{ setAclIqn(t.iqn); setTab('acls') }} className="btn btn-sm btn-ghost"><Icon name="lock" size={13}/>Access</button>
                        <button onClick={async ()=>{ if(await confirm({ title:`Delete target "${t.iqn}"?`, message:'Connected initiators lose the disk, and the access list of the target is removed. The volume and its data stay.', danger:true, confirmLabel:'Delete', confirmText: t.iqn.split(':').pop() ?? 'DELETE' })) deleteTarget.mutate(t.iqn) }} className="btn btn-sm btn-danger"><Icon name="delete" size={13}/>Delete</button>
                      </div>
                    </td>
                  </tr>
                ))}
                {!targetsQ.isLoading && targets.length===0 && (
                  <tr><td colSpan={8} style={{ padding:'40px 16px', textAlign:'center', color:'var(--text-tertiary)' }}>No iSCSI targets configured</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}

      {tab === 'acls' && (
        <>
          <div className="card" style={{ borderRadius:'var(--radius-xl)', padding:22, marginBottom:22 }}>
            <div style={{ fontWeight:700, marginBottom:14 }}>Allow an initiator</div>
            <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
              <label className="field">
                <span className="field-label">Target</span>
                <select value={aclIqn} onChange={e=>setAclIqn(e.target.value)} className="input" style={{ appearance:'none' }}>
                  <option value="">Select a target…</option>
                  {targets.map(t=><option key={t.iqn} value={t.iqn}>{t.iqn}</option>)}
                </select>
              </label>
              <label className="field">
                <span className="field-label">Initiator name (IQN)</span>
                <input value={initiator} onChange={e=>setInitiator(e.target.value)} placeholder="iqn.1993-08.org.debian:01:…" className="input" style={{ fontFamily:'var(--font-mono)' }}/>
              </label>
              <label className="field">
                <span className="field-label">CHAP user{chapNeeded ? '' : ' (optional)'}</span>
                <input value={chapUser} onChange={e=>setChapUser(e.target.value)} autoComplete="off" className="input" style={{ fontFamily:'var(--font-mono)' }}/>
              </label>
              <label className="field">
                <span className="field-label">CHAP secret (12 characters or more, no spaces)</span>
                <input type="password" value={chapPass} onChange={e=>setChapPass(e.target.value)} autoComplete="new-password" className="input" style={{ fontFamily:'var(--font-mono)' }}/>
              </label>
            </div>
            <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', margin:'10px 0 12px' }}>
              {chapNeeded
                ? 'This target requires CHAP: the initiator signs in with this user and secret.'
                : 'Only initiators on this list can connect. On Linux the initiator name is in /etc/iscsi/initiatorname.iscsi.'}
              {' '}Adding an initiator that is already listed replaces its credentials.
            </div>
            <button onClick={()=>addACL.mutate()} disabled={!aclIqn||!initiator.trim()||!chapOK||addACL.isPending} className="btn btn-primary">
              <Icon name="add" size={14}/>{addACL.isPending?'Adding…':'Allow'}
            </button>
          </div>

          {aclsQ.isLoading && <Skeleton height={120}/>}
          {aclsQ.isError   && <ErrorState error={aclsQ.error}/>}

          <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
            {acls.map(a=>(
              <div key={`${a.target_iqn}|${a.initiator_iqn}`} className="card" style={{ display:'flex', alignItems:'center', gap:14, padding:'12px 18px', borderRadius:'var(--radius-md)' }}>
                <Icon name="lock" size={16} style={{ color:'var(--primary)', flexShrink:0 }}/>
                <div style={{ flex:1 }}>
                  <div style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--primary)' }}>{a.target_iqn}</div>
                  <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', marginTop:2 }}>
                    Initiator: <span style={{ fontFamily:'var(--font-mono)' }}>{a.initiator_iqn}</span>
                    {a.chap_user ? <> · CHAP user <span style={{ fontFamily:'var(--font-mono)' }}>{a.chap_user}</span></> : ' · no CHAP'}
                  </div>
                </div>
                <button onClick={async ()=>{ if(await confirm({ title:'Remove this initiator?', message:`${a.initiator_iqn} loses access to ${a.target_iqn}; if it is connected, it is disconnected.`, danger:true, confirmLabel:'Remove' })) removeACL.mutate(a) }} className="btn btn-sm btn-danger"><Icon name="delete" size={13}/>Remove</button>
              </div>
            ))}
            {!aclsQ.isLoading && acls.length===0 && (
              <div style={{ textAlign:'center', padding:'32px 0', color:'var(--text-tertiary)' }}>No initiator is allowed yet: nobody can connect to a target until its initiators are listed here.</div>
            )}
          </div>
        </>
      )}
      <ConfirmDialog />
    </div>
  )
}
