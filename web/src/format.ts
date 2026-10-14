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

export function bytes(n: number): string {
  const { value, unit } = bytesParts(n)
  return `${value} ${unit}`
}

export const rate = (n: number) => `${bytes(n)}/s`

export const percent = (n: number) => `${n.toFixed(1)}%`
