import type { Snapshot } from '@/api'

// Status follows DESIGN.md: only "critical" uses the attention color; "warning"
// is shown with an icon and a word in ink. "ok" shows nothing extra.
export type Level = 'ok' | 'warning' | 'critical'

export interface Status {
  level: Level
  label: string
}

const ok: Status = { level: 'ok', label: 'Normal' }

// The Pi 4 firmware starts soft throttling at 80 °C.
export function temperatureStatus(celsius: number): Status {
  if (celsius >= 80) return { level: 'critical', label: 'Hot' }
  if (celsius >= 70) return { level: 'warning', label: 'Warm' }
  return ok
}

export function usageStatus(percent: number): Status {
  if (percent >= 90) return { level: 'critical', label: 'Nearly full' }
  if (percent >= 75) return { level: 'warning', label: 'Filling up' }
  return ok
}

export function cpuStatus(percent: number): Status {
  if (percent >= 90) return { level: 'critical', label: 'Saturated' }
  if (percent >= 70) return { level: 'warning', label: 'Busy' }
  return ok
}

// Problems the firmware reports right now. They outrank everything else on
// the page because they mean the hardware is protecting itself.
export function hardwareAlerts(s: Snapshot): string[] {
  const now = s.thermal?.throttle?.now
  if (!now) return []
  const out: string[] = []
  if (now.under_voltage) out.push('Under-voltage: the power supply cannot keep up. Use the official 5.1 V / 3 A supply.')
  if (now.throttled) out.push('CPU is throttled to protect the board.')
  else if (now.freq_capped) out.push('CPU frequency is capped.')
  if (now.soft_temp_limit) out.push('Soft temperature limit reached.')
  return out
}

type Collector = 'cpu' | 'memory' | 'thermal' | 'disk' | 'network'

// Why a metric is null, in words a person can act on (docs/API.md › Null fields).
export function missingReason(s: Snapshot, collector: Collector, rateBased: boolean): string {
  const err = s.errors?.[collector]
  if (err) return `Collection failed: ${err}`
  if (rateBased && s.seq === 1) return 'Needs a second sample. Refresh in a moment.'
  return 'Not available on this machine.'
}
