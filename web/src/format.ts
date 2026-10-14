const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

/** Splits a byte count into a display number and its binary unit. */
export function bytesParts(n: number): { value: string; unit: string } {
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return { value: n.toFixed(i === 0 ? 0 : 1), unit: units[i] }
}

/** The unit bytesParts picks for n, and how many bytes one of it is. */
export function byteScale(n: number): { div: number; unit: string } {
  let i = 0
  while (n >= 1024 ** (i + 1) && i < units.length - 1) i++
  return { div: 1024 ** i, unit: units[i] }
}

export function bytes(n: number): string {
  const { value, unit } = bytesParts(n)
  return `${value} ${unit}`
}

export const rate = (n: number) => `${bytes(n)}/s`

export const percent = (n: number) => `${n.toFixed(1)}%`

/** A whole number; for counters that animate through fractions. */
export const count = (n: number) => Math.round(n).toString()
