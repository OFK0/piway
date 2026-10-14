// Reconnect delays: exponential backoff with jitter.
//
// Attempt n waits a random time in [(1 - jitter) * c, c), where
// c = min(cap, base * 2^n). With the default jitter of 1 ("full jitter") that
// is [0, c). The
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
  /**
   * The share of each delay that is random, in (0, 1]. 1 (the default) is
   * full jitter: the widest spread between clients. Smaller values keep
   * delays close to the ceiling, so a user watching a countdown sees it grow
   * steadily instead of jumping around.
   */
  jitter?: number
  /** Returns a number in [0, 1). Injected so tests are deterministic. */
  random?: () => number
}

export class Backoff {
  private attempt = 0
  private readonly opts: BackoffOptions
  private readonly random: () => number
  private readonly jitter: number

  constructor(opts: BackoffOptions) {
    if (!(opts.base > 0) || !(opts.cap >= opts.base)) {
      throw new RangeError('backoff needs 0 < base <= cap')
    }
    const jitter = opts.jitter ?? 1
    if (!(jitter > 0 && jitter <= 1)) {
      throw new RangeError('backoff needs 0 < jitter <= 1')
    }
    this.opts = opts
    this.jitter = jitter
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
    const ceiling = this.ceiling()
    const delay = Math.floor(ceiling * (1 - this.jitter + this.jitter * this.random()))
    this.attempt++
    return delay
  }

  /** Call once a connection has proven healthy. */
  reset(): void {
    this.attempt = 0
  }
}
