/**
 * pages/ISCSIPage.tsx — iSCSI Targets (Phase 7)
 *
 * GET    /api/iscsi/status           → { success, running: bool }
 * GET    /api/iscsi/targets          → { success, targets: Target[] }
 * POST   /api/iscsi/targets          → { iqn, zvol, size? }
 * DELETE /api/iscsi/targets/{iqn}
 * GET    /api/iscsi/acls             → { success, acls: ACL[] }
 * POST   /api/iscsi/acls             → { iqn, initiator }
 * DELETE /api/iscsi/acls             → { iqn, initiator }
 * GET    /api/iscsi/zvols            → { success, zvols: string[] }
 */

import { useState } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface Target { iqn: string; zvol?: string; sessions?: number; size?: number }
interface ACL    { iqn: string; initiator: string }

const S = {
  btn:  { padding:'7px 13px', background:'var(--surface)', color:'var(--text-secondary)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:500, display:'inline-flex', alignItems:'center', gap:6 } as React.CSSProperties,
  btnP: { padding:'8px 16px', background:'var(--primary)', color:'#000', border:'none', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:700, display:'inline-flex', alignItems:'center', gap:6 } as React.CSSProperties,
  btnD: { padding:'6px 11px', background:'var(--error-bg)', border:'1px solid var(--error-border)', borderRadius:'var(--radius-sm)', cursor:'pointer', color:'var(--error)', fontSize:'var(--text-xs)', fontWeight:600, display:'inline-flex', alignItems:'center', gap:4 } as React.CSSProperties,
  inp:  { background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'8px 12px', color:'var(--text)', fontSize:'var(--text-sm)', width:'100%', outline:'none', boxSizing:'border-box' as const, fontFamily:'var(--font-ui)' },
}

function fmtSize(b?: number) { if (!b) return '—'; const u=['B','KB','MB','GB','TB']; const i=Math.min(Math.floor(Math.log(b)/Math.log(1024)),4); return `${(b/1024**i).toFixed(1)} ${u[i]}` }

type ITab = 'targets' | 'acls'

export function ISCSIPage() {
  const [tab, setTab] = useState<ITab>('targets')
  const TABS = [{ id:'targets' as ITab, label:'Targets', icon:'storage' }, { id:'acls' as ITab, label:'ACLs / Initiators', icon:'lock' }]

  const qc = useQueryClient()

  // Status
  const statusQ = useQuery({ queryKey:['iscsi','status'], queryFn:({signal})=>api.get<{success:boolean;running:boolean}>('/api/iscsi/status',signal), refetchInterval:20000 })

  // Targets
  const targetsQ = useQuery({ queryKey:['iscsi','targets'], queryFn:({signal})=>api.get<{success:boolean;targets:Target[]}>('/api/iscsi/targets',signal) })
  const zvolsQ   = useQuery({ queryKey:['iscsi','zvols'],   queryFn:({signal})=>api.get<{success:boolean;zvols:string[]}>('/api/iscsi/zvols',signal) })

  const [iqn, setIqn] = useState('iqn.2024-01.me.dplane:')
  const [zvol, setZvol] = useState('')

  const createTarget = useMutation({
    mutationFn: () => api.post('/api/iscsi/targets', { iqn, zvol }),
    onSuccess: () => { toast.success('Target created'); setIqn('iqn.2024-01.me.dplane:'); setZvol(''); qc.invalidateQueries({ queryKey:['iscsi'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const deleteTarget = useMutation({
    mutationFn: (q: string) => api.delete(`/api/iscsi/targets/${encodeURIComponent(q)}`, {}),
    onSuccess: () => { toast.success('Target deleted'); qc.invalidateQueries({ queryKey:['iscsi'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  // ACLs
  const aclsQ = useQuery({ queryKey:['iscsi','acls'], queryFn:({signal})=>api.get<{success:boolean;acls:ACL[]}>('/api/iscsi/acls',signal) })
  const [aclIqn, setAclIqn] = useState('')
  const [initiator, setInitiator] = useState('')

  const addACL = useMutation({
    mutationFn: () => api.post('/api/iscsi/acls', { iqn: aclIqn, initiator }),
    onSuccess: () => { toast.success('ACL added'); setAclIqn(''); setInitiator(''); qc.invalidateQueries({ queryKey:['iscsi','acls'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const removeACL = useMutation({
    mutationFn: ({ iqn, initiator }: { iqn: string; initiator: string }) => api.delete('/api/iscsi/acls', { iqn, initiator }),
    onSuccess: () => { toast.success('ACL removed'); qc.invalidateQueries({ queryKey:['iscsi','acls'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const targets = targetsQ.data?.targets ?? []
  const zvols   = zvolsQ.data?.zvols ?? []
  const acls    = aclsQ.data?.acls ?? []
  const running = statusQ.data?.running

  return (
    <div style={{ maxWidth: 960 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize:'var(--text-3xl)', fontWeight:700, letterSpacing:'-1px', marginBottom:6 }}>iSCSI</h1>
        <p style={{ color:'var(--text-secondary)' }}>Block storage over IP — targets and initiator ACLs</p>
      </div>

      {/* Status */}
      {statusQ.data && (
        <div style={{ display:'flex', alignItems:'center', gap:12, padding:'11px 18px', background:'var(--bg-card)', border:`1px solid ${running ? 'rgba(16,185,129,0.25)' : 'var(--border)'}`, borderRadius:'var(--radius-lg)', marginBottom:22 }}>
          <span style={{ width:10, height:10, borderRadius:'50%', background:running?'var(--success)':'var(--text-tertiary)', boxShadow:running?'0 0 6px var(--success)':'none' }}/>
          <span style={{ fontWeight:700, color:running?'var(--success)':'var(--text-tertiary)' }}>{running ? 'iSCSI daemon running' : 'iSCSI daemon stopped'}</span>
        </div>
      )}

      {/* Tabs */}
      <div style={{ display:'flex', gap:4, marginBottom:22, borderBottom:'1px solid var(--border)' }}>
        {TABS.map(t => (
          <button key={t.id} onClick={()=>setTab(t.id)} style={{ padding:'10px 20px', background:'none', border:'none', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:600, color:tab===t.id?'var(--primary)':'var(--text-secondary)', borderBottom:tab===t.id?'2px solid var(--primary)':'2px solid transparent', marginBottom:-1, display:'flex', alignItems:'center', gap:6 }}>
            <Icon name={t.icon} size={16}/>{t.label}
          </button>
        ))}
      </div>

      {/* Targets tab */}
      {tab === 'targets' && (
        <>
          {/* Create form */}
          <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:22, marginBottom:22 }}>
            <div style={{ fontWeight:700, marginBottom:14 }}>Create Target</div>
            <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr auto', gap:10, alignItems:'flex-end' }}>
              <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
                <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>IQN</span>
                <input value={iqn} onChange={e=>setIqn(e.target.value)} style={{ ...S.inp, fontFamily:'var(--font-mono)' }}/>
              </label>
              <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
                <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>ZVol</span>
                {zvols.length > 0 ? (
                  <select value={zvol} onChange={e=>setZvol(e.target.value)} style={{ ...S.inp, appearance:'none' }}>
                    <option value="">Select zvol…</option>
                    {zvols.map(z => <option key={z} value={z}>{z}</option>)}
                  </select>
                ) : (
                  <input value={zvol} onChange={e=>setZvol(e.target.value)} placeholder="tank/zvols/target0" style={{ ...S.inp, fontFamily:'var(--font-mono)' }}/>
                )}
              </label>
              <button onClick={()=>createTarget.mutate()} disabled={!iqn.trim()||!zvol.trim()||createTarget.isPending} style={S.btnP}>
                <Icon name="add" size={14}/>{createTarget.isPending?'Creating…':'Create'}
              </button>
            </div>
          </div>

          {targetsQ.isLoading && <Skeleton height={160}/>}
          {targetsQ.isError   && <ErrorState error={targetsQ.error}/>}

          <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-lg)', overflow:'hidden' }}>
            <table style={{ width:'100%', borderCollapse:'collapse' }}>
              <thead><tr style={{ background:'rgba(255,255,255,0.03)' }}>
                {['IQN','ZVol','Sessions','Size','Actions'].map(h=>(
                  <th key={h} style={{ padding:'10px 16px', textAlign:'left', fontSize:'var(--text-2xs)', fontWeight:700, color:'var(--text-tertiary)', textTransform:'uppercase', letterSpacing:'0.5px', borderBottom:'1px solid var(--border)' }}>{h}</th>
                ))}
              </tr></thead>
              <tbody>
                {targets.map(t=>(
                  <tr key={t.iqn} style={{ borderBottom:'1px solid var(--border)' }} onMouseEnter={e=>(e.currentTarget.style.background='rgba(255,255,255,0.02)')} onMouseLeave={e=>(e.currentTarget.style.background='transparent')}>
                    <td style={{ padding:'12px 16px', fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--primary)' }}>{t.iqn}</td>
                    <td style={{ padding:'12px 16px', fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>{t.zvol||'—'}</td>
                    <td style={{ padding:'12px 16px', fontSize:'var(--text-sm)' }}>{t.sessions ?? 0}</td>
                    <td style={{ padding:'12px 16px', fontSize:'var(--text-sm)', color:'var(--text-secondary)' }}>{fmtSize(t.size)}</td>
                    <td style={{ padding:'12px 16px' }}>
                      <button onClick={()=>{ if(window.confirm(`Delete target "${t.iqn}"?`)) deleteTarget.mutate(t.iqn) }} style={S.btnD}><Icon name="delete" size={13}/>Delete</button>
                    </td>
                  </tr>
                ))}
                {!targetsQ.isLoading && targets.length===0 && (
                  <tr><td colSpan={5} style={{ padding:'40px 16px', textAlign:'center', color:'var(--text-tertiary)' }}>No iSCSI targets configured</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}

      {/* ACLs tab */}
      {tab === 'acls' && (
        <>
          <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:22, marginBottom:22 }}>
            <div style={{ fontWeight:700, marginBottom:14 }}>Add Initiator ACL</div>
            <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr auto', gap:10, alignItems:'flex-end' }}>
              <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
                <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>Target IQN</span>
                {targets.length > 0 ? (
                  <select value={aclIqn} onChange={e=>setAclIqn(e.target.value)} style={{ ...S.inp, appearance:'none' }}>
                    <option value="">Select target…</option>
                    {targets.map(t=><option key={t.iqn} value={t.iqn}>{t.iqn}</option>)}
                  </select>
                ) : (
                  <input value={aclIqn} onChange={e=>setAclIqn(e.target.value)} placeholder="iqn.…" style={{ ...S.inp, fontFamily:'var(--font-mono)' }}/>
                )}
              </label>
              <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
                <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>Initiator IQN (or ALL)</span>
                <input value={initiator} onChange={e=>setInitiator(e.target.value)} placeholder="iqn.… or ALL" style={{ ...S.inp, fontFamily:'var(--font-mono)' }}/>
              </label>
              <button onClick={()=>addACL.mutate()} disabled={!aclIqn.trim()||!initiator.trim()||addACL.isPending} style={S.btnP}>
                <Icon name="add" size={14}/>{addACL.isPending?'Adding…':'Add'}
              </button>
            </div>
          </div>

          {aclsQ.isLoading && <Skeleton height={120}/>}
          {aclsQ.isError   && <ErrorState error={aclsQ.error}/>}

          <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
            {acls.map((a, i)=>(
              <div key={i} style={{ display:'flex', alignItems:'center', gap:14, padding:'12px 18px', background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-md)' }}>
                <Icon name="lock" size={16} style={{ color:'var(--primary)', flexShrink:0 }}/>
                <div style={{ flex:1 }}>
                  <div style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--primary)' }}>{a.iqn}</div>
                  <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', marginTop:2 }}>Initiator: <span style={{ fontFamily:'var(--font-mono)' }}>{a.initiator}</span></div>
                </div>
                <button onClick={()=>removeACL.mutate({ iqn:a.iqn, initiator:a.initiator })} style={S.btnD}><Icon name="delete" size={13}/>Remove</button>
              </div>
            ))}
            {!aclsQ.isLoading && acls.length===0 && (
              <div style={{ textAlign:'center', padding:'32px 0', color:'var(--text-tertiary)' }}>No ACL rules — all initiators may connect</div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
