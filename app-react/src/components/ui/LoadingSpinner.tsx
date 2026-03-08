/**
 * components/ui/LoadingSpinner.tsx
 *
 * Inline spinner and skeleton block for loading states.
 * Used by every page/component during TanStack Query isLoading.
 */

interface SpinnerProps {
  size?: number
  color?: string
}

export function Spinner({ size = 24, color = 'var(--primary)' }: SpinnerProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      aria-label="Loading"
      role="status"
      style={{ animation: 'spin 0.8s linear infinite' }}
    >
      <style>{`@keyframes spin { to { transform: rotate(360deg) } }`}</style>
      <circle
        cx="12"
        cy="12"
        r="10"
        stroke="rgba(255,255,255,0.1)"
        strokeWidth="2.5"
      />
      <path
        d="M12 2a10 10 0 0 1 10 10"
        stroke={color}
        strokeWidth="2.5"
        strokeLinecap="round"
      />
    </svg>
  )
}

interface SkeletonProps {
  width?: string | number
  height?: string | number
  borderRadius?: string
  style?: React.CSSProperties
}

export function Skeleton({ width = '100%', height = 16, borderRadius = 'var(--radius-sm)', style }: SkeletonProps) {
  return (
    <div
      aria-hidden="true"
      style={{
        width,
        height,
        borderRadius,
        background: 'linear-gradient(90deg, var(--surface) 25%, var(--surface-hover) 50%, var(--surface) 75%)',
        backgroundSize: '200% 100%',
        animation: 'shimmer 1.5s infinite',
        ...style,
      }}
    />
  )
}

/** Full-page or card-level loading state */
export function LoadingState({ message = 'Loading…' }: { message?: string }) {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: '12px',
        padding: '40px 24px',
      }}
    >
      <style>{`@keyframes shimmer { to { background-position: -200% 0 } }`}</style>
      <Spinner size={32} />
      <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{message}</div>
    </div>
  )
}

import type React from 'react'
