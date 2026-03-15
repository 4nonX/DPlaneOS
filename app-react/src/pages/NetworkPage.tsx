/**
 * pages/NetworkPage.tsx — Network (Phase 6)
 *
 * Tabs: Interfaces | VLANs | Bonding | DNS & NTP
 *
 * Calls:
 *   GET  /api/system/network                          → { success, interfaces[], dns[], gateway }
 *   POST /api/network/apply  { action:'configure', interface, dhcp, ip, netmask, gateway, mtu }
 *   POST /api/network/apply  { action:'set_dns', dns[] }
 *   POST /api/network/confirm                         → confirm applied config
 *   GET  /api/network/vlan                            → { success, vlans: string }
 *   POST /api/network/vlan   { name, parent, id }     → create VLAN
 *   POST /api/network/bond   { name, mode, slaves[] } → create bond
 *   GET  /api/system/ntp                              → { success, servers[], synced, details }
 *   POST /api/system/ntp     { servers[] }
 */

import { useState, useEffect, useRef } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'

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
}

interface NetworkResponse {
  success:    boolean
  interfaces: Iface[]
  dns?:       string[]
  gateway?:   string
}

interface NTPResponse {
  success: boolean
  servers: string[]
  synced:  boolean
  details: string
}

// ---------------------------------------------------------------------------
// Configure Interface Modal
// Sends: POST /api/network/apply { action:'configure', interface, dhcp, ip?, netmask?, gateway?, mtu }
// After success, caller shows the 30-second confirm banner.
// ---------------------------------------------------------------------------

