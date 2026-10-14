// A fixed-capacity FIFO. Once full, each push overwrites the oldest item,
// so memory stays bounded however long the dashboard stays open: at one
// snapshot per second, 300 items is the last five minutes.
//
// Storage is a preallocated array plus a head index. Unlike
// array.push + array.shift, a push never moves the other items.
export class RingBuffer<T> {
  readonly capacity: number
  private readonly items: (T | undefined)[]
  private head = 0 // index of the oldest item
  private count = 0

  constructor(capacity: number) {
    if (!Number.isInteger(capacity) || capacity < 1) {
      throw new RangeError(`capacity must be a positive integer, got ${capacity}`)
    }
    this.capacity = capacity
    this.items = new Array(capacity)
  }

  get size(): number {
    return this.count
  }

  push(item: T): void {
    const tail = (this.head + this.count) % this.capacity
    this.items[tail] = item
    if (this.count < this.capacity) {
      this.count++
    } else {
      // Full: the slot we just wrote was the oldest; the next one is now.
      this.head = (this.head + 1) % this.capacity
    }
  }

  /** The newest item, or undefined when empty. */
  last(): T | undefined {
    if (this.count === 0) return undefined
    return this.items[(this.head + this.count - 1) % this.capacity]
  }

  /** Items from oldest to newest, as a new array. */
  toArray(): T[] {
    const out = new Array<T>(this.count)
    for (let i = 0; i < this.count; i++) {
      out[i] = this.items[(this.head + i) % this.capacity] as T
    }
    return out
  }

  clear(): void {
    this.items.fill(undefined)
    this.head = 0
    this.count = 0
  }
}
