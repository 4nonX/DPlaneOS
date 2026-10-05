/**
 * stores/notifications.ts
 *
 * Manages the persistent notification history and the visibility of the
 * Pending Changes sidebar.
 */

import { create } from 'zustand'
import { useWsStore } from './ws'

export interface AppNotification {
  id: string
  type: 'info' | 'warning' | 'error' | 'success'
  title: string
  message: string
  timestamp: string
  read: boolean
}

interface NotificationsState {
  notifications: AppNotification[]
  isSidebarOpen: boolean
  setSidebarOpen: (open: boolean) => void
  addNotification: (n: Omit<AppNotification, 'id' | 'timestamp' | 'read'>) => void
  markRead: (id: string | 'all') => void
  clear: () => void
}

export const useNotificationsStore = create<NotificationsState>((set) => ({
  notifications: [],
  isSidebarOpen: false,

  setSidebarOpen: (open) => set({ isSidebarOpen: open }),

  addNotification: (n) => set((s) => ({
    notifications: [
      {
        ...n,
        id: Math.random().toString(36).substring(7),
        timestamp: new Date().toISOString(),
        read: false
      },
      ...s.notifications.slice(0, 49) // Keep last 50
    ]
  })),

  markRead: (id) => set((s) => ({
    notifications: s.notifications.map((n) => 
      (id === 'all' || n.id === id) ? { ...n, read: true } : n
    )
  })),

  clear: () => set({ notifications: [] })
}))

// Wire up WebSocket events to the notification store
// This is a one-time setup that can be called from AppShell or similar
export function initNotificationSubscribers() {
  const ws = useWsStore.getState()
  const notify = useNotificationsStore.getState().addNotification

  ws.on('poolHealthChange', (data: unknown) => {
    const d = data as { health?: string; name?: string }
    notify({
      type: d.health === 'ONLINE' ? 'success' : 'error',
      title: `Pool Health: ${d.name ?? ''}`,
      message: `Status changed to ${d.health ?? ''}`
    })
  })

  ws.on('hardwareEvent', (data: unknown) => {
    const d = data as { message?: string; event?: string }
    notify({
      type: 'warning',
      title: 'Hardware Event',
      message: d.message || `Event: ${d.event ?? ''}`
    })
  })

  ws.on('gitopsCommitFailed', (data: unknown) => {
    const d = data as { error?: string }
    notify({
      type: 'warning',
      title: 'GitOps: change not committed',
      message: d.error ?? 'A web UI change could not be written back to the Git repository'
    })
  })

  ws.on('gitopsDrift', (data: unknown) => {
    const d = data as { repo_url?: string }
    notify({
      type: 'warning',
      title: 'GitOps Drift Detected',
      message: `System configuration has drifted from ${d.repo_url ?? ''}`
    })
  })
}
