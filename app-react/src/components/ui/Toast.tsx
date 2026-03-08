/**
 * components/ui/Toast.tsx
 * Slide-in toast notifications with auto-dismiss progress bar.
 */

import { useEffect, useState } from 'react'
import { useToastStore, type Toast } from '@/hooks/useToast'
import { Icon } from './Icon'

const CONFIG = {
  success: { icon: 'check_circle', color: 'var(--success)', border: 'var(--success-border)' },
  error:   { icon: 'cancel',       color: 'var(--error)',   border: 'var(--error-border)' },
  warning: { icon: 'warning',      color: 'var(--warning)', border: 'var(--warning-border)' },
  info:    { icon: 'info',         color: 'var(--primary)', border: 'rgba(138,156,255,0.3)' },
} as const

function ToastItem({ toast }: { toast: Toast }) {
  const remove  = useToastStore((s) => s.remove)
  const [progress, setProgress] = useState(100)
  const cfg = CONFIG[toast.type as keyof typeof CONFIG] ?? CONFIG.info

  // Countdown progress bar
  useEffect(() => {
    const duration = 4000
    const interval = 30
    const step = (interval / duration) * 100
    const t = setInterval(() => setProgress((p) => Math.max(0, p - step)), interval)
    return () => clearInterval(t)
  }, [toast.id])

  return (
    <div
      role="alert"
      style={{
        display: 'flex', alignItems: 'flex-start', gap: 11,
        padding: '13px 14px',
        background: 'rgba(14,14,16,0.97)',
        border: `1px solid ${cfg.border}`,
        borderLeft: `3px solid ${cfg.color}`,
        borderRadius: 'var(--radius-md)',
        backdropFilter: 'blur(16px)',
        boxShadow: '0 8px 32px rgba(0,0,0,0.55), 0 0 0 1px rgba(255,255,255,0.04)',
        minWidth: 280, maxWidth: 400,
        pointerEvents: 'all',
        animation: 'slideInRight 0.25s cubic-bezier(0.16,1,0.3,1) both',
        position: 'relative', overflow: 'hidden',
      }}
    >
      <Icon name={cfg.icon} size={18} style={{ color: cfg.color, flexShrink: 0, marginTop: 1 }} />
      <span style={{ flex: 1, fontSize: 'var(--text-sm)', lineHeight: 1.5, color: 'rgba(255,255,255,0.88)' }}>
        {toast.message}
      </span>
      <button
        onClick={() => remove(toast.id)}
        aria-label="Dismiss"
        style={{
          background: 'none', border: 'none', cursor: 'pointer',
          color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center',
          flexShrink: 0, padding: 2, marginLeft: 2, borderRadius: 4,
          transition: 'color var(--transition-fast)',
        }}
        onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--text)'; }}
        onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-tertiary)'; }}
      >
        <Icon name="close" size={16} />
      </button>

      {/* Progress bar */}
      <div style={{
        position: 'absolute', bottom: 0, left: 0,
        height: 2, width: `${progress}%`,
        background: cfg.color, opacity: 0.5,
        transition: 'width 30ms linear',
        borderRadius: '0 0 0 3px',
      }} />
    </div>
  )
}

export function ToastContainer() {
  const toasts = useToastStore((s) => s.toasts)
  if (toasts.length === 0) return null

  return (
    <div
      aria-live="polite"
      aria-label="Notifications"
      style={{
        position: 'fixed',
        top: 'calc(var(--topbar-height) + 14px)',
        right: '20px',
        zIndex: 'var(--z-toast)' as any,
        display: 'flex', flexDirection: 'column', gap: 8,
        pointerEvents: 'none',
      }}
    >
      {toasts.map((t) => <ToastItem key={t.id} toast={t} />)}
    </div>
  )
}
