/**
 * components/ui/ErrorState.tsx
 *
 * Displays the actual error message from the daemon.
 * Never invents placeholder text.
 * Used by every page that has a failed query.
 */

import { Icon } from './Icon'

interface ErrorStateProps {
  error: unknown
  /** Optional retry callback */
  onRetry?: () => void
  /** Optional title override */
  title?: string
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  return 'An unexpected error occurred'
}

export function ErrorState({ error, onRetry, title = 'Failed to load data' }: ErrorStateProps) {
  const message = getErrorMessage(error)

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: '12px',
        padding: '40px 24px',
        textAlign: 'center'}}
    >
      <Icon name="error_outline" size={40} style={{ color: 'var(--error)', opacity: 0.7 }} />
      <div style={{ fontWeight: 600, fontSize: 'var(--text-md)' }}>{title}</div>
      <div
        style={{
          fontSize: 'var(--text-sm)',
          color: 'var(--text-secondary)',
          fontFamily: 'var(--font-mono)',
          background: 'var(--error-bg)',
          border: '1px solid var(--error-border)',
          borderRadius: 'var(--radius-sm)',
          padding: '8px 14px',
          maxWidth: '480px',
          wordBreak: 'break-word'}}
      >
        {message}
      </div>
      {onRetry && (
        <button
          onClick={onRetry}
          style={{
            marginTop: '8px',
            padding: '8px 18px',
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-sm)',
            color: 'var(--text)',
            cursor: 'pointer',
            fontSize: 'var(--text-sm)',
            fontFamily: 'var(--font-ui)',
            display: 'flex',
            alignItems: 'center',
            gap: '6px'}}
        >
          <Icon name="refresh" size={16} />
          Retry
        </button>
      )}
    </div>
  )
}
