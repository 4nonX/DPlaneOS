/**
 * components/layout/TopBar.tsx
 *
 * Fixed top bar with page icon, title, breadcrumb group, and user chip.
 */

import { useRouterState } from '@tanstack/react-router'
import { toggleTheme, getTheme } from '@/lib/theme'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Icon } from '@/components/ui/Icon'
import { Tooltip } from '@/components/ui/Tooltip'
import { useAuthStore } from '@/stores/auth'
import { useStorageStore } from '@/stores/storage'
import { findNavEntry } from './navConfig'
import { api } from '@/lib/api'
import { useNotificationsStore } from '@/stores/notifications'
import { JobIndicator } from '@/components/ui/JobIndicator'
import { MyAccessModal } from './MyAccessModal'

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
  onSearchOpen: () => void
  onHelpOpen?: () => void
  isMobile?: boolean
  mobileMenuOpen?: boolean
  onMobileMenuToggle?: () => void
}

export function TopBar({ sidebarCollapsed, onSearchOpen, onHelpOpen, isMobile, mobileMenuOpen, onMobileMenuToggle }: TopBarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const user     = useAuthStore((s) => s.user)
  const [showAccess, setShowAccess] = useState(false)

  const entry     = findNavEntry(pathname)
  const pageTitle = entry?.leaf.label ?? (pathname === '/' ? 'Dashboard' : pathname.slice(1))
  const pageIcon  = entry?.leaf.icon  ?? 'home'
  const groupLabel = entry?.groupLabel



  return (
    <header
      style={{
        position: 'fixed', top: 0,
        left: isMobile ? 0 : (sidebarCollapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)'),
        right: 0, height: 'var(--topbar-height)',
        background: 'hsla(var(--hue-bg), 18%, 4%, 0.7)',
        borderBottom: '1px solid var(--border-subtle)',
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: isMobile ? '0 16px' : '0 32px',
        zIndex: 'var(--z-topbar)',
        backdropFilter: 'var(--blur-glass)',
        boxShadow: '0 4px 30px rgba(0, 0, 0, 0.4)',
        transition: 'left var(--transition-bounce), padding var(--transition-fast)'}}
    >
      {/* ── Left: breadcrumb + title + pool monitor ── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 0 }}>
        {isMobile && onMobileMenuToggle && (
          <button
            onClick={onMobileMenuToggle}
            aria-label="Toggle navigation menu"
            aria-expanded={mobileMenuOpen}
            style={{
              width: 44, height: 44, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)',
              transition: 'color var(--transition-fast)',
              marginRight: 8,
            }}
            onMouseEnter={e => { e.currentTarget.style.color = 'var(--text)' }}
            onMouseLeave={e => { e.currentTarget.style.color = 'var(--text-secondary)' }}
          >
            <Icon name="menu" size={20} aria-hidden="true" />
          </button>
        )}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <div style={{
            width: 30, height: 30, borderRadius: 8,
            background: 'var(--primary-bg)',
            border: '1px solid var(--border)',
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

        {/* Separator */}
        <div style={{ width: 1, height: 28, background: 'var(--border)', margin: '0 20px', flexShrink: 0 }} />

        {/* Pool capacity bar — moved from center to left for conventional topbar layout */}
        <PoolMonitor />
      </div>

      {/* ── Right: search + user chip ── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
        {/* Search trigger */}
        {isMobile ? (
          <button
            onClick={onSearchOpen}
            aria-label="Open global search (Ctrl+K)"
            aria-keyshortcuts="Control+k Meta+k"
            style={{
              width: 44, height: 44, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: 'none', border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-md)', cursor: 'pointer', color: 'var(--text-tertiary)',
              transition: 'all var(--transition-fast)',
            }}
            onMouseEnter={e => { e.currentTarget.style.background = 'hsla(0,0%,100%,0.08)'; e.currentTarget.style.color = 'var(--text-secondary)' }}
            onMouseLeave={e => { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-tertiary)' }}
          >
            <Icon name="search" size={18} aria-hidden="true" />
          </button>
        ) : (
          <button
            onClick={onSearchOpen}
            aria-label="Open global search (Ctrl+K)"
            aria-keyshortcuts="Control+k Meta+k"
            style={{
              display: 'flex', alignItems: 'center', gap: 8,
              padding: '5px 10px 5px 8px',
              background: 'hsla(0,0%,100%,0.04)',
              border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-md)',
              cursor: 'pointer', color: 'var(--text-tertiary)',
              fontSize: 'var(--text-xs)', fontFamily: 'inherit',
              transition: 'all var(--transition-fast)',
            }}
            onMouseEnter={e => { e.currentTarget.style.background = 'hsla(0,0%,100%,0.08)'; e.currentTarget.style.color = 'var(--text-secondary)' }}
            onMouseLeave={e => { e.currentTarget.style.background = 'hsla(0,0%,100%,0.04)'; e.currentTarget.style.color = 'var(--text-tertiary)' }}
          >
            <Icon name="search" size={14} aria-hidden="true" />
            <span>Search</span>
            <kbd style={{ padding: '1px 5px', borderRadius: 3, fontSize: 10, background: 'var(--bg-card)', border: '1px solid var(--border)', fontFamily: 'var(--font-mono)' }} aria-hidden="true">
              {navigator.platform.startsWith('Mac') ? '⌘K' : 'Ctrl+K'}
            </kbd>
          </button>
        )}

        {onHelpOpen && (
          <Tooltip content="Keyboard shortcuts (?)">
            <button
              onClick={onHelpOpen}
              aria-label="Show keyboard shortcuts"
              aria-keyshortcuts="?"
              style={{
                width: 30, height: 30, display: 'flex', alignItems: 'center', justifyContent: 'center',
                background: 'none', border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-md)',
                cursor: 'pointer', color: 'var(--text-tertiary)',
                fontSize: 'var(--text-xs)', fontWeight: 700, fontFamily: 'inherit',
                transition: 'all var(--transition-fast)',
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'hsla(0,0%,100%,0.08)'; e.currentTarget.style.color = 'var(--text-secondary)' }}
              onMouseLeave={e => { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-tertiary)' }}
            >
              ?
            </button>
          </Tooltip>
        )}

        {api.isMockActive() && (
          <div style={{
            background: 'linear-gradient(90deg, var(--error) 0%, var(--warning) 100%)',
            padding: '4px 12px',
            borderRadius: 6,
            fontSize: 10,
            fontWeight: 800,
            color: 'var(--text)',
            letterSpacing: '0.5px',
            textTransform: 'uppercase',
            boxShadow: 'var(--error-glow)',
            animation: 'pulse 2s infinite'
          }}>
            Preview Mode - Mock Data
          </div>
        )}

        {user && (
          <Tooltip content={user.role && user.role !== 'user' ? `${user.username} · ${user.role}` : user.username}>
            <button type="button" onClick={() => setShowAccess(true)} aria-label={`Account ${user.username}: roles and permissions`} style={{
              width: 30, height: 30, borderRadius: '50%', border: 'none', padding: 0,
              background: 'linear-gradient(135deg, var(--primary) 0%, hsl(260, 55%, 62%) 100%)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              fontSize: 13, fontWeight: 700, color: 'var(--text-on-primary)',
              flexShrink: 0, userSelect: 'none', cursor: 'pointer',
              boxShadow: 'var(--shadow-sm)'}}>
              {user.username[0].toUpperCase()}
            </button>
          </Tooltip>
        )}
        {user && showAccess && <MyAccessModal username={user.username} onClose={() => setShowAccess(false)} />}

        <ThemeToggle />
        <JobIndicator />
        <NotificationsBell />
      </div>
    </header>
  )
}

