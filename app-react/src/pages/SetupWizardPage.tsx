/**
 * pages/SetupWizardPage.tsx — First-run Setup Wizard (Phase 9)
 *
 * Shown at /setup — root route, no auth required.
 * Guarded by daemon: setup_complete=0 in system_config.
 *
 * Steps:
 *   0  Welcome
 *   1  Admin Account  → POST /api/system/setup-admin { username, password }
 *   2  Disk Selection → GET  /api/system/disks
 *   3  Pool Config    → POST /api/system/pool/create { name, type, disks }
 *   4  Hostname / TZ  → (collected, sent with setup-complete)
 *   5  Complete       → POST /api/system/setup-complete { hostname, timezone }
 *                       → redirect to /login
 */

import { useState, useCallback } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface DiskInfo {
  name:        string
  size:        string
  type:        string    // 'SSD' | 'HDD' | 'NVMe' | ...
  model:       string
  serial:      string
  in_use:      boolean
  mount_point?: string
}

interface PoolSuggestion {
  name:        string
  type:        string    // 'Single' | 'Mirror' | 'RAID-Z1' | 'RAID-Z2' | 'RAID-Z3'
  disks:       string[]
  total_size:  string
  usable_size: string
  redundancy:  string
}

interface DisksResponse {
  disks:       DiskInfo[]
  suggestions: PoolSuggestion[]
}

// ---------------------------------------------------------------------------
// Step indicator
// ---------------------------------------------------------------------------

const STEPS = ['Welcome', 'Admin', 'Disks', 'Pool', 'System', 'Done']

function StepBar({ current }: { current: number }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 0, marginBottom: 48 }}>
      {STEPS.map((label, i) => {
        const done    = i < current
        const active  = i === current
        const isLast  = i === STEPS.length - 1
        return (
          <div key={label} style={{ display: 'flex', alignItems: 'center' }}>
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }}>
              <div style={{
                width: 32, height: 32, borderRadius: '50%',
                background: done ? 'var(--primary)' : active ? 'var(--primary-bg)' : 'var(--surface)',
                border: `2px solid ${done || active ? 'var(--primary)' : 'var(--border)'}`,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                fontSize: 12, fontWeight: 700,
                color: done ? '#000' : active ? 'var(--primary)' : 'var(--text-tertiary)',
                transition: 'all 0.2s',
              }}>
                {done ? <Icon name="check" size={16} /> : i + 1}
              </div>
              <span style={{
                fontSize: 11, fontWeight: active ? 700 : 500,
                color: active ? 'var(--primary)' : done ? 'var(--text-secondary)' : 'var(--text-tertiary)',
              }}>
                {label}
              </span>
            </div>
            {!isLast && (
              <div style={{
                width: 48, height: 2, marginBottom: 22,
                background: done ? 'var(--primary)' : 'var(--border)',
                transition: 'background 0.3s',
              }} />
            )}
          </div>
        )
      })}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 0 — Welcome
// ---------------------------------------------------------------------------

