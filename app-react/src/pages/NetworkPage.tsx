/**
 * pages/NetworkPage.tsx - Network (Phase 6)
 *
 * Tabs: Interfaces | VLANs | Bonding | DNS & NTP
 *
 * Every interface, VLAN, bond and DNS change applies at once and is reverted
 * unless confirmed (PendingChangeBanner).
 *
 * Calls:
 *   GET    /api/system/network                     → { success, interfaces[], routes[], dns: { nameservers, search, configured? } }
 *   PUT    /api/system/network { action:'configure', interface, dhcp, ip, netmask, gateway }
 *   PUT    /api/system/network { action:'set_dns', nameservers[], search[] }
 *   GET    /api/network/vlan                       → { success, vlans: [{ name, parent?, vlan_id?, managed, up }] }
 *   POST   /api/network/vlan   { parent, vlan_id, ip?, dhcp? }
 *   DELETE /api/network/vlan   { interface }
 *   GET    /api/network/bond                       → { success, bonds: [{ name, mode, slaves, state, managed }] }
 *   POST   /api/network/bond   { name, mode, slaves[], ip?, dhcp? }
 *   DELETE /api/network/bond/{name}
 *   GET    /api/system/ntp                         → { success, servers[], synced, details }
 *   POST   /api/system/ntp     { servers[] }
 */

import { useState, useEffect, useRef } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, getSessionId, getUsername, getCsrfToken, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'
import { useRouter } from '@tanstack/react-router'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { PendingChangeBanner, useChangeApplied } from '@/components/network/PendingChangeBanner'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Iface {
  name:     string
  ip?:      string
  netmask?: string
  gateway?: string
  mac?:     string
  speed?:   string
  mtu?:     number
  dhcp?:    boolean
  primary?: boolean
  up?:      boolean
  state?:   string
  type?:    string
  managed?: boolean   // configured by DPlaneOS (else by the system configuration)
  member_of?: string  // bond this interface belongs to
}

interface NetworkResponse {
  success:    boolean
  interfaces: Iface[]
  dns?:       { nameservers?: string[]; search?: string[]; configured?: { servers?: string[]; search?: string[] } }
}

interface VLANInfo { name: string; parent?: string; vlan_id?: number; managed: boolean; up: boolean }
interface BondInfo { name: string; mode: string; slaves: string[] | null; state: string; managed: boolean }

interface NTPResponse {
  success: boolean
  servers: string[]
  synced:  boolean
  details: string
}

// ---------------------------------------------------------------------------
// Configure Interface Modal
// Sends: PUT /api/system/network { action:'configure', interface, dhcp, ip?, netmask?, gateway? }
// After success, caller shows the 30-second confirm banner.
// ---------------------------------------------------------------------------

