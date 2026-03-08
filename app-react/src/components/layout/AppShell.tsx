/**
 * components/layout/AppShell.tsx
 *
 * Main application layout: Sidebar + TopBar + page content area.
 * Also mounts the toast container and connects the WebSocket on load.
 */

import { useState, useEffect } from 'react'
import { Outlet } from '@tanstack/react-router'
import { Sidebar } from './Sidebar'
import { TopBar } from './TopBar'
import { ToastContainer } from '@/components/ui/Toast'
import { useWsStore } from '@/stores/ws'

export function AppShell() {
  const [collapsed, setCollapsed] = useState(false)
  const connect = useWsStore((s) => s.connect)
  const disconnect = useWsStore((s) => s.disconnect)

  // Connect WebSocket on mount, disconnect on unmount
  useEffect(() => {
    connect()
    return () => disconnect()
  }, [connect, disconnect])

  const sidebarWidth = collapsed
    ? 'var(--sidebar-width-collapsed)'
    : 'var(--sidebar-width)'

  return (
    <>
      <Sidebar collapsed={collapsed} onToggle={() => setCollapsed((c) => !c)} />

      <TopBar sidebarCollapsed={collapsed} />

      <main
        style={{
          marginLeft: sidebarWidth,
          marginTop: 'var(--topbar-height)',
          minHeight: 'calc(100vh - var(--topbar-height))',
          padding: '32px',
          transition: 'margin-left 0.2s ease',
          maxWidth: '1600px',
        }}
      >
        <Outlet />
      </main>

      <ToastContainer />
    </>
  )
}
