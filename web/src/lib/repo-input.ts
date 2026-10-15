// Accepts what people paste for a repository: owner/name, or a GitHub URL
// (https, git@ or with .git) and returns "owner/name", or null.
export function parseRepo(input: string): string | null {
  let s = input.trim()
  s = s.replace(/^git@github\.com:/i, '').replace(/^(https?:\/\/)?(www\.)?github\.com\//i, '')
  s = s.replace(/\.git$/i, '').replace(/\/+$/, '')
  return /^[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/.test(s) ? s : null
}
