/**
 * stores/ws.ts — D-PlaneOS WebSocket Store
 *
 * Manages a single connection to /ws/monitor.
 * Authenticates via session_id immediately after open.
 * Reconnects with exponential backoff (1s → 30s cap).
 * Distributes events to subscriber callbacks.
 *
 * WebSocket message types from daemon:
 *   initial_state | state_update   → stateUpdate subscribers
 *   hardware_event                 → hardwareEvent subscribers
 *   resilver_started               → resilverStarted subscribers
 *   resilver_progress              → resilverProgress subscribers
 *   resilver_completed             → resilverCompleted subscribers
 *   pool_health_change             → poolHealthChange subscribers
 *   disk_temperature_warning       → diskTempWarning subscribers
 *   scrub_started | scrub_completed → scrubEvent subscribers
 *   inotify_stats                  → inotifyStats subscribers
 */

import { create } from 'zustand'
import { getSessionId } from '@/lib/api'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type WsStatus = 'connecting' | 'connected' | 'disconnected'

type EventMap = {
  stateUpdate: (data: unknown) => void
  hardwareEvent: (data: unknown) => void
  resilverStarted: (data: unknown) => void
  resilverProgress: (data: unknown) => void
  resilverCompleted: (data: unknown) => void
  poolHealthChange: (data: unknown) => void
  diskTempWarning: (data: unknown) => void
  diskAdded: (data: unknown) => void
  diskRemoved: (data: unknown) => void
  diskReplacementAvailable: (data: unknown) => void
  mountError: (data: unknown) => void
  scrubEvent: (data: unknown) => void
  inotifyStats: (data: unknown) => void
}

type EventName = keyof EventMap

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

interface WsState {
  status: WsStatus
  connect: () => void
  disconnect: () => void
  on: <E extends EventName>(event: E, cb: EventMap[E]) => () => void
}

// Subscriber map — lives outside Zustand state to avoid re-render on subscribe
const subscribers = new Map<EventName, Set<EventMap[EventName]>>()
function getSet(event: EventName): Set<EventMap[EventName]> {
  if (!subscribers.has(event)) subscribers.set(event, new Set())
  return subscribers.get(event)!
}
function emit(event: EventName, data: unknown) {
  getSet(event).forEach((cb) => (cb as (d: unknown) => void)(data))
}

let ws: WebSocket | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | null = null
let reconnectDelay = 1000
let keepaliveTimer: ReturnType<typeof setInterval> | null = null
let intentionalClose = false

function clearReconnectTimer() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
}

function clearKeepalive() {
  if (keepaliveTimer) {
    clearInterval(keepaliveTimer)
    keepaliveTimer = null
  }
}

export const useWsStore = create<WsState>((set) => {
  function scheduleReconnect() {
    clearReconnectTimer()
    reconnectTimer = setTimeout(() => {
      if (!intentionalClose) doConnect()
    }, reconnectDelay)
    reconnectDelay = Math.min(reconnectDelay * 2, 30_000)
  }

  function doConnect() {
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
      return
    }

    intentionalClose = false
    set({ status: 'connecting' })

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    ws = new WebSocket(`${protocol}//${window.location.host}/ws/monitor`)

    ws.onopen = () => {
      reconnectDelay = 1000
      set({ status: 'connected' })

      // Authenticate immediately
      const sessionId = getSessionId()
      if (!sessionId) {
        // No session — close; auth store will redirect to /login
        ws?.close()
        return
      }
      ws!.send(JSON.stringify({ type: 'auth', session_id: sessionId }))

      // Keepalive ping every 30s to prevent proxy timeout
      clearKeepalive()
      keepaliveTimer = setInterval(() => {
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'ping' }))
        }
      }, 30_000)
    }

    ws.onmessage = (event) => {
      let msg: { type: string; data?: unknown } | null = null
      try {
        msg = JSON.parse(event.data as string) as { type: string; data?: unknown }
      } catch {
        return
      }

      switch (msg.type) {
        case 'initial_state':
        case 'state_update':
          emit('stateUpdate', msg.data)
          break
        case 'hardware_event':
          emit('hardwareEvent', msg.data ?? msg)
          break
        case 'resilver_started':
          emit('resilverStarted', msg.data ?? msg)
          break
        case 'resilver_progress':
          emit('resilverProgress', msg.data ?? msg)
          break
        case 'resilver_completed':
          emit('resilverCompleted', msg.data ?? msg)
          break
        case 'pool_health_change':
          emit('poolHealthChange', msg.data ?? msg)
          break
        case 'disk_temperature_warning':
          emit('diskTempWarning', msg.data ?? msg)
          break
        case 'scrub_started':
        case 'scrub_completed':
          emit('scrubEvent', msg.data ?? msg)
          break
        case 'inotify_stats':
        case 'inotify_status':
          emit('inotifyStats', msg.data ?? msg)
          break
        // pong — no-op
      }
    }

    ws.onclose = () => {
      clearKeepalive()
      if (!intentionalClose) {
        set({ status: 'disconnected' })
        scheduleReconnect()
      }
    }

    ws.onerror = () => {
      // onclose fires right after onerror; reconnect handled there
    }
  }

  return {
    status: 'disconnected',

    connect: doConnect,

    disconnect: () => {
      intentionalClose = true
      clearReconnectTimer()
      clearKeepalive()
      ws?.close()
      ws = null
      set({ status: 'disconnected' })
    },

    on: (event, cb) => {
      getSet(event).add(cb as EventMap[EventName])
      return () => {
        getSet(event).delete(cb as EventMap[EventName])
      }
    },
  }
})
