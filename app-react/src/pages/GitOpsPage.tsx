/**
 * pages/GitOpsPage.tsx — GitOps State Machine (Phase 7)
 *
 * GET  /api/gitops/status → { state, pending_changes, last_applied }
 * GET  /api/gitops/plan   → { changes: Change[] }
 * GET  /api/gitops/state  → current desired state YAML/JSON
 * PUT  /api/gitops/state  → update desired state
 * POST /api/gitops/check  → validate
 * POST /api/gitops/apply  → apply pending changes
 * POST /api/gitops/approve
 */

import { useState } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface GitopsStatus { success: boolean; state?: string; pending_changes?: number; last_applied?: string; drift?: boolean }
interface Change       { resource?: string; action?: 'create'|'update'|'delete'|string; description?: string }

const S = {
  btn:  { padding:'8px 14px', background:'var(--surface)', color:'var(--text-secondary)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:500, display:'inline-flex', alignItems:'center', gap:6 } as React.CSSProperties,
  btnP: { padding:'9px 20px', background:'var(--primary)', color:'#000', border:'none', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:700, display:'inline-flex', alignItems:'center', gap:6 } as React.CSSProperties,
  ta:   { background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'12px 14px', color:'var(--text)', fontSize:12, width:'100%', outline:'none', boxSizing:'border-box' as const, fontFamily:'var(--font-mono)', resize:'vertical' as const, minHeight:280 },
}

function fmtDate(s?:string){if(!s)return'—';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

function changeColor(a?:string):string {
  if (a === 'create') return 'var(--success)'
  if (a === 'delete') return 'var(--error)'
  return 'var(--warning)'
}

export function GitOpsPage() {
  const qc = useQueryClient()
  const [stateEdit, setStateEdit] = useState<string|null>(null)

  const statusQ = useQuery({ queryKey:['gitops','status'], queryFn:({signal})=>api.get<GitopsStatus>('/api/gitops/status',signal), refetchInterval:15_000 })
  const planQ   = useQuery({ queryKey:['gitops','plan'],   queryFn:({signal})=>api.get<{success:boolean;changes:Change[]}>('/api/gitops/plan',signal) })
  const stateQ  = useQuery({ queryKey:['gitops','state'],  queryFn:({signal})=>api.get<{success:boolean;state:string}>('/api/gitops/state',signal) })

  const check   = useMutation({ mutationFn: () => api.post('/api/gitops/check',{}), onSuccess: () => toast.success('Config valid'), onError: (e:Error)=>toast.error(e.message) })
  const apply   = useMutation({ mutationFn: () => api.post('/api/gitops/apply',{}), onSuccess: () => { toast.success('Applied'); qc.invalidateQueries({queryKey:['gitops']}) }, onError: (e:Error)=>toast.error(e.message) })
  const approve = useMutation({ mutationFn: () => api.post('/api/gitops/approve',{}), onSuccess: () => { toast.success('Approved'); qc.invalidateQueries({queryKey:['gitops']}) }, onError: (e:Error)=>toast.error(e.message) })
  const saveState = useMutation({
    mutationFn: () => api.put('/api/gitops/state', { state: stateEdit }),
    onSuccess: () => { toast.success('State saved'); setStateEdit(null); qc.invalidateQueries({queryKey:['gitops']}) },
    onError: (e:Error)=>toast.error(e.message),
  })

  const st = statusQ.data
  const changes = planQ.data?.changes ?? []
  const stateText = stateEdit ?? stateQ.data?.state ?? ''

  return (
    <div style={{ maxWidth: 1000 }}>
      <div style={{ marginBottom:28 }}><h1 style={{ fontSize:'var(--text-3xl)', fontWeight:700, letterSpacing:'-1px', marginBottom:6 }}>GitOps</h1><p style={{ color:'var(--text-secondary)' }}>Declarative infrastructure state management</p></div>

      {/* Status bar */}
      {statusQ.isLoading && <Skeleton height={80} style={{ marginBottom:20 }} />}
      {st && (
        <div style={{ display:'flex', alignItems:'center', gap:14, padding:'14px 20px', background:'var(--bg-card)', border:`1px solid ${st.drift?'var(--warning-border)':'var(--border)'}`, borderRadius:'var(--radius-lg)', marginBottom:24 }}>
          <Icon name={st.drift ? 'warning' : 'check_circle'} size={20} style={{ color: st.drift ? 'var(--warning)' : 'var(--success)' }} />
          <div style={{ flex:1 }}>
            <span style={{ fontWeight:700 }}>{st.state || 'Unknown'}</span>
            {st.drift && <span style={{ marginLeft:10, fontSize:'var(--text-xs)', color:'var(--warning)' }}>Drift detected</span>}
            {st.pending_changes != null && st.pending_changes > 0 && <span style={{ marginLeft:10, fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{st.pending_changes} pending changes</span>}
            {st.last_applied && <span style={{ marginLeft:10, fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Last applied: {fmtDate(st.last_applied)}</span>}
          </div>
          <div style={{ display:'flex', gap:8 }}>
            <button onClick={()=>check.mutate()} disabled={check.isPending} style={S.btn}><Icon name="fact_check" size={14}/>{check.isPending?'Checking…':'Check'}</button>
            <button onClick={()=>apply.mutate()} disabled={apply.isPending} style={S.btnP}><Icon name="rocket_launch" size={14}/>{apply.isPending?'Applying…':'Apply'}</button>
            <button onClick={()=>approve.mutate()} disabled={approve.isPending} style={S.btn}><Icon name="verified" size={14}/>{approve.isPending?'Approving…':'Approve'}</button>
          </div>
        </div>
      )}

      <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:20 }}>
        {/* Plan */}
        <div>
          <div style={{ fontWeight:700, marginBottom:12, display:'flex', alignItems:'center', gap:8 }}><Icon name="list_alt" size={18} style={{ color:'var(--primary)' }}/>Pending Changes</div>
          {planQ.isLoading && <Skeleton height={180} />}
          {planQ.isError  && <ErrorState error={planQ.error} />}
          <div style={{ display:'flex', flexDirection:'column', gap:6 }}>
            {changes.map((c, i) => {
              const color = changeColor(c.action)
              return (
                <div key={i} style={{ display:'flex', alignItems:'flex-start', gap:10, padding:'10px 14px', background:'var(--bg-card)', border:`1px solid ${color}20`, borderRadius:'var(--radius-md)' }}>
                  <span style={{ padding:'2px 6px', borderRadius:'var(--radius-sm)', background:`${color}18`, color, fontSize:'var(--text-2xs)', fontWeight:700, textTransform:'uppercase', flexShrink:0, marginTop:1 }}>{c.action}</span>
                  <div>
                    <div style={{ fontWeight:600, fontSize:'var(--text-sm)' }}>{c.resource}</div>
                    {c.description && <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{c.description}</div>}
                  </div>
                </div>
              )
            })}
            {!planQ.isLoading && changes.length === 0 && (
              <div style={{ textAlign:'center', padding:'32px 0', border:'1px dashed var(--border)', borderRadius:'var(--radius-lg)', color:'var(--text-tertiary)' }}>
                <Icon name="check_circle" size={32} style={{ opacity:0.3, display:'block', margin:'0 auto 8px' }} />No pending changes
              </div>
            )}
          </div>
        </div>

        {/* State editor */}
        <div>
          <div style={{ fontWeight:700, marginBottom:12, display:'flex', alignItems:'center', gap:8 }}>
            <Icon name="code" size={18} style={{ color:'var(--primary)' }}/>Desired State
            <button onClick={()=>setStateEdit(stateText||'')} style={{ ...S.btn, marginLeft:'auto', fontSize:'var(--text-xs)', padding:'4px 10px' }}><Icon name="edit" size={13}/>Edit</button>
          </div>
          {stateQ.isLoading && <Skeleton height={280} />}
          {stateEdit !== null ? (
            <>
              <textarea value={stateEdit} onChange={e=>setStateEdit(e.target.value)} style={S.ta} />
              <div style={{ display:'flex', gap:8, marginTop:8 }}>
                <button onClick={()=>setStateEdit(null)} style={S.btn}>Cancel</button>
                <button onClick={()=>saveState.mutate()} disabled={saveState.isPending} style={S.btnP}><Icon name="save" size={14}/>{saveState.isPending?'Saving…':'Save'}</button>
              </div>
            </>
          ) : (
            <pre style={{ background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'12px 14px', fontFamily:'var(--font-mono)', fontSize:11, lineHeight:1.7, overflow:'auto', maxHeight:320, margin:0, color:'rgba(255,255,255,0.7)', whiteSpace:'pre-wrap' }}>
              {stateQ.data?.state || '(empty)'}
            </pre>
          )}
        </div>
      </div>
    </div>
  )
}
