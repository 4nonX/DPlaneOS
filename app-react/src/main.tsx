/**
 * main.tsx - D-PlaneOS v4 entry point
 *
 * Boot sequence:
 *  1. initCsrf()            - fetch CSRF token from /api/csrf, cache it
 *  2. validateSession()     - check session against /api/auth/check + /api/auth/session
 *  3. Render RouterProvider - TanStack Router handles auth redirects via beforeLoad
 *
 * CSRF must complete before any mutation can be made.
 * Session validation must complete before the router renders to avoid flash
 * of protected content redirecting to /login.
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { QueryClientProvider } from '@tanstack/react-query'

import '@/index.css'
import { initCsrf } from '@/lib/api'
import { queryClient } from '@/lib/queryClient'
import { useAuthStore } from '@/stores/auth'
import { router } from '@/routes/index'

async function boot() {
  // Step 1: CSRF token - required before any POST/PUT/DELETE
  try {
    await initCsrf()
  } catch (err) {
    // Daemon offline on load. Render anyway - pages will show error states.
    console.error('[boot] CSRF init failed:', err)
  }

  // Step 2: Session validation - determines isAuthenticated before first render
  await useAuthStore.getState().validateSession()

  // Step 3: Mount React
  const rootEl = document.getElementById('root')
  if (!rootEl) throw new Error('#root element not found')

  createRoot(rootEl).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  )
}

boot()

