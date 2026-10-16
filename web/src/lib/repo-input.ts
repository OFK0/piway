// Accepts what people paste for a repository: owner/name, or a GitHub URL
// (https, git@ or with .git) and returns "owner/name", or null.
export function parseRepo(input: string): string | null {
  let s = input.trim()
  s = s.replace(/^git@github\.com:/i, '').replace(/^(https?:\/\/)?(www\.)?github\.com\//i, '')
  s = s.replace(/\.git$/i, '').replace(/\/+$/, '')
  return /^[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/.test(s) ? s : null
}

/**
 * Accepts what people paste for a GitLab project: group/name, nested
 * group/sub/name, or its URL (https, git@, with .git or a /-/ page) and
 * returns the project path, or null. host is the server it is on.
 */
export function parseGitLabRepo(input: string, host: string): string | null {
  let s = input.trim()
  const h = host.trim().replace(/^https?:\/\//i, '').replace(/\/+$/, '') || 'gitlab.com'
  const esc = h.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  s = s.replace(new RegExp(`^git@${esc}:`, 'i'), '').replace(new RegExp(`^(https?://)?${esc}/`, 'i'), '')
  s = s.replace(/\/-\/.*$/, '').replace(/\.git$/i, '').replace(/\/+$/, '')
  return /^[A-Za-z0-9._-]+(\/[A-Za-z0-9._-]+)+$/.test(s) ? s : null
}
