const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

export function bytes(n: number): string {
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export const rate = (n: number) => `${bytes(n)}/s`

export const percent = (n: number) => `${n.toFixed(1)}%`
