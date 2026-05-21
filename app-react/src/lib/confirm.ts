import { apiFetch } from '@/lib/api'

/**
 * Issue a short-lived (60 s) server-side confirmation token for a named
 * destructive operation. The token must be sent as X-Confirm-Token on the
 * actual destructive request within 60 seconds of issuance.
 *
 * @param operation - must match a validConfirmOps entry on the server
 * @param target    - the specific resource being destroyed (pool name, container name, etc.)
 */
export async function issueConfirmToken(operation: string, target: string): Promise<string> {
  const res = await apiFetch<{ success: boolean; token: string }>(
    '/api/confirm/issue',
    { method: 'POST', body: { operation, target } },
  )
  return res.token
}
