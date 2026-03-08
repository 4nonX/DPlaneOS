/**
 * components/ui/Toast.tsx
 *
 * Renders the global toast queue from useToastStore.
 * Placed once in AppShell, visible across all pages.
 */

import { useToastStore, type Toast } from '@/hooks/useToast'
import { Icon } from './Icon'

const ICONS: Record<string, string> = {
  success: 'check_circle',
  error:   'cancel',
  warning: 'warning',
  info:    'info',
}

const ACCENT: Record<string, string> = {
  success: 'var(--success)',
  error:   'var(--error)',
  warning: 'var(--warning)',
  info:    'var(--primary)',
}

function ToastItem({ toast }: { toast: Toast }) {
  const remove = useToastStore((s) => s.remove)
  const accent = ACCENT[toast.type] ?? ACCENT.info
  const icon   = ICONS[toast.type]  ?? ICONS.info

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: '12px',
        padding: '14px 16px',
        borderRadius: 'var(--radius-md)',
        background: 'rgba(18, 18, 20, 0.97)',
        border: '1px solid var(--border-strong)',
        borderLeft: `4px solid ${accent}`,
        backdropFilter: 'blur(12px)',
        boxShadow: '0 8px 32px rgba(0,0,0,0.5)',
        minWidth: '280px',
        maxWidth: '420px',
        pointerEvents: 'all',
      }}
      role="alert"
    >
      <Icon name={icon} size={20} style={{ color: accent, flexShrink: 0, marginTop: 1 }} />
      <span style={{ flex: 1, fontSize: 'var(--text-sm)', lineHeight: 1.5, color: 'rgba(255,255,255,0.92)' }}>
        {toast.message}
      </span>
      <button
        onClick={() => remove(toast.id)}
        aria-label="Dismiss notification"
        style={{
          background: 'none',
          border: 'none',
          cursor: 'pointer',
          color: 'var(--text-tertiary)',
          display: 'flex',
          alignItems: 'center',
          flexShrink: 0,
          padding: 0,
          marginLeft: 4,
        }}
      >
        <Icon name="close" size={18} />
      </button>
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
        top: 'calc(var(--topbar-height) + 16px)',
        right: '24px',
        zIndex: 'var(--z-toast)',
        display: 'flex',
        flexDirection: 'column',
        gap: '10px',
        pointerEvents: 'none',
      }}
    >
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} />
      ))}
    </div>
  )
}
