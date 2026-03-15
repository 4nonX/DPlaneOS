/**
 * components/layout/TopBar.tsx
 *
 * Fixed top bar with page icon, title, breadcrumb group, and user chip.
 */

import { useRouterState } from '@tanstack/react-router'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Icon } from '@/components/ui/Icon'
import { Tooltip } from '@/components/ui/Tooltip'
import { useAuthStore } from '@/stores/auth'
import { useStorageStore } from '@/stores/storage'
import { findNavEntry } from './navConfig'
import { api } from '@/lib/api'

interface ZFSPool {
  name: string
  size: string
  alloc: string
  free: string
  capacity: string
  health: string
}
interface PoolsResponse { success: boolean; pools?: ZFSPool[]; data?: ZFSPool[] }

interface TopBarProps {
  sidebarCollapsed: boolean
}

export function TopBar({ sidebarCollapsed }: TopBarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const user     = useAuthStore((s) => s.user)

  const entry     = findNavEntry(pathname)
  const pageTitle = entry?.leaf.label ?? (pathname === '/' ? 'Dashboard' : pathname.slice(1))
  const pageIcon  = entry?.leaf.icon  ?? 'home'
  const groupLabel = entry?.groupLabel

  return (
    <header
      style={{
        position: 'fixed', top: 0,
        left: sidebarCollapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)',
        right: 0, height: 'var(--topbar-height)',
        background: 'hsla(var(--hue-bg), 18%, 4%, 0.7)',
        borderBottom: '1px solid var(--border-subtle)',
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '0 32px',
        zIndex: 40,
        backdropFilter: 'var(--blur-glass)',
        boxShadow: '0 4px 30px rgba(0, 0, 0, 0.4)',
        transition: 'left var(--transition-bounce)'}}
    >
      {/* ── Left: breadcrumb + title ── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <div style={{
          width: 30, height: 30, borderRadius: 8,
          background: 'var(--primary-bg)',
          border: '1px solid rgba(138,156,255,0.2)',
          display: 'flex', alignItems: 'center', justifyContent: 'center'}}>
          <Icon name={pageIcon} size={16} style={{ color: 'var(--primary)' }} />
        </div>

        <div>
          {groupLabel && (
            <div style={{
              fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)',
              fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.6px',
              lineHeight: 1, marginBottom: 3}}>
              {groupLabel}
            </div>
          )}
          <h1 style={{
            fontSize: 'var(--text-md)', fontWeight: 700,
            letterSpacing: '-0.3px', color: 'var(--text)', lineHeight: 1}}>
            {pageTitle}
          </h1>
        </div>
      </div>

      {/* ── Center: Pool Capacity Bar ── */}
      <PoolMonitor />

      {/* ── Right: user chip ── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
        {api.isMockActive() && (
          <div style={{
            background: 'linear-gradient(90deg, #ff4b2b, #ff416c)',
            padding: '4px 12px',
            borderRadius: 6,
            fontSize: 10,
            fontWeight: 800,
            color: '#fff',
            letterSpacing: '0.5px',
            textTransform: 'uppercase',
            boxShadow: '0 4px 12px rgba(255, 75, 43, 0.3)',
            animation: 'pulse 2s infinite'
          }}>
            Preview Mode — Mock Data
          </div>
        )}

        {user && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 10,
            padding: '6px 16px 6px 8px',
            background: 'hsla(0,0%,100%,0.03)',
            border: '1px solid var(--border-subtle)',
            borderRadius: 'var(--radius-full)',
            cursor: 'pointer',
            transition: 'all var(--transition-fast)',
            boxShadow: 'inset 0 1px 0 hsla(0,0%,100%,0.02)'}}
          onMouseEnter={(e) => {
            e.currentTarget.style.background = 'hsla(0,0%,100%,0.08)'
            e.currentTarget.style.borderColor = 'var(--border)'
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.background = 'hsla(0,0%,100%,0.03)'
            e.currentTarget.style.borderColor = 'var(--border-subtle)'
          }}>
            <div style={{
              width: 26, height: 26, borderRadius: '50%',
              background: 'linear-gradient(135deg, var(--primary) 0%, hsl(260, 100%, 75%) 100%)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              fontSize: 12, fontWeight: 800, color: '#000', flexShrink: 0,
              boxShadow: '0 2px 8px var(--primary-glow)'}}>
              {user.username[0].toUpperCase()}
            </div>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text)', fontWeight: 600 }}>
              {user.username}
            </span>
            {user.role && user.role !== 'user' && (
              <span className="badge badge-primary" style={{ fontSize: 9 }}>
                {user.role}
              </span>
            )}
          </div>
        )}
      </div>
    </header>
  )
}

// ---------------------------------------------------------------------------
// PoolMonitor
// ---------------------------------------------------------------------------

function PoolMonitor() {
  const { activePool, setActivePool } = useStorageStore()
  const [showDropdown, setShowDropdown] = useState(false)

  const poolsQ = useQuery({
    queryKey: ['zfs', 'pools'],
    queryFn: ({ signal }) => api.get<PoolsResponse>('/api/zfs/pools', signal),
    refetchInterval: 30_000,
  })

  const pools = poolsQ.data?.pools ?? poolsQ.data?.data ?? []
  
  // Auto-select first pool if none selected
  if (!activePool && pools.length > 0) {
    setActivePool(pools[0].name)
  }

  const selected = pools.find(p => p.name === activePool) ?? pools[0]
  if (!selected) return <div style={{ flex: 1 }} />

  const pct = parseInt((selected.capacity || '0').replace('%', '')) || 0
  const color = pct >= 90 ? 'var(--error)' : pct >= 75 ? 'var(--warning)' : 'var(--primary)'

  return (
    <div style={{ 
      flex: 1, 
      maxWidth: 400, 
      margin: '0 40px',
      display: 'flex', 
      flexDirection: 'column', 
      gap: 5,
      position: 'relative'
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div 
          onClick={() => setShowDropdown(!showDropdown)}
          style={{ 
            display: 'flex', 
            alignItems: 'center', 
            gap: 4, 
            cursor: 'pointer',
            fontSize: 'var(--text-2xs)',
            fontWeight: 700,
            color: 'var(--text-secondary)',
            textTransform: 'uppercase',
            whiteSpace: 'nowrap',
            flexShrink: 0
          }}
        >
          <Icon name="storage" size={12} style={{ color }} />
          {selected.name}
          <Icon name="arrow_drop_down" size={14} />
        </div>
        
        <Tooltip content={`${selected.alloc} used / ${selected.size}`}>
          <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', fontWeight: 600 }}>
            {selected.capacity}
          </div>
        </Tooltip>
      </div>

      <div style={{ 
        height: 6, 
        background: 'rgba(255,255,255,0.05)', 
        borderRadius: 999, 
        overflow: 'hidden',
        border: '1px solid rgba(255,255,255,0.03)'
      }}>
        <div style={{ 
          height: '100%', 
          width: `${pct}%`, 
          background: color, 
          borderRadius: 999, 
          transition: 'width 0.8s cubic-bezier(0.34, 1.56, 0.64, 1)',
          boxShadow: pct > 80 ? `0 0 8px ${color}80` : 'none'
        }} />
      </div>

      {showDropdown && (
        <div style={{
          position: 'absolute',
          top: '100%',
          left: 0,
          marginTop: 8,
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-md)',
          boxShadow: 'var(--shadow-lg)',
          zIndex: 100,
          minWidth: 160,
          overflow: 'hidden',
          backdropFilter: 'var(--blur-glass)'
        }}>
          {pools.map(p => (
            <div 
              key={p.name}
              onClick={() => { setActivePool(p.name); setShowDropdown(false) }}
              style={{
                padding: '10px 14px',
                cursor: 'pointer',
                fontSize: 'var(--text-xs)',
                fontWeight: 600,
                color: activePool === p.name ? 'var(--primary)' : 'var(--text-secondary)',
                background: activePool === p.name ? 'rgba(255,255,255,0.03)' : 'transparent',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                transition: 'all 0.15s'
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.05)' }}
              onMouseLeave={e => { if (activePool !== p.name) e.currentTarget.style.background = 'transparent' }}
            >
              {p.name}
              {activePool === p.name && <Icon name="check" size={14} />}
            </div>
          ))}
        </div>
      )}
      
      {showDropdown && (
        <div 
          onClick={() => setShowDropdown(false)}
          style={{ position: 'fixed', inset: 0, zIndex: 99 }} 
        />
      )}
    </div>
  )
}
