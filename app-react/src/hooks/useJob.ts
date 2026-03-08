/**
 * hooks/useJob.ts
 *
 * Polls GET /api/jobs/{id} every 2 seconds while a job is running.
 * Stops polling when the job reaches "done" or "failed".
 *
 * Daemon job response shapes:
 *   Running:  { id, type, status: "running", started_at }
 *   Done:     { id, type, status: "done", result: {...}, started_at, finished_at }
 *   Failed:   { id, type, status: "failed", error: "...", started_at, finished_at }
 *   404:      job not found (daemon restarted mid-job)
 */

import { useQuery } from '@tanstack/react-query'
import { api, ApiError } from '@/lib/api'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface JobSnapshot {
  id: string
  type: string
  status: 'running' | 'done' | 'failed'
  result?: Record<string, unknown>
  error?: string
  started_at: string
  finished_at?: string
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * useJob(jobId)
 *
 * Pass null/undefined to disable polling (before a job has been started).
 *
 * Returns:
 *   data     — JobSnapshot | undefined
 *   isPolling — true while status === "running"
 *   interrupted — true if the daemon returned 404 (restarted mid-job)
 *   error    — ApiError if the poll request itself failed
 */
export function useJob(jobId: string | null | undefined) {
  const query = useQuery<JobSnapshot, ApiError>({
    queryKey: ['job', jobId],
    queryFn: ({ signal }) => api.get<JobSnapshot>(`/api/jobs/${jobId}`, signal),
    enabled: !!jobId,
    // Poll every 2 seconds while the job is still running
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return 2_000
      return data.status === 'running' ? 2_000 : false
    },
    retry: (failureCount, error) => {
      // Don't retry 404 — that means the daemon restarted mid-job
      if (error instanceof ApiError && error.status === 404) return false
      // Retry other transient errors up to 3 times
      return failureCount < 3
    },
  })

  const interrupted =
    query.isError &&
    query.error instanceof ApiError &&
    query.error.status === 404

  const isPolling = !!jobId && query.data?.status === 'running'

  return {
    ...query,
    isPolling,
    interrupted,
  }
}
