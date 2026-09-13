// Thin fetch wrapper for talking to the Go backend. Deliberately no heavy
// state library - plain fetch + React state is enough for this app's needs.

// Baked in at build time (see frontend/Dockerfile's VITE_API_BASE_URL build
// arg) so a container image can point at whatever host/port the backend is
// actually published on. Falls back to localhost:8080 for local `npm run dev`.
export const API_BASE = import.meta.env.VITE_API_BASE_URL || 'http://localhost:8080'

/**
 * Fetches the current authenticated user, or null if not logged in.
 */
export async function getMe() {
  const res = await fetch(`${API_BASE}/api/me`, { credentials: 'include' })
  if (!res.ok) return null
  return res.json()
}

/**
 * URL to send the browser to in order to start the Bitbucket OAuth flow.
 */
export function loginUrl() {
  return `${API_BASE}/auth/login`
}

/**
 * Clears the session cookie server-side.
 */
export async function logout() {
  await fetch(`${API_BASE}/auth/logout`, { method: 'POST', credentials: 'include' })
}

/**
 * Lists the current user's repo subscriptions.
 */
export async function listRepos() {
  const res = await fetch(`${API_BASE}/api/repos`, { credentials: 'include' })
  if (!res.ok) throw new Error('failed to load repos')
  return res.json()
}

/**
 * Subscribes to a repo. Throws with the backend's error message on failure
 * (e.g. repo not found, already subscribed).
 */
export async function addRepo(workspace, repoSlug) {
  const res = await fetch(`${API_BASE}/api/repos`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace, repoSlug }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || 'failed to add repo')
  }
  return res.json()
}

/**
 * Removes a repo subscription by its id.
 */
export async function removeRepo(id) {
  const res = await fetch(`${API_BASE}/api/repos/${id}`, {
    method: 'DELETE',
    credentials: 'include',
  })
  if (!res.ok) throw new Error('failed to remove repo')
}

/**
 * Lists commits across the user's subscribed repos, newest first.
 * Pass `before` (a previous response's nextCursor) to load older commits.
 */
export async function listCommits(before) {
  const url = new URL(`${API_BASE}/api/commits`)
  if (before) url.searchParams.set('before', before)
  const res = await fetch(url, { credentials: 'include' })
  if (!res.ok) throw new Error('failed to load commit feed')
  return res.json()
}
