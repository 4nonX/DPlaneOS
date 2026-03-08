/**
 * lib/queryClient.ts
 *
 * Global TanStack Query client.
 *
 * Global onError: every query and mutation failure that isn't handled locally
 * surfaces the actual daemon error message as a toast.
 *
 * No retries on 401 (handled in apiFetch) or 4xx (client errors).
 * 3 retries on transient errors (5xx, network failure).
 */

import { QueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'
import { toast } from '@/hooks/useToast'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,        // 30s before background refetch
      gcTime: 5 * 60_000,       // 5min cache retention
      retry: (failureCount, error) => {
        // Don't retry auth errors or client-side errors
        if (error instanceof ApiError && error.status < 500) return false
        return failureCount < 3
      },
    },
    mutations: {
      onError: (error) => {
        const message =
          error instanceof ApiError
            ? error.message
            : error instanceof Error
              ? error.message
              : 'An unknown error occurred'
        toast.error(message)
      },
    },
  },
})
