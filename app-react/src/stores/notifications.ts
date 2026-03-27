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

  ws.on('poolHealthChange', (data: any) => {
    notify({
      type: data.health === 'ONLINE' ? 'success' : 'error',
      title: `Pool Health: ${data.name}`,
      message: `Status changed to ${data.health}`
    })
  })

  ws.on('hardwareEvent', (data: any) => {
    notify({
      type: 'warning',
      title: 'Hardware Event',
      message: data.message || `Event: ${data.event}`
    })
  })

  ws.on('gitopsDrift', (data: any) => {
    notify({
      type: 'warning',
      title: 'GitOps Drift Detected',
      message: `System configuration has drifted from ${data.repo_url}`
    })
  })
}
