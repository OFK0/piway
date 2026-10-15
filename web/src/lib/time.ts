const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['day', 86_400_000],
  ['hour', 3_600_000],
  ['minute', 60_000],
  ['second', 1000],
]

/**
 * "3 minutes ago", "yesterday". now is passed in so render stays pure; it
 * can lag the data by up to its refresh interval, so anything newer than
 * five seconds (or "in the future") reads "now".
 */
export function ago(iso: string, now: number): string {
  const diff = Date.parse(iso) - now
  if (diff > -5000) return rtf.format(0, 'second') // "now", in the reader's language
  for (const [unit, ms] of units) {
    if (Math.abs(diff) >= ms || unit === 'second') return rtf.format(Math.round(diff / ms), unit)
  }
  return ''
}

/** "1 m 12 s", "45 s", for a build's duration. */
export function duration(fromIso: string, toIso: string): string {
  const s = Math.max(0, Math.round((Date.parse(toIso) - Date.parse(fromIso)) / 1000))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  return s % 60 === 0 ? `${m} m` : `${m} m ${s % 60} s`
}

export const shortSha = (sha: string) => sha.slice(0, 7)

/** A Go duration without zero parts: "5m0s" → "5m", "1h0m0s" → "1h", "30s" stays. */
export const shortDuration = (d: string) => d.replace(/([hm])0s$/, '$1').replace(/h0m$/, 'h')
