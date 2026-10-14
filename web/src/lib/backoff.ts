// Reconnect delays: exponential backoff with "full jitter".
//
// Attempt n waits a random time in [0, min(cap, base * 2^n)). The
// exponential part stops a dead server from being hammered; the jitter
// stops every open dashboard from reconnecting in the same instant when
// piway restarts (a thundering herd). Full jitter spreads clients the most
// for the same average wait; see "Exponential Backoff And Jitter",
// AWS Architecture Blog, 2015.

export interface BackoffOptions {
  /** First ceiling, in ms. */
  base: number
  /** Largest ceiling, in ms. */
  cap: number
  /** Returns a number in [0, 1). Injected so tests are deterministic. */
  random?: () => number
}

export class Backoff {
  private attempt = 0
  private readonly opts: BackoffOptions
  private readonly random: () => number

  constructor(opts: BackoffOptions) {
    if (!(opts.base > 0) || !(opts.cap >= opts.base)) {
      throw new RangeError('backoff needs 0 < base <= cap')
    }
    this.opts = opts
    this.random = opts.random ?? Math.random
  }

  /** How many delays have been handed out since the last reset. */
  get attempts(): number {
    return this.attempt
  }

  /** The upper bound for the next delay. */
  ceiling(): number {
    // 2 ** attempt overflows to Infinity long before it matters; min() fixes it.
    return Math.min(this.opts.cap, this.opts.base * 2 ** this.attempt)
  }

  /** The delay before the next attempt, in ms. */
  next(): number {
    const delay = Math.floor(this.random() * this.ceiling())
    this.attempt++
    return delay
  }

  /** Call once a connection has proven healthy. */
  reset(): void {
    this.attempt = 0
  }
}
