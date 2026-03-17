/**
 * pages/QuotasPage.tsx - ZFS Quotas (Phase 7)
 *
 * GET  /api/zfs/dataset/quota?dataset=   → { quota, refquota }
 * POST /api/zfs/datasets { action:'set_quota', dataset, quota }
 * GET  /api/zfs/quota/usergroup          → { quotas: UGQuota[] }
 * POST /api/zfs/quota/usergroup          → { dataset, type, id, quota }
 */

import { useState } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface UGQuota { dataset: string; type: 'user'|'group'; id: string; quota: number; used?: number }

function fmtSize(bytes:number):string {
  if (!bytes) return '0 B'
  const u=['B','KB','MB','GB','TB','PB']
  const i=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),u.length-1)
  return `${(bytes/Math.pow(1024,i)).toFixed(1)} ${u[i]}`
}
function parseSize(s:string):number {
  const m=s.match(/^([\d.]+)\s*(B|KB|MB|GB|TB|PB)?$/i)
  if(!m)return 0
  const mul:Record<string,number>={b:1,kb:1024,mb:1024**2,gb:1024**3,tb:1024**4,pb:1024**5}
  return Math.round(parseFloat(m[1])*(mul[(m[2]||'B').toLowerCase()]??1))
}

function DatasetQuotaLookup() {
  const [dataset, setDataset] = useState('')
  const [queried, setQueried] = useState<string|null>(null)
  const [quota, setQuota] = useState('')

  const qc = useQueryClient()
  const quotaQ = useQuery({
    queryKey: ['quota', 'dataset', queried],
    queryFn:  ({ signal }) => api.get<{ success:boolean; quota?:number; refquota?:number }>(`/api/zfs/dataset/quota?dataset=${encodeURIComponent(queried!)}`, signal),
    enabled:  !!queried,
  })

  const setQ = useMutation({
    mutationFn: () => api.post('/api/zfs/datasets', { action:'set_quota', dataset:queried, quota:parseSize(quota) }),
    onSuccess: () => { toast.success('Quota set'); qc.invalidateQueries({ queryKey:['quota','dataset',queried] }) },
    onError: (e:Error) => toast.error(e.message),
  })
  const removeQ = useMutation({
    mutationFn: () => api.post('/api/zfs/datasets', { action:'set_quota', dataset:queried, quota:0 }),
    onSuccess: () => { toast.success('Quota removed'); qc.invalidateQueries({ queryKey:['quota','dataset',queried] }) },
    onError: (e:Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius:'var(--radius-xl)', padding:22, marginBottom:24 }}>
      <div style={{ fontWeight:700, marginBottom:14 }}>Dataset Quota</div>
      <div style={{ display:'flex', gap:8, marginBottom:14 }}>
        <input value={dataset} onChange={e=>setDataset(e.target.value)} placeholder="tank/data" className="input" style={{ flex:1, fontFamily:'var(--font-mono)' }} onKeyDown={e=>e.key==='Enter'&&setQueried(dataset)} />
        <button onClick={()=>setQueried(dataset)} className="btn btn-primary"><Icon name="search" size={14}/>Load</button>
      </div>
      {quotaQ.isLoading && <Skeleton height={60}/>}
      {quotaQ.data && queried && (
        <div style={{ display:'flex', gap:16, alignItems:'center', padding:'12px 16px', background:'var(--surface)', borderRadius:'var(--radius-sm)', marginBottom:12 }}>
          <div><div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Current quota</div><div style={{ fontFamily:'var(--font-mono)', fontWeight:700 }}>{quotaQ.data.quota ? fmtSize(quotaQ.data.quota) : 'None'}</div></div>
          {quotaQ.data.refquota != null && <div><div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Refquota</div><div style={{ fontFamily:'var(--font-mono)', fontWeight:700 }}>{quotaQ.data.refquota ? fmtSize(quotaQ.data.refquota) : 'None'}</div></div>}
        </div>
      )}
      {queried && !quotaQ.isLoading && (
        <div style={{ display:'flex', gap:8, alignItems:'flex-end' }}>
          <label className="field" style={{ flex:1 }}>
            <span className="field-label">New quota (e.g. 100GB)</span>
            <input value={quota} onChange={e=>setQuota(e.target.value)} placeholder="100GB" className="input"/>
          </label>
          <button onClick={()=>setQ.mutate()} disabled={!quota.trim()||setQ.isPending} className="btn btn-primary"><Icon name="save" size={14}/>{setQ.isPending?'Setting…':'Set'}</button>
          <button onClick={()=>removeQ.mutate()} disabled={removeQ.isPending} className="btn btn-ghost" style={{ color:'var(--error)', borderColor:'var(--error-border)' }}><Icon name="delete" size={14}/>Remove</button>
        </div>
      )}
    </div>
  )
}