// ---------------------------------------------------------------------------
// NotificationsBell
// ---------------------------------------------------------------------------

function NotificationsBell() {
  const [showFlyout, setShowFlyout] = useState(false)
  
  const notifications = useNotificationsStore(s => s.notifications) // Use the new notification history

  return (
    <div style={{ position: 'relative' }}>
      <button
        onClick={() => setShowFlyout(!showFlyout)}
        aria-label="Notifications"
        aria-expanded={showFlyout}
        style={{
          background: 'none',
          border: 'none',
          cursor: 'pointer',
          color: 'var(--text-tertiary)',
          display: 'flex',
          alignItems: 'center',
          padding: 8,
          borderRadius: 8,
          transition: 'all 0.2s',
          position: 'relative'
        }}
        onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.05)'; e.currentTarget.style.color = 'var(--text)' }}
        onMouseLeave={e => { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-tertiary)' }}
      >
        <Icon name="notifications" size={20} />
        {notifications.some(n => !n.read) && (
          <span style={{
            position: 'absolute',
            top: 6,
            right: 6,
            width: 8,
            height: 8,
            borderRadius: '50%',
            background: 'var(--primary)',
            border: '2px solid var(--surface)',
            boxShadow: 'none'
          }} />
        )}
      </button>

      {showFlyout && (
        <>
          <div style={{
            position: 'absolute',
            top: 'calc(100% + 12px)',
            right: 0,
            width: 320,
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-lg)',
            boxShadow: 'var(--shadow-xl)',
            zIndex: 'var(--z-modal)',
            overflow: 'hidden',
            backdropFilter: 'var(--blur-glass)'
          }}>
            <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border-subtle)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={{ fontWeight: 700, fontSize: 'var(--text-sm)' }}>Notifications</span>
            </div>

            <div style={{ maxHeight: 400, overflowY: 'auto' }}>
              {notifications.map((n) => (
                <div key={n.id} style={{ padding: 16, borderBottom: '1px solid var(--border-subtle)', opacity: n.read ? 0.6 : 1 }}>
                  <div style={{ display: 'flex', gap: 12 }}>
                    <Icon name={n.type === 'error' ? 'error' : n.type === 'warning' ? 'warning' : 'info'} size={18} style={{ color: `var(--${n.type})` }} />
                    <div style={{ flex: 1 }}>
                      <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', marginBottom: 2 }}>{n.title}</div>
                      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', margin: 0 }}>{n.message}</p>
                    </div>
                  </div>
                </div>
              ))}

              {notifications.length === 0 && (
                <div style={{ padding: '40px 20px', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
                  No notifications
                </div>
              )}
            </div>
          </div>
          <div 
            onClick={() => setShowFlyout(false)}
            style={{ position: 'fixed', inset: 0, zIndex: 'var(--z-dropdown)' }}
          />
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ThemeToggle
// ---------------------------------------------------------------------------

function ThemeToggle() {
  const [isDark, setIsDark] = useState(() => getTheme() === 'dark')

  function handleToggle() {
    toggleTheme()
    setIsDark(getTheme() === 'dark')
  }

  return (
    <button
      onClick={handleToggle}
      aria-label={isDark ? 'Switch to light theme' : 'Switch to dark theme'}
      aria-pressed={!isDark}
      style={{
        background: 'none', border: 'none', cursor: 'pointer',
        color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center',
        padding: 8, borderRadius: 8, transition: 'all 0.2s',
      }}
      onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.05)'; e.currentTarget.style.color = 'var(--text)' }}
      onMouseLeave={e => { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-tertiary)' }}
    >
      <Icon name={isDark ? 'light_mode' : 'dark_mode'} size={20} />
    </button>
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
      width: 220,
      flexShrink: 0,
      display: 'flex',
      flexDirection: 'column',
      gap: 5,
      position: 'relative'
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <button
          type="button"
          onClick={() => setShowDropdown(!showDropdown)}
          aria-expanded={showDropdown}
          aria-haspopup="listbox"
          aria-label={`Storage pool: ${selected.name}`}
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
            flexShrink: 0,
            background: 'none',
            border: 'none',
            padding: 0,
            fontFamily: 'inherit'
          }}
        >
          <Icon name="storage" size={12} style={{ color }} />
          {selected.name}
          <Icon name="arrow_drop_down" size={14} />
        </button>
        
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
          zIndex: 'var(--z-modal)',
          minWidth: 160,
          overflow: 'hidden',
          backdropFilter: 'var(--blur-glass)'
        }}>
          {pools.map(p => (
            <button
              key={p.name}
              type="button"
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
                transition: 'all 0.15s',
                width: '100%',
                border: 'none',
                textAlign: 'left',
                fontFamily: 'inherit'
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.05)' }}
              onMouseLeave={e => { if (activePool !== p.name) e.currentTarget.style.background = 'transparent' }}
            >
              {p.name}
              {activePool === p.name && <Icon name="check" size={14} />}
            </button>
          ))}
        </div>
      )}
      
      {showDropdown && (
        <div 
          onClick={() => setShowDropdown(false)}
          style={{ position: 'fixed', inset: 0, zIndex: 'var(--z-dropdown)' }}
        />
      )}
    </div>
  )
}

