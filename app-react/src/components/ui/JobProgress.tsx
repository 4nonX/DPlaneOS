/**
 * components/ui/JobProgress.tsx
 *
 * Displays the status of an async daemon job (replication, docker pull, etc.).
 * Uses useJob() which polls GET /api/jobs/{id} every 2s.
 *
 * States:
 *   running     → spinner + "Operation in progress…"
 *   done        → success badge + optional result
 *   failed      → error state with daemon error message
 *   interrupted → special message (daemon restarted mid-job)
 */

import { useJob } from '@/hooks/useJob'
import { Spinner } from './LoadingSpinner'
import { ErrorState } from './ErrorState'
import { Icon } from './Icon'

interface JobProgressProps {
  jobId: string | null | undefined
  /** Label shown while running */
  runningLabel?: string
  /** Label shown when done */
  doneLabel?: string
  /** Called when job reaches "done" or "failed" */
  onDone?: (result: Record<string, unknown> | undefined) => void
  onFailed?: (error: string) => void
}

export function JobProgress({
  jobId,
  runningLabel = 'Operation in progress…',
  doneLabel = 'Operation completed',
  onDone,
  onFailed,
}: JobProgressProps) {
  const { data, isPolling, interrupted, isError, error } = useJob(jobId)

  // Callbacks fire on state transitions
  if (data?.status === 'done' && onDone) onDone(data.result)
  if (data?.status === 'failed' && onFailed) onFailed(data.error ?? 'Unknown error')

  if (!jobId) return null

  if (interrupted) {
    return (
      <div
        style={{
          padding: '12px 16px',
          background: 'var(--warning-bg)',
          border: '1px solid var(--warning-border)',
          borderRadius: 'var(--radius-sm)',
          fontSize: 'var(--text-sm)',
          color: 'var(--warning)',
          display: 'flex',
          alignItems: 'center',
          gap: '8px',
        }}
      >
        <Icon name="warning" size={16} />
        Operation interrupted — daemon restarted mid-job. Please retry.
      </div>
    )
  }

  if (isError) {
    return <ErrorState error={error} title="Failed to check job status" />
  }

  if (!data || isPolling) {
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '10px',
          padding: '12px 16px',
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-sm)',
          fontSize: 'var(--text-sm)',
          color: 'var(--text-secondary)',
        }}
      >
        <Spinner size={16} />
        <span>{runningLabel}</span>
      </div>
    )
  }

  if (data.status === 'done') {
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '8px',
          padding: '12px 16px',
          background: 'var(--success-bg)',
          border: '1px solid var(--success-border)',
          borderRadius: 'var(--radius-sm)',
          fontSize: 'var(--text-sm)',
          color: 'var(--success)',
        }}
      >
        <Icon name="check_circle" size={16} />
        {doneLabel}
      </div>
    )
  }

  if (data.status === 'failed') {
    return (
      <ErrorState
        error={data.error ?? 'Operation failed'}
        title="Operation failed"
      />
    )
  }

  return null
}
