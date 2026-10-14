import type { Snapshot } from '@/api'

// How many rows the last snapshot filled. The loading skeleton draws the same
// number of cores, interfaces and disks, so the page already has its final
// height when the data arrives instead of growing on every load.
export interface SnapshotShape {
  cores: number
  interfaces: number
  devices: number
}

const storageKey = 'piway-snapshot-shape'

// The first visit's guess: a Raspberry Pi 4 with eth0, wlan0 and an SD card.
const piDefault: SnapshotShape = { cores: 4, interfaces: 2, devices: 1 }

export function loadShape(): SnapshotShape {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(storageKey) ?? 'null')
    if (isShape(v)) return v
  } catch {
    // Storage blocked or a value from an older version: fall through.
  }
  return piDefault
}

export function saveShape(s: Snapshot) {
  const shape: SnapshotShape = {
    cores: s.cpu?.per_core.length ?? 0,
    interfaces: s.network?.length ?? 0,
    devices: s.disk?.devices?.length ?? 0,
  }
  try {
    localStorage.setItem(storageKey, JSON.stringify(shape))
  } catch {
    // Without storage the next load uses the default shape.
  }
}

function isShape(v: unknown): v is SnapshotShape {
  if (typeof v !== 'object' || v === null) return false
  const s = v as Record<string, unknown>
  return [s.cores, s.interfaces, s.devices].every((n) => Number.isInteger(n) && (n as number) >= 0 && (n as number) <= 256)
}