function ConfigureIfaceModal({ iface, onClose, onDone }: {
  iface:   Iface
  onClose: () => void
  onDone:  () => void
}) {
  const applied = useChangeApplied()
  const [dhcp,    setDhcp]    = useState(iface.dhcp ?? false)
  const [ip,      setIp]      = useState(iface.ip      ?? '')
  const [netmask, setNetmask] = useState(iface.netmask ?? '255.255.255.0')
  const [gateway, setGateway] = useState(iface.gateway ?? '')

  const save = useMutation({
    mutationFn: async () => ensureOk(await api.put<{ success?: boolean; error?: string }>('/api/system/network', {
      action:    'configure',
      interface: iface.name,
      dhcp,
      ip:        dhcp ? undefined : ip        || undefined,
      netmask:   dhcp ? undefined : netmask   || undefined,
      gateway:   dhcp ? undefined : gateway   || undefined,
    })),
    onSuccess: () => { applied(`${iface.name}: applied - confirm to keep it`); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function labelRow(label: string, children: React.ReactNode) {
    return (
      <label className="field">
        <span className="field-label">{label}</span>
        {children}
      </label>
    )
  }

  return (
    <Modal title={`Configure ${iface.name}`} onClose={onClose}>
      {/* DHCP toggle */}
      <label style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', cursor: 'pointer' }}>
        <input type="checkbox" checked={dhcp} onChange={e => setDhcp(e.target.checked)}
          style={{ width: 16, height: 16, accentColor: 'var(--primary)', cursor: 'pointer' }} />
        <div>
          <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>Obtain IP via DHCP</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Automatically assigned address</div>
        </div>
      </label>

      {!dhcp && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          {labelRow('IP Address',
            <input value={ip} onChange={e => setIp(e.target.value)} placeholder="192.168.1.100"
              className="input" style={{ fontFamily: 'var(--font-mono)' }} autoFocus />
          )}
          {labelRow('Subnet Mask',
            <input value={netmask} onChange={e => setNetmask(e.target.value)} placeholder="255.255.255.0"
              className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          )}
          {labelRow('Default Gateway',
            <input value={gateway} onChange={e => setGateway(e.target.value)} placeholder="192.168.1.1"
              className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          )}
        </div>
      )}

      <div style={{ padding: '10px 14px', background: 'var(--info-bg)', border: '1px solid var(--info-border)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--info)', display: 'flex', gap: 8, alignItems: 'flex-start' }}>
        <Icon name="info" size={14} style={{ flexShrink: 0, marginTop: 1 }} />
        Network changes apply immediately. You have <strong>90 seconds</strong> to confirm before the system reverts. If you change the address you are connected to, open the interface on the new address and confirm there.
      </div>

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
          <Icon name="save" size={15} />{save.isPending ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// InterfacesTab
// ---------------------------------------------------------------------------

function InterfacesTab() {
  const qc = useQueryClient()
  const [configIface, setConfigIface] = useState<Iface | null>(null)

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn:  ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
    refetchInterval: 15_000,
  })

  // Container plumbing is not configured here.
  const ifaces = (netQ.data?.interfaces ?? []).filter(i => i.type !== 'loopback' && !/^(veth|docker|br-)/.test(i.name))

  if (netQ.isLoading) return <Skeleton height={300} />
  if (netQ.isError)   return <ErrorState error={netQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['network', 'info'] })} />

  return (
    <>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {ifaces.map(iface => {
          const isUp = iface.up !== false && iface.state !== 'down'
          return (
            <div key={iface.name} style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '16px 20px', background: 'var(--bg-card)', border: `1px solid ${isUp ? 'rgba(16,185,129,0.2)' : 'var(--border)'}`, borderRadius: 'var(--radius-lg)' }}>
              <div style={{ width: 42, height: 42, borderRadius: 'var(--radius-md)', background: isUp ? 'rgba(16,185,129,0.1)' : 'var(--surface)', border: `1px solid ${isUp ? 'rgba(16,185,129,0.25)' : 'var(--border)'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
                <Icon name="lan" size={22} style={{ color: isUp ? 'var(--success)' : 'var(--text-tertiary)' }} />
              </div>

              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 3 }}>
                  <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>{iface.name}</span>
                  {iface.primary && (
                    <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'var(--primary-bg)', color: 'var(--primary)', fontSize: 10, fontWeight: 700, letterSpacing: '0.3px' }}>PRIMARY</span>
                  )}
                  {iface.dhcp && (
                    <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'rgba(99,102,241,0.12)', color: '#818cf8', fontSize: 10, fontWeight: 700 }}>DHCP</span>
                  )}
                  {iface.member_of && (
                    <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'var(--surface)', color: 'var(--text-secondary)', fontSize: 10, fontWeight: 700 }}>IN {iface.member_of.toUpperCase()}</span>
                  )}
                  <span style={{ width: 8, height: 8, borderRadius: '50%', background: isUp ? 'var(--success)' : 'var(--text-tertiary)', boxShadow: isUp ? '0 0 5px var(--success)' : 'none', display: 'inline-block' }} />
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 14, flexWrap: 'wrap' }}>
                  <span>{iface.ip ? `${iface.ip}${iface.netmask ? ' / ' + iface.netmask : ''}` : 'No IP'}</span>
                  {iface.gateway && <span>GW: {iface.gateway}</span>}
                  {iface.mac     && <span style={{ fontFamily: 'var(--font-mono)' }}>{iface.mac}</span>}
                  {iface.speed   && <span>{iface.speed}</span>}
                  {iface.mtu     && <span>MTU {iface.mtu}</span>}
                  <span title={iface.managed ? 'Configured on this page' : 'Configured by the system configuration (DHCP by default)'}>
                    {iface.managed ? 'configured here' : 'system default'}
                  </span>
                </div>
              </div>

              {iface.member_of ? (
                <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>configure {iface.member_of}</span>
              ) : (
                <button onClick={() => setConfigIface(iface)} className="btn btn-ghost">
                  <Icon name="settings" size={14} />Configure
                </button>
              )}
            </div>
          )
        })}
        {ifaces.length === 0 && (
          <div style={{ textAlign: 'center', padding: '48px 0', color: 'var(--text-tertiary)' }}>No network interfaces detected</div>
        )}
      </div>

      {configIface && (
        <ConfigureIfaceModal
          iface={configIface}
          onClose={() => setConfigIface(null)}
          onDone={() => qc.invalidateQueries({ queryKey: ['network', 'info'] })}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// VLANsTab
// ---------------------------------------------------------------------------

function VLANsTab() {
  const qc = useQueryClient()
  const applied = useChangeApplied()
  const [parent, setParent] = useState('')
  const [vlanId, setVlanId] = useState('')
  const [ip, setIp] = useState('')
  const [dhcp, setDhcp] = useState(false)
  const { confirm, ConfirmDialog } = useConfirm()

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn: ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
  })
  const vlanQ = useQuery({
    queryKey: ['network', 'vlan'],
    queryFn: ({ signal }) => api.get<{ success: boolean; vlans: VLANInfo[] }>('/api/network/vlan', signal),
  })

  const createVLAN = useMutation({
    mutationFn: async () => {
      if (!parent.trim() || !vlanId) throw new Error('Parent interface and VLAN ID are required')
      return ensureOk(await api.post<{ success?: boolean; error?: string }>('/api/network/vlan', {
        parent: parent.trim(), vlan_id: Number(vlanId), ip: dhcp ? undefined : ip.trim() || undefined, dhcp,
      }))
    },
    onSuccess: () => {
      applied(`VLAN ${parent}.${vlanId}: applied - confirm to keep it`)
      setVlanId(''); setIp(''); setDhcp(false)
      qc.invalidateQueries({ queryKey: ['network'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteVLAN = useMutation({
    mutationFn: async (iface: string) => ensureOk(await api.delete<{ success?: boolean; error?: string }>('/api/network/vlan', { interface: iface })),
    onSuccess: () => { applied(); qc.invalidateQueries({ queryKey: ['network'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  // Parents: physical interfaces and bonds, not VLANs or bond members.
  const parents = (netQ.data?.interfaces ?? []).filter(i =>
    !i.name.includes('.') && !i.member_of && i.type !== 'loopback' && !/^(veth|docker|br-)/.test(i.name))
  const vlans = vlanQ.data?.vlans ?? []

  return (
    <>
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginBottom: 24 }}>
        <div style={{ fontWeight: 700, marginBottom: 16 }}>Create VLAN Interface</div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px 1fr auto auto', gap: 12, alignItems: 'flex-end' }}>
          <label className="field">
            <span className="field-label">Parent Interface</span>
            <select value={parent} onChange={e => setParent(e.target.value)} className="input" style={{ appearance: 'none' }}>
              <option value="">Select…</option>
              {parents.map(i => <option key={i.name} value={i.name}>{i.name}</option>)}
            </select>
          </label>
          <label className="field">
            <span className="field-label">VLAN ID</span>
            <input type="number" value={vlanId} onChange={e => setVlanId(e.target.value)} placeholder="10" min={1} max={4094} className="input" />
          </label>
          <label className="field">
            <span className="field-label">Address (optional)</span>
            <input value={ip} onChange={e => setIp(e.target.value)} disabled={dhcp} placeholder="10.0.10.5/24" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 10, fontSize: 'var(--text-sm)' }}>
            <input type="checkbox" checked={dhcp} onChange={e => setDhcp(e.target.checked)} />DHCP
          </label>
          <button onClick={() => createVLAN.mutate()} disabled={createVLAN.isPending || !parent || !vlanId} className="btn btn-primary" style={{ marginBottom: 4 }}>
            <Icon name="add" size={15} />{createVLAN.isPending ? 'Creating…' : 'Create VLAN'}
          </button>
        </div>
        <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', marginTop: 8 }}>
          The interface is named <code>parent.id</code> (e.g. eth0.10). The parent keeps its own address.
        </div>
      </div>

      <div style={{ fontWeight: 700, marginBottom: 12 }}>Current VLANs</div>
      {vlanQ.isLoading && <Skeleton height={80} />}
      {vlanQ.isError   && <ErrorState error={vlanQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['network', 'vlan'] })} />}

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <table className="data-table">
          <thead>
            <tr>
              {['Interface', 'Parent', 'VLAN ID', 'State', 'Actions'].map(h => <th key={h}>{h}</th>)}
            </tr>
          </thead>
          <tbody>
            {vlans.map(v => (
              <tr key={v.name}>
                <td style={{ fontWeight: 600 }}>{v.name}</td>
                <td>{v.parent ?? '-'}</td>
                <td>{v.vlan_id ?? '-'}</td>
                <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{v.up ? 'up' : 'down'}</td>
                <td>
                  {v.managed ? (
                    <button onClick={async () => { if (await confirm({ title: `Delete VLAN "${v.name}"?`, danger: true, confirmLabel: 'Delete' })) { deleteVLAN.mutate(v.name) } }} disabled={deleteVLAN.isPending} className="btn btn-xs btn-danger">
                      <Icon name="delete" size={12} />Remove
                    </button>
                  ) : <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>system configuration</span>}
                </td>
              </tr>
            ))}
            {vlans.length === 0 && !vlanQ.isLoading && (
              <tr><td colSpan={5} style={{ textAlign: 'center', padding: '32px', color: 'var(--text-tertiary)' }}>No VLANs configured</td></tr>
            )}
          </tbody>
        </table>
      </div>
      <ConfirmDialog />
    </>
  )
}

// ---------------------------------------------------------------------------
// BondingTab
// ---------------------------------------------------------------------------

const BOND_MODES = [
  { value: 'active-backup', label: 'Active-Backup  (failover, 1 active at a time)' },
  { value: '802.3ad',       label: 'LACP / 802.3ad  (requires switch support)' },
  { value: 'balance-rr',    label: 'Round-Robin  (balance-rr)' },
  { value: 'balance-xor',   label: 'XOR  (balance-xor)' },
  { value: 'balance-tlb',   label: 'Adaptive TX Load Balancing  (balance-tlb)' },
  { value: 'balance-alb',   label: 'Adaptive Load Balancing  (balance-alb)' },
  { value: 'broadcast',     label: 'Broadcast' },
]

function BondingTab() {
  const qc = useQueryClient()
  const applied = useChangeApplied()
  const [bondName, setBondName] = useState('bond0')
  const [mode, setMode] = useState('active-backup')
  const [members, setMembers] = useState<string[]>([])
  const [ip, setIp] = useState('')
  const [dhcp, setDhcp] = useState(false)
  const { confirm, ConfirmDialog } = useConfirm()

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn: ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
  })
  const bondQ = useQuery({
    queryKey: ['network', 'bond'],
    queryFn: ({ signal }) => api.get<{ success: boolean; bonds: BondInfo[] }>('/api/network/bond', signal),
  })

  const createBond = useMutation({
    mutationFn: async () => {
      if (!bondName.trim()) throw new Error('Bond name is required')
      if (members.length === 0) throw new Error('Choose at least one interface')
      return ensureOk(await api.post<{ success?: boolean; error?: string }>('/api/network/bond', {
        name: bondName.trim(), mode, slaves: members, ip: dhcp ? undefined : ip.trim() || undefined, dhcp,
      }))
    },
    onSuccess: () => {
      applied(`Bond ${bondName}: applied - confirm to keep it`)
      setMembers([]); setIp(''); setDhcp(false)
      qc.invalidateQueries({ queryKey: ['network'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteBond = useMutation({
    mutationFn: async (name: string) => ensureOk(await api.delete<{ success?: boolean; error?: string }>(`/api/network/bond/${name}`)),
    onSuccess: () => { applied(); qc.invalidateQueries({ queryKey: ['network'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const candidates = (netQ.data?.interfaces ?? []).filter(i =>
    (i.type === 'ethernet' || !i.type) && !i.name.includes('.') && !i.member_of && !/^(veth|docker|br-)/.test(i.name))
  const bonds = bondQ.data?.bonds ?? []

  return (
    <>
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginBottom: 24 }}>
        <div style={{ fontWeight: 700, marginBottom: 16 }}>Create Bond Interface</div>
        <div style={{ display: 'grid', gap: 14 }}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
            <label className="field">
              <span className="field-label">Bond Name</span>
              <input value={bondName} onChange={e => setBondName(e.target.value)} className="input" placeholder="bond0" />
            </label>
            <label className="field">
              <span className="field-label">Bonding Mode</span>
              <select value={mode} onChange={e => setMode(e.target.value)} className="input" style={{ appearance: 'none' }}>
                {BOND_MODES.map(m => <option key={m.value} value={m.value}>{m.label}</option>)}
              </select>
            </label>
          </div>
          <div className="field">
            <span className="field-label">Member Interfaces</span>
            <div style={{ display: 'flex', gap: 14, flexWrap: 'wrap' }}>
              {candidates.map(i => (
                <label key={i.name} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)' }}>
                  <input type="checkbox" checked={members.includes(i.name)}
                    onChange={e => setMembers(m => e.target.checked ? [...m, i.name] : m.filter(x => x !== i.name))} />
                  {i.name}{i.ip ? <span style={{ color: 'var(--text-tertiary)' }}> ({i.ip})</span> : null}
                </label>
              ))}
              {candidates.length === 0 && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>No free interfaces</span>}
            </div>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', gap: 14, alignItems: 'flex-end' }}>
            <label className="field">
              <span className="field-label">Address (optional)</span>
              <input value={ip} onChange={e => setIp(e.target.value)} disabled={dhcp} placeholder="192.168.1.10/24" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 10, fontSize: 'var(--text-sm)' }}>
              <input type="checkbox" checked={dhcp} onChange={e => setDhcp(e.target.checked)} />DHCP
            </label>
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            The members lose their own addresses. Without an address here the bond takes over the configuration of the first member that has one, so a bond made of the interface you are connected through stays reachable (DHCP may hand out a new address: the bond has its own MAC address).
          </div>
          <div>
            <button onClick={() => createBond.mutate()} disabled={createBond.isPending || members.length === 0} className="btn btn-primary">
              <Icon name="cable" size={15} />{createBond.isPending ? 'Creating…' : 'Create Bond'}
            </button>
          </div>
        </div>
      </div>

      {bonds.length > 0 && (
        <>
          <div style={{ fontWeight: 700, marginBottom: 12 }}>Bond Interfaces</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {bonds.map(b => (
              <div key={b.name} className="card" style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', borderRadius: 'var(--radius-md)' }}>
                <Icon name="device_hub" size={20} style={{ color: 'var(--primary)', flexShrink: 0 }} />
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 700 }}>{b.name} <span style={{ fontWeight: 400, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{b.mode} · {b.state}</span></div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {(b.slaves ?? []).length ? (b.slaves ?? []).join(', ') : 'no members'}
                    {(() => { const i = netQ.data?.interfaces.find(x => x.name === b.name); return i?.ip ? ` · ${i.ip}` : '' })()}
                  </div>
                </div>
                {b.managed ? (
                  <button onClick={async () => { if (await confirm({ title: `Delete bond "${b.name}"?`, message: 'Its members go back to the system configuration (DHCP by default).', danger: true, confirmLabel: 'Delete' })) { deleteBond.mutate(b.name) } }} disabled={deleteBond.isPending} className="btn btn-sm btn-danger">
                    <Icon name="delete" size={13} />Delete
                  </button>
                ) : <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>system configuration</span>}
              </div>
            ))}
          </div>
        </>
      )}
      <ConfirmDialog />
    </>
  )
}

// ---------------------------------------------------------------------------
// DnsNtpTab
// ---------------------------------------------------------------------------

function DnsNtpTab() {
  const qc = useQueryClient()
  const applied = useChangeApplied()

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn: ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
  })
  const ntpQ = useQuery({
    queryKey: ['system', 'ntp'],
    queryFn: ({ signal }) => api.get<NTPResponse>('/api/system/ntp', signal),
    refetchInterval: 30_000,
  })

  const [dnsEdit, setDnsEdit] = useState<string | null>(null)
  const [searchEdit, setSearchEdit] = useState<string | null>(null)
  const [ntpEdit, setNtpEdit] = useState<string | null>(null)

  // Effective values: user edit OR server value (configured here, else in use)
  const dns = netQ.data?.dns
  const dnsStr = dnsEdit ?? (dns?.configured?.servers ?? dns?.nameservers ?? []).join(', ')
  const searchStr = searchEdit ?? (dns?.configured?.search ?? dns?.search ?? []).filter(d => d !== '.').join(', ')
  const ntpStr = ntpEdit ?? ntpQ.data?.servers?.join(', ') ?? ''

  const saveDns = useMutation({
    mutationFn: async () => {
      const nameservers = dnsStr.split(',').map(s => s.trim()).filter(Boolean)
      const search = searchStr.split(/[\s,]+/).map(s => s.trim()).filter(Boolean)
      return ensureOk(await api.put<{ success?: boolean; error?: string }>('/api/system/network', { action: 'set_dns', nameservers, search }))
    },
    onSuccess: () => {
      applied('DNS: applied - confirm to keep it')
      setDnsEdit(null); setSearchEdit(null)
      qc.invalidateQueries({ queryKey: ['network', 'info'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const saveNtp = useMutation({
    mutationFn: () => {
      const servers = ntpStr.split(',').map(s => s.trim()).filter(Boolean)
      if (servers.length === 0) throw new Error('At least one NTP server is required')
      return api.post('/api/system/ntp', { servers })
    },
    onSuccess: () => {
      toast.success('NTP servers saved')
      setNtpEdit(null)
      qc.invalidateQueries({ queryKey: ['system', 'ntp'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 680 }}>
      {/* DNS */}
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
          <Icon name="dns" size={20} style={{ color: 'var(--primary)' }} />
          <div style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>DNS Nameservers</div>
        </div>
        {netQ.isLoading ? <Skeleton height={56} /> : (
          <div style={{ display: 'grid', gap: 10 }}>
            <label className="field">
              <span className="field-label">Servers</span>
              <input value={dnsStr} onChange={e => setDnsEdit(e.target.value)} placeholder="192.168.1.1, 9.9.9.9"
                className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
            <label className="field">
              <span className="field-label">Search domains (optional)</span>
              <input value={searchStr} onChange={e => setSearchEdit(e.target.value)} placeholder="home.lan"
                className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                {dns?.configured ? 'Set here; used for all interfaces.' : 'In use now (from DHCP or the system configuration).'}
              </span>
              <button onClick={() => saveDns.mutate()} disabled={saveDns.isPending} className="btn btn-primary">
                {saveDns.isPending ? 'Saving…' : 'Save'}
              </button>
            </div>
          </div>
        )}
      </div>

      {/* NTP */}
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
          <Icon name="schedule" size={20} style={{ color: 'var(--primary)' }} />
          <div style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>NTP Servers</div>
          {ntpQ.data && (
            <span style={{ marginLeft: 'auto', padding: '2px 10px', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', fontWeight: 700,
              background: ntpQ.data.synced ? 'var(--success-bg)' : 'rgba(251,191,36,0.1)',
              border:     ntpQ.data.synced ? '1px solid var(--success-border)' : '1px solid rgba(251,191,36,0.3)',
              color:      ntpQ.data.synced ? 'var(--success)' : 'rgba(251,191,36,0.9)' }}>
              {ntpQ.data.synced ? '✓ Synchronized' : '⚠ Not synced'}
            </span>
          )}
        </div>
        {ntpQ.isLoading ? <Skeleton height={80} /> : (
          <>
            <div style={{ display: 'flex', gap: 10, marginBottom: 12, marginTop: 12 }}>
              <input value={ntpStr} onChange={e => setNtpEdit(e.target.value)}
                placeholder="0.pool.ntp.org, 1.pool.ntp.org"
                className="input" style={{ flex: 1, fontFamily: 'var(--font-mono)' }} />
              <button onClick={() => saveNtp.mutate()} disabled={saveNtp.isPending} className="btn btn-primary">
                {saveNtp.isPending ? 'Saving…' : 'Save'}
              </button>
            </div>
            {ntpQ.data?.details && (
              <>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>NTP status output</div>
                <pre style={{ background: 'var(--surface)', borderRadius: 'var(--radius-sm)', padding: '10px 14px', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'rgba(255,255,255,0.55)', margin: 0, whiteSpace: 'pre-wrap', maxHeight: 180, overflow: 'auto', lineHeight: 1.7 }}>
                  {ntpQ.data.details}
                </pre>
              </>
            )}
          </>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// DiagnosticsTab
// ---------------------------------------------------------------------------

function DiagnosticsTab() {
  const [type, setType] = useState<'ping' | 'dns' | 'traceroute'>('ping')
  const [target, setTarget] = useState('')
  const [output, setOutput] = useState<string[]>([])
  const [isRunning, setIsRunning] = useState(false)
  const [count, setCount] = useState(4)
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [output])

  async function run() {
    if (!target.trim() || isRunning) return
    setIsRunning(true)
    setOutput([])

    try {
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        'X-Session-ID': getSessionId() ?? '',
        'X-User': getUsername() ?? '',
        'X-CSRF-Token': getCsrfToken(),
      }

      const response = await fetch('/api/system/diagnostics', {
        method: 'POST',
        headers,
        body: JSON.stringify({ type, target: target.trim(), count }),
      })

      if (!response.ok) {
        const errData = await response.json().catch(() => ({}))
        throw new Error(errData.error || `Error ${response.status}`)
      }

      const reader = response.body?.getReader()
      if (!reader) throw new Error('Streaming not supported')

      const decoder = new TextDecoder()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        const chunk = decoder.decode(value, { stream: true })
        setOutput(prev => [...prev, ...chunk.split('\n').filter(l => l.length > 0)])
      }
    } catch (e: unknown) {
      setOutput(prev => [...prev, `[ERROR] ${e instanceof Error ? e.message : String(e)}`])
    } finally {
      setIsRunning(false)
    }
  }

  return (
    <>
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginBottom: 20 }}>
        <div style={{ fontWeight: 700, marginBottom: 16 }}>Network Troubleshooting</div>
        <div style={{ display: 'grid', gridTemplateColumns: '120px 1fr 80px auto', gap: 12, alignItems: 'flex-end' }}>
          <label className="field">
            <span className="field-label">Tool</span>
            <select value={type} onChange={e => setType(e.target.value as 'ping'|'dns'|'traceroute')} className="input" style={{ appearance: 'none', background: 'var(--surface)' }}>
              <option value="ping">Ping</option>
              <option value="dns">DNS Lookup</option>
              <option value="traceroute">Traceroute</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">Target (Hostname or IP)</span>
            <input value={target} onChange={e => setTarget(e.target.value)} placeholder="google.com" className="input" onKeyDown={e => e.key === 'Enter' && run()} />
          </label>
          {type === 'ping' && (
            <label className="field">
              <span className="field-label">Count</span>
              <input type="number" value={count} onChange={e => setCount(Number(e.target.value))} min={1} max={10} className="input" />
            </label>
          )}
          <button onClick={run} disabled={isRunning || !target.trim()} className="btn btn-primary" style={{ marginBottom: 4 }}>
            <Icon name={isRunning ? 'sync' : 'play_arrow'} size={15} style={{ animation: isRunning ? 'spin 2s linear infinite' : 'none' }} />
            {isRunning ? 'Running…' : 'Run'}
          </button>
        </div>
      </div>

      <div ref={scrollRef} style={{ background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', padding: '16px 20px', minHeight: '300px', maxHeight: '500px', overflowY: 'auto', fontFamily: 'var(--font-mono)', fontSize: '13px', lineHeight: '1.6', color: 'var(--text)', position: 'relative' }}>
        {output.length === 0 && !isRunning && (
          <div style={{ color: 'var(--text-tertiary)', textAlign: 'center', marginTop: 120 }}>
            <Icon name="terminal" size={32} style={{ opacity: 0.1, marginBottom: 12 }} />
            <div>Diagnostics output will appear here</div>
          </div>
        )}
        {output.map((line, i) => (
          <div key={i} style={{ borderBottom: '1px solid rgba(255,255,255,0.03)', paddingBottom: 2 }}>{line}</div>
        ))}
        {isRunning && (
          <div style={{ opacity: 0.5, marginTop: 4 }}>
            <span style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', background: 'var(--primary)', animation: 'pulse 1.5s infinite', marginRight: 8 }} />
            Tracing in progress...
          </div>
        )}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// NetworkPage
// ---------------------------------------------------------------------------

type Tab = 'interfaces' | 'vlans' | 'bonding' | 'dns' | 'diagnostics'

export function NetworkPage() {
  const router = useRouter()
  const [tab, setTab] = useState<Tab>('interfaces')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'interfaces', label: 'Interfaces', icon: 'lan' },
    { id: 'vlans',      label: 'VLANs',      icon: 'account_tree' },
    { id: 'bonding',    label: 'Bonding',     icon: 'device_hub' },
    { id: 'dns',        label: 'DNS & NTP',   icon: 'dns' },
    { id: 'diagnostics', label: 'Diagnostics', icon: 'troubleshoot' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
        <div>
          <h1 className="page-title">Network</h1>
          <p className="page-subtitle">Interfaces, VLANs, bonding, DNS and NTP</p>
        </div>
        <button onClick={() => router.navigate({ to: '/firewall' })} className="btn btn-ghost" style={{ marginTop: 8 }}>
          <Icon name="local_fire_department" size={16} />Firewall Settings
        </button>
      </div>

      <div className="tabs-underline">
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      <PendingChangeBanner />

      {tab === 'interfaces' && <InterfacesTab />}
      {tab === 'vlans'      && <VLANsTab />}
      {tab === 'bonding'    && <BondingTab />}
      {tab === 'dns'        && <DnsNtpTab />}
      {tab === 'diagnostics' && <DiagnosticsTab />}
    </div>
  )
}