function StepWelcome({ onNext }: { onNext: () => void }) {
  return (
    <div style={{ textAlign: 'center' }}>
      <div style={{
        width: 80, height: 80, borderRadius: '50%',
        background: 'var(--primary-bg)', border: '2px solid rgba(138,156,255,0.3)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        margin: '0 auto 24px',
      }}>
        <Icon name="storage" size={40} style={{ color: 'var(--primary)' }} />
      </div>
      <h2 style={{ fontSize: 'var(--text-2xl)', fontWeight: 800, marginBottom: 12 }}>
        Welcome to D-PlaneOS
      </h2>
      <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)', maxWidth: 480, margin: '0 auto 40px', lineHeight: 1.7 }}>
        This wizard will guide you through initial setup. You'll create an admin account,
        configure your storage pool, and set basic system options.
      </p>
      <button onClick={onNext} className="btn btn-primary" style={{ fontSize: 'var(--text-md)', padding: '14px 36px' }}>
        Get Started <Icon name="arrow_forward" size={18} />
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 1 — Admin Account
// ---------------------------------------------------------------------------

function StepAdmin({ onNext }: { onNext: () => void }) {
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirm,  setConfirm]  = useState('')
  const [error,    setError]    = useState('')
  const [showPass, setShowPass] = useState(false)

  const save = useMutation({
    mutationFn: () => api.post('/api/system/setup-admin', { username, password }),
    onSuccess: () => { setError(''); onNext() },
    onError: (e: Error) => setError(e.message),
  })

  function submit() {
    setError('')
    if (!username.trim()) { setError('Username is required'); return }
    if (password.length < 8) { setError('Password must be at least 8 characters'); return }
    if (password !== confirm) { setError('Passwords do not match'); return }
    save.mutate()
  }

  return (
    <div>
      <h2 style={{ fontSize: 'var(--text-xl)', fontWeight: 700, marginBottom: 8 }}>Admin Account</h2>
      <p style={{ color: 'var(--text-secondary)', marginBottom: 28, fontSize: 'var(--text-sm)' }}>
        Create the administrator account for this NAS.
      </p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 400 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>Username</span>
          <input
            value={username}
            onChange={e => setUsername(e.target.value)}
            placeholder="admin"
            className="input"
            autoComplete="username"
          />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>Password</span>
          <div style={{ position: 'relative' }}>
            <input
              type={showPass ? 'text' : 'password'}
              value={password}
              onChange={e => setPassword(e.target.value)}
              placeholder="Min. 8 characters"
              className="input"
              style={{ paddingRight: 40 }}
              autoComplete="new-password"
            />
            <button
              type="button"
              onClick={() => setShowPass(v => !v)}
              style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex' }}
            >
              <Icon name={showPass ? 'visibility_off' : 'visibility'} size={18} />
            </button>
          </div>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>Confirm Password</span>
          <input
            type={showPass ? 'text' : 'password'}
            value={confirm}
            onChange={e => setConfirm(e.target.value)}
            placeholder="Repeat password"
            className="input"
            style={{ borderColor: confirm && confirm !== password ? 'var(--error)' : '' }}
            autoComplete="new-password"
            onKeyDown={e => e.key === 'Enter' && submit()}
          />
        </label>

        {/* Password strength hint */}
        {password.length > 0 && (
          <div style={{ display: 'flex', gap: 4 }}>
            {[8, 12, 16].map(threshold => (
              <div key={threshold} style={{
                flex: 1, height: 3, borderRadius: 2,
                background: password.length >= threshold
                  ? password.length >= 16 ? 'var(--success)' : password.length >= 12 ? 'rgba(251,191,36,0.8)' : 'var(--primary)'
                  : 'var(--border)',
                transition: 'background 0.2s',
              }} />
            ))}
            <span style={{ fontSize: 10, color: 'var(--text-tertiary)', marginLeft: 6 }}>
              {password.length >= 16 ? 'Strong' : password.length >= 12 ? 'Good' : password.length >= 8 ? 'Weak' : ''}
            </span>
          </div>
        )}

        {error && (
          <div style={{ padding: '10px 14px', background: 'var(--error-bg)', border: '1px solid var(--error-border)', borderRadius: 'var(--radius-sm)', color: 'var(--error)', fontSize: 'var(--text-sm)' }}>
            {error}
          </div>
        )}

        <button onClick={submit} disabled={save.isPending} className="btn btn-primary" style={{ marginTop: 8 }}>
          {save.isPending ? 'Creating…' : 'Continue'} <Icon name="arrow_forward" size={16} />
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 2 — Disk Selection
// ---------------------------------------------------------------------------

function StepDisks({
  onNext,
  onSkip,
  setSelectedDisks,
  selectedDisks,
}: {
  onNext:           () => void
  onSkip:           () => void
  selectedDisks:    Set<string>
  setSelectedDisks: (s: Set<string>) => void
}) {
  const disksQ = useQuery({
    queryKey: ['setup', 'disks'],
    queryFn:  ({ signal }) => api.get<DisksResponse>('/api/system/disks', signal),
  })

  const disks       = disksQ.data?.disks       ?? []
  const suggestions = disksQ.data?.suggestions ?? []

  function toggleDisk(name: string) {
    const next = new Set(selectedDisks)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    setSelectedDisks(next)
  }

  function applySuggestion(s: PoolSuggestion) {
    setSelectedDisks(new Set(s.disks))
  }

  return (
    <div>
      <h2 style={{ fontSize: 'var(--text-xl)', fontWeight: 700, marginBottom: 8 }}>Select Disks</h2>
      <p style={{ color: 'var(--text-secondary)', marginBottom: 24, fontSize: 'var(--text-sm)' }}>
        Choose the disks to include in your storage pool. In-use disks are shown but cannot be selected.
      </p>

      {disksQ.isLoading && <Skeleton height={200} />}

      {disksQ.isError && (
        <div style={{ padding: '16px 20px', background: 'var(--error-bg)', border: '1px solid var(--error-border)', borderRadius: 'var(--radius-md)', marginBottom: 20, color: 'var(--error)' }}>
          Could not load disk list. You can skip this step and create a pool later.
        </div>
      )}

      {/* Suggestions */}
      {suggestions.length > 0 && (
        <div style={{ marginBottom: 20 }}>
          <div style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 8 }}>
            Suggested Configurations
          </div>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {suggestions.map((s, i) => (
              <button
                key={i}
                onClick={() => applySuggestion(s)}
                style={{
                  padding: '8px 14px', background: 'var(--surface)', border: '1px solid var(--border)',
                  borderRadius: 'var(--radius-sm)', cursor: 'pointer',
                  fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)',
                  display: 'flex', alignItems: 'center', gap: 6,
                }}
              >
                <Icon name="auto_fix_high" size={13} style={{ color: 'var(--primary)' }} />
                {s.type} — {s.usable_size} usable
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Disk list */}
      {!disksQ.isLoading && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 24 }}>
          {disks.length === 0 && (
            <div style={{ padding: '32px', textAlign: 'center', color: 'var(--text-tertiary)', border: '1px dashed var(--border)', borderRadius: 'var(--radius-md)' }}>
              No disks found
            </div>
          )}
          {disks.map(disk => {
            const isSelected = selectedDisks.has(disk.name)
            const disabled   = disk.in_use
            return (
              <div
                key={disk.name}
                onClick={() => !disabled && toggleDisk(disk.name)}
                style={{
                  display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px',
                  background: isSelected ? 'var(--primary-bg)' : 'var(--bg-card)',
                  border: `1px solid ${isSelected ? 'var(--primary)' : disabled ? 'var(--border)' : 'var(--border)'}`,
                  borderRadius: 'var(--radius-md)',
                  cursor: disabled ? 'not-allowed' : 'pointer',
                  opacity: disabled ? 0.5 : 1,
                  transition: 'all 0.15s',
                }}
              >
                {/* Checkbox */}
                <div style={{
                  width: 20, height: 20, borderRadius: 4, flexShrink: 0,
                  background: isSelected ? 'var(--primary)' : 'var(--surface)',
                  border: `2px solid ${isSelected ? 'var(--primary)' : 'var(--border)'}`,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                }}>
                  {isSelected && <Icon name="check" size={14} style={{ color: '#000' }} />}
                </div>

                <Icon name={disk.type === 'SSD' || disk.type === 'NVMe' ? 'memory' : 'hard_drive'} size={22}
                  style={{ color: isSelected ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0 }} />

                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>
                    /dev/{disk.name}
                    {disk.in_use && (
                      <span style={{ marginLeft: 8, fontSize: 10, fontFamily: 'var(--font-ui)', fontWeight: 700, padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'var(--error-bg)', color: 'var(--error)', border: '1px solid var(--error-border)' }}>
                        IN USE
                      </span>
                    )}
                  </div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
                    {disk.model || 'Unknown model'} · {disk.size} · {disk.type}
                    {disk.serial && ` · S/N: ${disk.serial}`}
                    {disk.mount_point && ` · mounted at ${disk.mount_point}`}
                  </div>
                </div>

                <div style={{
                  padding: '3px 9px', borderRadius: 'var(--radius-xs)', flexShrink: 0,
                  background: 'var(--surface)', border: '1px solid var(--border)',
                  fontSize: 11, fontWeight: 700, color: 'var(--text-secondary)',
                }}>
                  {disk.size}
                </div>
              </div>
            )
          })}
        </div>
      )}

      <div style={{ display: 'flex', gap: 10, justifyContent: 'space-between' }}>
        <button onClick={onSkip} className="btn btn-ghost">
          Skip (create pool later)
        </button>
        <button
          onClick={onNext}
          disabled={selectedDisks.size === 0}
          className="btn btn-primary"
          style={{ opacity: selectedDisks.size === 0 ? 0.4 : 1 }}
        >
          Continue ({selectedDisks.size} selected) <Icon name="arrow_forward" size={16} />
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 3 — Pool Configuration
// ---------------------------------------------------------------------------

const POOL_TYPES = [
  { value: 'Single',  label: 'Single / Stripe', desc: 'Maximum space, no redundancy. Data loss if any disk fails.',  minDisks: 1 },
  { value: 'Mirror',  label: 'Mirror',           desc: 'Full mirror between disks. Survives loss of all but one disk.', minDisks: 2 },
  { value: 'RAID-Z1', label: 'RAID-Z1',          desc: '1 parity disk. Survives 1 disk failure.',                    minDisks: 3 },
  { value: 'RAID-Z2', label: 'RAID-Z2',          desc: '2 parity disks. Survives 2 disk failures.',                  minDisks: 4 },
  { value: 'RAID-Z3', label: 'RAID-Z3',          desc: '3 parity disks. Survives 3 disk failures.',                  minDisks: 5 },
]

function StepPool({
  selectedDisks,
  onNext,
  onBack,
}: {
  selectedDisks: Set<string>
  onNext:        () => void
  onBack:        () => void
}) {
  const [poolName, setPoolName] = useState('tank')
  const [poolType, setPoolType] = useState('RAID-Z1')
  const [error,    setError]    = useState('')

  const diskCount  = selectedDisks.size
  const validTypes = POOL_TYPES.filter(t => diskCount >= t.minDisks)
  const activeType = validTypes.find(t => t.value === poolType) ?? validTypes[validTypes.length - 1]

  const create = useMutation({
    mutationFn: () => api.post('/api/system/pool/create', {
      name:  poolName,
      type:  poolType,
      disks: [...selectedDisks],
    }),
    onSuccess: () => { setError(''); onNext() },
    onError:   (e: Error) => setError(e.message),
  })

  function submit() {
    setError('')
    if (!poolName.trim())       { setError('Pool name is required'); return }
    if (!/^[a-z][a-z0-9_-]*$/.test(poolName)) {
      setError('Pool name must start with a letter and contain only lowercase letters, numbers, _ or -')
      return
    }
    if (!activeType)             { setError('No valid pool type for selected disk count'); return }
    create.mutate()
  }

  return (
    <div>
      <h2 style={{ fontSize: 'var(--text-xl)', fontWeight: 700, marginBottom: 8 }}>Configure Pool</h2>
      <p style={{ color: 'var(--text-secondary)', marginBottom: 24, fontSize: 'var(--text-sm)' }}>
        {diskCount} disk{diskCount !== 1 ? 's' : ''} selected. Choose your pool topology.
      </p>

      {/* Selected disks summary */}
      <div style={{ padding: '12px 16px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', marginBottom: 20, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        {[...selectedDisks].map(d => (
          <span key={d} style={{ padding: '2px 8px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xs)', fontFamily: 'var(--font-mono)', fontSize: 12 }}>
            /dev/{d}
          </span>
        ))}
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 540 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>Pool Name</span>
          <input
            value={poolName}
            onChange={e => setPoolName(e.target.value.toLowerCase())}
            placeholder="tank"
            className="input"
            style={{ fontFamily: 'var(--font-mono)' }}
          />
        </label>

        {/* RAID type picker */}
        <div>
          <div style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: 8 }}>
            Pool Type
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {POOL_TYPES.map(t => {
              const canUse   = diskCount >= t.minDisks
              const selected = poolType === t.value
              return (
                <div
                  key={t.value}
                  onClick={() => canUse && setPoolType(t.value)}
                  style={{
                    display: 'flex', alignItems: 'flex-start', gap: 12, padding: '12px 16px',
                    background: selected ? 'var(--primary-bg)' : 'var(--bg-card)',
                    border: `1px solid ${selected ? 'var(--primary)' : 'var(--border)'}`,
                    borderRadius: 'var(--radius-md)',
                    cursor: canUse ? 'pointer' : 'not-allowed',
                    opacity: canUse ? 1 : 0.4,
                  }}
                >
                  <div style={{
                    width: 18, height: 18, borderRadius: '50%', flexShrink: 0, marginTop: 1,
                    background: selected ? 'var(--primary)' : 'var(--surface)',
                    border: `2px solid ${selected ? 'var(--primary)' : 'var(--border)'}`,
                  }} />
                  <div>
                    <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)' }}>
                      {t.label}
                      <span style={{ marginLeft: 8, fontSize: 11, fontWeight: 500, color: 'var(--text-tertiary)' }}>
                        (min. {t.minDisks} disk{t.minDisks !== 1 ? 's' : ''})
                      </span>
                    </div>
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{t.desc}</div>
                  </div>
                </div>
              )
            })}
          </div>
        </div>

        {error && (
          <div style={{ padding: '10px 14px', background: 'var(--error-bg)', border: '1px solid var(--error-border)', borderRadius: 'var(--radius-sm)', color: 'var(--error)', fontSize: 'var(--text-sm)' }}>
            {error}
          </div>
        )}

        <div style={{ display: 'flex', gap: 10 }}>
          <button onClick={onBack} className="btn btn-ghost">
            <Icon name="arrow_back" size={16} /> Back
          </button>
          <button onClick={submit} disabled={create.isPending} className="btn btn-primary">
            {create.isPending ? 'Creating Pool…' : 'Create Pool'} <Icon name="arrow_forward" size={16} />
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 4 — Hostname & Timezone
// ---------------------------------------------------------------------------

const COMMON_TIMEZONES = [
  'UTC', 'Europe/Berlin', 'Europe/London', 'Europe/Paris', 'Europe/Zurich',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'Asia/Tokyo', 'Asia/Seoul', 'Asia/Shanghai', 'Asia/Singapore',
  'Australia/Sydney', 'Pacific/Auckland',
]

function StepSystem({
  hostname, setHostname,
  timezone, setTimezone,
  onNext, onBack,
}: {
  hostname:    string; setHostname: (v: string) => void
  timezone:    string; setTimezone: (v: string) => void
  onNext:      () => void
  onBack:      () => void
}) {
  function submit() {
    onNext()
  }

  return (
    <div>
      <h2 style={{ fontSize: 'var(--text-xl)', fontWeight: 700, marginBottom: 8 }}>System Settings</h2>
      <p style={{ color: 'var(--text-secondary)', marginBottom: 28, fontSize: 'var(--text-sm)' }}>
        Set your NAS hostname and timezone. These can be changed later in Settings.
      </p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 420 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>Hostname</span>
          <input
            value={hostname}
            onChange={e => setHostname(e.target.value)}
            placeholder="dplaneos"
            className="input"
            style={{ fontFamily: 'var(--font-mono)' }}
          />
          <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
            Lowercase letters, numbers, hyphens only.
          </span>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>Timezone</span>
          <input
            value={timezone}
            onChange={e => setTimezone(e.target.value)}
            list="tz-list"
            placeholder="UTC"
            className="input"
          />
          <datalist id="tz-list">
            {COMMON_TIMEZONES.map(tz => <option key={tz} value={tz} />)}
          </datalist>
          <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
            Region/City format, e.g. Europe/Berlin
          </span>
        </label>

        <div style={{ display: 'flex', gap: 10, marginTop: 8 }}>
          <button onClick={onBack} className="btn btn-ghost">
            <Icon name="arrow_back" size={16} /> Back
          </button>
          <button onClick={submit} className="btn btn-primary">
            Finish Setup <Icon name="arrow_forward" size={16} />
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 5 — Complete
// ---------------------------------------------------------------------------

function StepComplete({ hostname, onGoToLogin }: { hostname: string; onGoToLogin: () => void }) {
  return (
    <div style={{ textAlign: 'center' }}>
      <div style={{
        width: 80, height: 80, borderRadius: '50%',
        background: 'rgba(16,185,129,0.1)', border: '2px solid rgba(16,185,129,0.4)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        margin: '0 auto 24px',
      }}>
        <Icon name="check_circle" size={44} style={{ color: 'var(--success)' }} />
      </div>
      <h2 style={{ fontSize: 'var(--text-2xl)', fontWeight: 800, marginBottom: 12 }}>
        Setup Complete!
      </h2>
      <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)', maxWidth: 440, margin: '0 auto 12px', lineHeight: 1.7 }}>
        {hostname ? (
          <><strong style={{ color: 'var(--text)' }}>{hostname}</strong> is ready.</>
        ) : 'Your NAS is ready.'}
        {' '}Sign in with the admin credentials you just created.
      </p>
      <p style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', marginBottom: 36 }}>
        You can configure shares, users, and more from the dashboard.
      </p>
      <button onClick={onGoToLogin} className="btn btn-primary" style={{ fontSize: 'var(--text-md)', padding: '14px 36px' }}>
        Go to Login <Icon name="login" size={18} />
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// SetupWizardPage — orchestrator
// ---------------------------------------------------------------------------

export function SetupWizardPage() {
  const navigate = useNavigate()

  const [step,          setStep]          = useState(0)
  const [selectedDisks, setSelectedDisks] = useState<Set<string>>(new Set())
  const [hostname,      setHostname]      = useState('dplaneos')
  const [timezone,      setTimezone]      = useState('UTC')
  const [completing,    setCompleting]    = useState(false)
  const [completeError, setCompleteError] = useState('')

  const next = useCallback(() => setStep(s => s + 1), [])
  const back = useCallback(() => setStep(s => s - 1), [])

  // Called at end of Step 4 — sends setup-complete
  async function finish() {
    setCompleting(true)
    setCompleteError('')
    try {
      await api.post('/api/system/setup-complete', {
        hostname: hostname.trim() || undefined,
        timezone: timezone.trim() || undefined,
      })
      setStep(5)
    } catch (e) {
      setCompleteError((e as Error).message)
    } finally {
      setCompleting(false)
    }
  }

  return (
    <div style={{
      minHeight: '100vh',
      background: 'var(--bg)',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
      padding: 24,
    }}>
      <div style={{ width: '100%', maxWidth: 680 }}>
        {/* Logo */}
        <div style={{ textAlign: 'center', marginBottom: 40 }}>
          <span style={{ fontSize: 13, fontWeight: 800, letterSpacing: '0.12em', textTransform: 'uppercase', color: 'var(--primary)' }}>
            D-PlaneOS
          </span>
        </div>

        <StepBar current={step} />

        {/* Step content card */}
        <div style={{
          background: 'var(--bg-card)', border: '1px solid var(--border)',
          borderRadius: 'var(--radius-xl)', padding: '40px 44px',
        }}>
          {step === 0 && <StepWelcome onNext={next} />}

          {step === 1 && <StepAdmin onNext={next} />}

          {step === 2 && (
            <StepDisks
              selectedDisks={selectedDisks}
              setSelectedDisks={setSelectedDisks}
              onNext={next}
              onSkip={() => setStep(4)}
            />
          )}

          {step === 3 && (
            <StepPool
              selectedDisks={selectedDisks}
              onNext={next}
              onBack={back}
            />
          )}

          {step === 4 && (
            <>
              <StepSystem
                hostname={hostname}  setHostname={setHostname}
                timezone={timezone}  setTimezone={setTimezone}
                onNext={finish}
                onBack={() => setStep(selectedDisks.size > 0 ? 3 : 2)}
              />
              {completing && (
                <div style={{ marginTop: 16, color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', textAlign: 'center' }}>
                  Finalising setup…
                </div>
              )}
              {completeError && (
                <div style={{ marginTop: 16, padding: '10px 14px', background: 'var(--error-bg)', border: '1px solid var(--error-border)', borderRadius: 'var(--radius-sm)', color: 'var(--error)', fontSize: 'var(--text-sm)' }}>
                  {completeError}
                </div>
              )}
            </>
          )}

          {step === 5 && (
            <StepComplete
              hostname={hostname}
              onGoToLogin={() => navigate({ to: '/login' })}
            />
          )}
        </div>

        {/* Footer */}
        <div style={{ textAlign: 'center', marginTop: 24, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          D-PlaneOS · First-run Setup
        </div>
      </div>
    </div>
  )
}