function ConfigureIfaceModal({ iface, onClose, onDone }: {
  iface:   Iface
  onClose: () => void
  onDone:  () => void
}) {
  const [dhcp,    setDhcp]    = useState(iface.dhcp ?? true)
  const [ip,      setIp]      = useState(iface.ip      ?? '')
  const [netmask, setNetmask] = useState(iface.netmask ?? '255.255.255.0')
  const [gateway, setGateway] = useState(iface.gateway ?? '')
  const [mtu,     setMtu]     = useState(String(iface.mtu ?? 1500))

  const save = useMutation({
    mutationFn: () => api.post('/api/network/apply', {
      action:    'configure',
      interface: iface.name,
      dhcp,
      ip:        dhcp ? undefined : ip        || undefined,
      netmask:   dhcp ? undefined : netmask   || undefined,
      gateway:   dhcp ? undefined : gateway   || undefined,
      mtu:       Number(mtu) || 1500,
    }),
    onSuccess: () => { toast.success(`${iface.name} — changes applied`); onDone(); onClose() },
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
          {labelRow('MTU',
            <input type="number" value={mtu} onChange={e => setMtu(e.target.value)} min={576} max={9000} className="input" />
          )}
        </div>
      )}

      {dhcp && (
        <label className="field">
          <span className="field-label">MTU</span>
          <input type="number" value={mtu} onChange={e => setMtu(e.target.value)} min={576} max={9000} className="input" style={{ width: 120 }} />
        </label>
      )}

      <div style={{ padding: '10px 14px', background: 'rgba(251,191,36,0.08)', border: '1px solid rgba(251,191,36,0.2)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'rgba(251,191,36,0.85)', display: 'flex', gap: 8, alignItems: 'flex-start' }}>
        <Icon name="warning" size={14} style={{ flexShrink: 0, marginTop: 1 }} />
        Applying new network settings may briefly disconnect you. You will have 30 seconds to confirm the change before it reverts.
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
// ConfirmBanner — shown after a network/apply, counts down 30 s
// ---------------------------------------------------------------------------

function ConfirmBanner({ onConfirm, onDismiss }: { onConfirm: () => void; onDismiss: () => void }) {
  const [secs, setSecs] = useState(30)
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    timer.current = setInterval(() => {
      setSecs(prev => {
        if (prev <= 1) { clearInterval(timer.current!); onDismiss(); return 0 }
        return prev - 1
      })
    }, 1000)
    return () => clearInterval(timer.current!)
  }, [onDismiss])

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', background: 'rgba(251,191,36,0.1)', border: '1px solid rgba(251,191,36,0.35)', borderRadius: 'var(--radius-lg)', marginBottom: 20 }}>
      <Icon name="timer" size={22} style={{ color: 'rgba(251,191,36,0.9)', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, color: 'rgba(251,191,36,0.9)' }}>Network changes applied</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Auto-reverting in {secs}s — confirm to keep the new configuration</div>
      </div>
      <button onClick={onConfirm} className="btn btn-primary" style={{ background: 'rgba(251,191,36,0.9)' }}>
        <Icon name="check" size={15} />Confirm
      </button>
      <button onClick={onDismiss} className="btn btn-ghost">Discard</button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// InterfacesTab
// ---------------------------------------------------------------------------

function InterfacesTab() {
  const qc = useQueryClient()
  const [configIface,    setConfigIface]    = useState<Iface | null>(null)
  const [pendingConfirm, setPendingConfirm] = useState(false)

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn:  ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
    refetchInterval: 15_000,
  })

  const confirm = useMutation({
    mutationFn: () => api.post('/api/network/confirm', {}),
    onSuccess: () => { toast.success('Network configuration confirmed'); setPendingConfirm(false) },
    onError: (e: Error) => toast.error(e.message),
  })

  function handleConfigured() {
    setPendingConfirm(true)
    qc.invalidateQueries({ queryKey: ['network', 'info'] })
  }

  const ifaces = netQ.data?.interfaces ?? []

  if (netQ.isLoading) return <Skeleton height={300} />
  if (netQ.isError)   return <ErrorState error={netQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['network', 'info'] })} />

  return (
    <>
      {pendingConfirm && (
        <ConfirmBanner
          onConfirm={() => confirm.mutate()}
          onDismiss={() => setPendingConfirm(false)}
        />
      )}

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
                  <span style={{ width: 8, height: 8, borderRadius: '50%', background: isUp ? 'var(--success)' : 'var(--text-tertiary)', boxShadow: isUp ? '0 0 5px var(--success)' : 'none', display: 'inline-block' }} />
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 14, flexWrap: 'wrap' }}>
                  <span>{iface.ip ? `${iface.ip}${iface.netmask ? ' / ' + iface.netmask : ''}` : 'No IP'}</span>
                  {iface.gateway && <span>GW: {iface.gateway}</span>}
                  {iface.mac     && <span style={{ fontFamily: 'var(--font-mono)' }}>{iface.mac}</span>}
                  {iface.speed   && <span>{iface.speed}</span>}
                  {iface.mtu     && <span>MTU {iface.mtu}</span>}
                </div>
              </div>

              <button onClick={() => setConfigIface(iface)} className="btn btn-ghost">
                <Icon name="settings" size={14} />Configure
              </button>
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
          onDone={handleConfigured}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// VLANsTab
// POST /api/network/vlan { name, parent, id }
// GET  /api/network/vlan → { success, vlans: string }  (raw text from daemon)
// ---------------------------------------------------------------------------