function UserGroupQuotas() {
  const qc = useQueryClient()
  const [ds, setDs] = useState(''); const [type, setType] = useState<'user'|'group'>('user')
  const [uid, setUid] = useState(''); const [quota, setQuota] = useState('')

  const ugQ = useQuery({ queryKey:['quota','usergroup'], queryFn:({signal})=>api.get<{success:boolean;quotas:UGQuota[]}>('/api/zfs/quota/usergroup',signal) })
  const set = useMutation({
    mutationFn: () => api.post('/api/zfs/quota/usergroup', { dataset:ds, type, id:uid, quota:parseSize(quota) }),
    onSuccess: () => { toast.success('Quota set'); setDs(''); setUid(''); setQuota(''); qc.invalidateQueries({queryKey:['quota','usergroup']}) },
    onError: (e:Error)=>toast.error(e.message),
  })

  const quotas = ugQ.data?.quotas??[]

  return (
    <div className="card" style={{ borderRadius:'var(--radius-xl)', padding:22 }}>
      <div style={{ fontWeight:700, marginBottom:14 }}>User / Group Quotas</div>
      <div style={{ display:'grid', gridTemplateColumns:'1fr 80px 1fr 100px auto', gap:8, alignItems:'flex-end', marginBottom:20 }}>
        {[['Dataset','text',ds,setDs,'tank/home'],['User/Group ID','text',uid,setUid,'1000']].map(([lbl,t,val,setter,ph])=>(
          <label key={lbl as string} className="field">
            <span className="field-label">{lbl as string}</span>
            <input type={t as string} value={val as string} onChange={e=>(setter as React.Dispatch<React.SetStateAction<string>>)(e.target.value)} placeholder={ph as string} className="input"/>
          </label>
        ))}
        <label className="field">
          <span className="field-label">Type</span>
          <select value={type} onChange={e=>setType(e.target.value as 'user'|'group')} className="input" style={{appearance:'none'}}><option value="user">user</option><option value="group">group</option></select>
        </label>
        <label className="field">
          <span className="field-label">Quota</span>
          <input value={quota} onChange={e=>setQuota(e.target.value)} placeholder="50GB" className="input"/>
        </label>
        <button onClick={()=>set.mutate()} disabled={!ds.trim()||!uid.trim()||!quota.trim()||set.isPending} className="btn btn-primary" style={{ alignSelf:'flex-end' }}><Icon name="add" size={14}/>{set.isPending?'Setting…':'Set'}</button>
      </div>
      {ugQ.isLoading&&<Skeleton height={100}/>}
      {ugQ.isError&&<ErrorState error={ugQ.error}/>}
      <div style={{ display:'flex', flexDirection:'column', gap:6 }}>
        {quotas.map((q,i)=>(
          <div key={i} style={{ display:'flex', alignItems:'center', gap:12, padding:'10px 14px', background:'var(--surface)', borderRadius:'var(--radius-sm)' }}>
            <Icon name={q.type==='user'?'person':'group'} size={16} style={{ color:'var(--primary)', flexShrink:0 }}/>
            <div style={{ flex:1 }}>
              <span style={{ fontWeight:600, fontSize:'var(--text-sm)' }}>{q.id}</span>
              <span style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', marginLeft:8 }}>on {q.dataset}</span>
            </div>
            <span style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-sm)', color:'var(--primary)' }}>{fmtSize(q.quota)}</span>
            {q.used!=null&&<span style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>used {fmtSize(q.used)}</span>}
          </div>
        ))}
        {!ugQ.isLoading&&quotas.length===0&&<div style={{ textAlign:'center', padding:'24px 0', color:'var(--text-tertiary)', fontSize:'var(--text-sm)' }}>No user/group quotas set</div>}
      </div>
    </div>
  )
}

export function QuotasPage() {
  return (
    <div style={{ maxWidth:900 }}>
      <div className="page-header">
        <h1 className="page-title">Quotas</h1>
        <p className="page-subtitle">ZFS dataset, user and group space limits</p>
      </div>
      <DatasetQuotaLookup/>
      <UserGroupQuotas/>
    </div>
  )
}

