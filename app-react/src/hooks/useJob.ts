/**
 * hooks/useJob.ts
 *
 * Polls GET /api/jobs/{id} every 2 seconds while a job is running.
 * Stops polling when the job reaches "done" or "failed".
 *
 * Daemon job response shapes:
 *   Running:  { id, type, status: "running", started_at, progress? }  // progress from poll or WS
 *   Done:     { id, type, status: "done", result: {...}, started_at, finished_at }
 *   Failed:   { id, type, status: "failed", error: "...", started_at, finished_at }
 *   404:      job not found (daemon restarted mid-job)
 */

import { useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/lib/api'
import { useWsStore } from '@/stores/ws'

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
  progress?: {
    percent?: number
    bytes_sent?: number
    total_bytes?: number
    rate_mbs?: number
    eta_seconds?: number
  }
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export function useJob(jobId: string | null | undefined) {
  const queryClient = useQueryClient()
  const ws = useWsStore()

  const query = useQuery<JobSnapshot, ApiError>({
    queryKey: ['job', jobId],
    queryFn: ({ signal }) => api.get<JobSnapshot>(`/api/jobs/${jobId}`, signal),
    enabled: !!jobId,
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return 2_000
      return data.status === 'running' ? 5_000 : false // Slower poll if WS is active
    },
    retry: (failureCount, error) => {
      if (error instanceof ApiError && error.status === 404) return false
      return failureCount < 3
    },
  })

  // WebSocket progress: active while this job may still be running (including before first poll returns).
  useEffect(() => {
    if (!jobId) return
    const st = query.data?.status
    if (st === 'done' || st === 'failed') return

    return ws.on('jobProgress', (msg) => {
      if (msg.job_id !== jobId) return
      queryClient.setQueryData(['job', jobId], (old: JobSnapshot | undefined) => {
        if (!old) {
          return {
            id: jobId,
            type: '',
            status: 'running',
            started_at: new Date().toISOString(),
            progress: msg.data,
          }
        }
        if (old.status !== 'running') return old
        return { ...old, progress: msg.data }
      })
    })
  }, [jobId, query.data?.status, ws, queryClient])

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