function VLANsTab() {
  const qc = useQueryClient()
  const [name,   setName]   = useState('')
  const [parent, setParent] = useState('')
  const [vlanId, setVlanId] = useState('')

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn: ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
  })

  const vlanQ = useQuery({
    queryKey: ['network', 'vlan'],
    queryFn: ({ signal }) => api.get<{ success: boolean; vlans: string }>('/api/network/vlan', signal),
  })

  const create = useMutation({
    mutationFn: () => {
      if (!name.trim() || !parent.trim() || !vlanId) throw new Error('Name, parent interface and VLAN ID are required')
      return api.post('/api/network/vlan', { name: name.trim(), parent: parent.trim(), id: Number(vlanId) })
    },
    onSuccess: () => {
      toast.success('VLAN created')
      setName(''); setVlanId('')
      qc.invalidateQueries({ queryKey: ['network', 'vlan'] })
      qc.invalidateQueries({ queryKey: ['network', 'info'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // Physical interfaces only (no existing VLANs as parent)
  const physIfaces = (netQ.data?.interfaces ?? []).filter(i => !i.name.includes('.'))

  return (
    <>
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginBottom: 24 }}>
        <div style={{ fontWeight: 700, marginBottom: 16 }}>Create VLAN Interface</div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 100px', gap: 12, marginBottom: 12 }}>
          <label className="field">
            <span className="field-label">Interface Name</span>
            <input value={name} onChange={e => setName(e.target.value)} placeholder="vlan10" className="input" />
          </label>
          <label className="field">
            <span className="field-label">Parent Interface</span>
            <select value={parent} onChange={e => setParent(e.target.value)} className="input" style={{ appearance: 'none' }}>
              <option value="">Select…</option>
              {physIfaces.map(i => <option key={i.name} value={i.name}>{i.name}</option>)}
            </select>
          </label>
          <label className="field">
            <span className="field-label">VLAN ID</span>
            <input type="number" value={vlanId} onChange={e => setVlanId(e.target.value)} placeholder="10" min={1} max={4094} className="input" />
          </label>
        </div>
        <button onClick={() => create.mutate()} disabled={create.isPending || !name || !parent || !vlanId} className="btn btn-primary">
          <Icon name="add" size={15} />{create.isPending ? 'Creating…' : 'Create VLAN'}
        </button>
      </div>

      <div style={{ fontWeight: 700, marginBottom: 12 }}>Current VLANs</div>
      {vlanQ.isLoading && <Skeleton height={80} />}
      {vlanQ.isError   && <ErrorState error={vlanQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['network', 'vlan'] })} />}
      {vlanQ.data && (
        <pre className="card" style={{ background: 'var(--surface)', borderRadius: 'var(--radius-lg)', padding: '14px 18px', fontFamily: 'var(--font-mono)', fontSize: 12, lineHeight: 1.8, color: 'rgba(255,255,255,0.65)', whiteSpace: 'pre-wrap', margin: 0, maxHeight: 300, overflow: 'auto' }}>
          {vlanQ.data.vlans || 'No VLANs configured'}
        </pre>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// BondingTab
// POST /api/network/bond { name, mode, slaves: string[] }
// ---------------------------------------------------------------------------

function BondingTab() {
  const qc = useQueryClient()
  const [bondName,   setBondName]   = useState('bond0')
  const [mode,       setMode]       = useState('active-backup')
  const [slavesStr,  setSlavesStr]  = useState('')

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn: ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
  })

  const create = useMutation({
    mutationFn: () => {
      const slaves = slavesStr.split(',').map(s => s.trim()).filter(Boolean)
      if (!bondName.trim()) throw new Error('Bond name is required')
      if (slaves.length < 2) throw new Error('At least 2 slave interfaces are required')
      return api.post('/api/network/bond', { name: bondName.trim(), mode, slaves })
    },
    onSuccess: () => {
      toast.success('Bond interface created')
      setSlavesStr('')
      qc.invalidateQueries({ queryKey: ['network', 'info'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const BOND_MODES = [
    { value: 'active-backup', label: 'Active-Backup  (failover, 1 active at a time)' },
    { value: 'balance-rr',    label: 'Round-Robin  (balance-rr)' },
    { value: '802.3ad',       label: 'LACP / 802.3ad  (requires switch support)' },
    { value: 'balance-tlb',   label: 'Adaptive TX Load Balancing  (balance-tlb)' },
    { value: 'balance-alb',   label: 'Adaptive Load Balancing  (balance-alb)' },
  ]

  const existingBonds = (netQ.data?.interfaces ?? []).filter(i =>
    i.type === 'bond' || i.name.startsWith('bond')
  )

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
          <label className="field">
            <span className="field-label">Slave Interfaces</span>
            <input value={slavesStr} onChange={e => setSlavesStr(e.target.value)} placeholder="eth0, eth1" className="input" />
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Comma-separated. Minimum 2 interfaces required.</span>
          </label>
          <div>
            <button onClick={() => create.mutate()} disabled={create.isPending} className="btn btn-primary">
              <Icon name="cable" size={15} />{create.isPending ? 'Creating…' : 'Create Bond'}
            </button>
          </div>
        </div>
      </div>

      {existingBonds.length > 0 && (
        <>
          <div style={{ fontWeight: 700, marginBottom: 12 }}>Active Bond Interfaces</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {existingBonds.map(b => (
              <div key={b.name} className="card" style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', borderRadius: 'var(--radius-md)' }}>
                <Icon name="device_hub" size={20} style={{ color: 'var(--primary)', flexShrink: 0 }} />
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 700 }}>{b.name}</div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {b.ip || 'No IP'}{b.speed ? ` · ${b.speed}` : ''}
                    {b.mtu ? ` · MTU ${b.mtu}` : ''}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// DnsNtpTab
// POST /api/network/apply { action:'set_dns', dns:[] }
// GET  /api/system/ntp → { servers[], synced, details }
// POST /api/system/ntp { servers[] }
// ---------------------------------------------------------------------------

function DnsNtpTab() {
  const qc = useQueryClient()

  const netQ = useQuery({
    queryKey: ['network', 'info'],
    queryFn: ({ signal }) => api.get<NetworkResponse>('/api/system/network', signal),
  })
  const ntpQ = useQuery({
    queryKey: ['system', 'ntp'],
    queryFn: ({ signal }) => api.get<NTPResponse>('/api/system/ntp', signal),
    refetchInterval: 30_000,
  })

  const [dnsStr,    setDnsStr]    = useState('')
  const [ntpStr,    setNtpStr]    = useState('')
  const [dnsSeeded, setDnsSeeded] = useState(false)
  const [ntpSeeded, setNtpSeeded] = useState(false)

  useEffect(() => {
    if (netQ.data?.dns && !dnsSeeded) {
      setDnsStr(netQ.data.dns.join(', '))
      setDnsSeeded(true)
    }
  }, [netQ.data, dnsSeeded])

  useEffect(() => {
    if (ntpQ.data?.servers && !ntpSeeded) {
      setNtpStr(ntpQ.data.servers.join(', '))
      setNtpSeeded(true)
    }
  }, [ntpQ.data, ntpSeeded])

  const saveDns = useMutation({
    mutationFn: () => {
      const dns = dnsStr.split(',').map(s => s.trim()).filter(Boolean)
      return api.post('/api/network/apply', { action: 'set_dns', dns })
    },
    onSuccess: () => { toast.success('DNS servers saved'); qc.invalidateQueries({ queryKey: ['network', 'info'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const saveNtp = useMutation({
    mutationFn: () => {
      const servers = ntpStr.split(',').map(s => s.trim()).filter(Boolean)
      if (servers.length === 0) throw new Error('At least one NTP server is required')
      return api.post('/api/system/ntp', { servers })
    },
    onSuccess: () => { toast.success('NTP servers saved'); qc.invalidateQueries({ queryKey: ['system', 'ntp'] }) },
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
          <>
            <div style={{ display: 'flex', gap: 10 }}>
              <input value={dnsStr} onChange={e => setDnsStr(e.target.value)}
                placeholder="8.8.8.8, 8.8.4.4, 1.1.1.1"
                className="input" style={{ flex: 1, fontFamily: 'var(--font-mono)' }} />
              <button onClick={() => saveDns.mutate()} disabled={saveDns.isPending} className="btn btn-primary">
                {saveDns.isPending ? 'Saving…' : 'Save'}
              </button>
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 7 }}>
              Comma-separated list of nameserver addresses
            </div>
          </>
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
              <input value={ntpStr} onChange={e => setNtpStr(e.target.value)}
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
// NetworkPage
// ---------------------------------------------------------------------------

type Tab = 'interfaces' | 'vlans' | 'bonding' | 'dns'

export function NetworkPage() {
  const [tab, setTab] = useState<Tab>('interfaces')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'interfaces', label: 'Interfaces', icon: 'lan' },
    { id: 'vlans',      label: 'VLANs',      icon: 'account_tree' },
    { id: 'bonding',    label: 'Bonding',     icon: 'device_hub' },
    { id: 'dns',        label: 'DNS & NTP',   icon: 'dns' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header">
        <h1 className="page-title">Network</h1>
        <p className="page-subtitle">Interfaces, VLANs, bonding, DNS and NTP</p>
      </div>

      <div className="tabs-underline">
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'interfaces' && <InterfacesTab />}
      {tab === 'vlans'      && <VLANsTab />}
      {tab === 'bonding'    && <BondingTab />}
      {tab === 'dns'        && <DnsNtpTab />}
    </div>
  )
}
