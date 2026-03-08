/**
 * hooks/useToast.ts
 *
 * Lightweight global toast system.
 * Used by TanStack Query's onError in queryClient, and directly by components.
 */

import { create } from 'zustand'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type ToastType = 'success' | 'error' | 'warning' | 'info'

export interface Toast {
  id: string
  message: string
  type: ToastType
  /** Duration in ms before auto-dismiss. 0 = sticky. Default: 5000. */
  duration: number
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

interface ToastState {
  toasts: Toast[]
  add: (message: string, type?: ToastType, duration?: number) => void
  remove: (id: string) => void
}

export const useToastStore = create<ToastState>((set) => ({
  toasts: [],

  add: (message, type = 'info', duration = 5000) => {
    const id = `${Date.now()}-${Math.random()}`
    const toast: Toast = { id, message, type, duration }

    set((state) => ({ toasts: [...state.toasts, toast] }))

    if (duration > 0) {
      setTimeout(() => {
        set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
      }, duration)
    }
  },

  remove: (id) => {
    set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
  },
}))

/** Convenience accessor for use outside React components (e.g. queryClient callbacks) */
export const toast = {
  success: (msg: string, duration?: number) =>
    useToastStore.getState().add(msg, 'success', duration),
  error: (msg: string, duration?: number) =>
    useToastStore.getState().add(msg, 'error', duration),
  warning: (msg: string, duration?: number) =>
    useToastStore.getState().add(msg, 'warning', duration),
  info: (msg: string, duration?: number) =>
    useToastStore.getState().add(msg, 'info', duration),
}
