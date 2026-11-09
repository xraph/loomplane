// A stand-in for the browser's EventSource, which jsdom lacks. test-setup.ts
// installs it on window; a test drives a stream through the instance the
// code under test opened.

type Listener = (event: MessageEvent<string>) => void

export class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  /** Every source opened since the last reset, oldest first. */
  static instances: FakeEventSource[] = []

  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2
  readonly url: string
  readonly withCredentials: boolean
  readyState = FakeEventSource.CONNECTING
  closed = false
  onerror: ((event: Event) => void) | null = null
  onopen: ((event: Event) => void) | null = null
  private listeners = new Map<string, Set<Listener>>()

  constructor(url: string | URL, init?: EventSourceInit) {
    this.url = String(url)
    this.withCredentials = !!init?.withCredentials
    FakeEventSource.instances.push(this)
  }

  /** The latest source opened. */
  static latest(): FakeEventSource {
    const last = FakeEventSource.instances.at(-1)
    if (!last) throw new Error("no EventSource was opened")
    return last
  }

  static reset(): void {
    FakeEventSource.instances = []
  }

  addEventListener(name: string, listener: Listener): void {
    if (!this.listeners.has(name)) this.listeners.set(name, new Set())
    this.listeners.get(name)?.add(listener)
  }

  removeEventListener(name: string, listener: Listener): void {
    this.listeners.get(name)?.delete(listener)
  }

  close(): void {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }

  /** open says the connection is up. */
  open(): void {
    this.readyState = FakeEventSource.OPEN
    const event = new Event("open")
    this.onopen?.(event)
    this.dispatch("open", event as MessageEvent<string>)
  }

  /** emit sends one frame: its event name, JSON data and SSE id. */
  emit(name: string, data: unknown, id?: string | number): void {
    if (this.closed) return
    const event = new MessageEvent<string>(name, {
      data: JSON.stringify(data),
      lastEventId: id === undefined ? "" : String(id),
    })
    this.dispatch(name, event)
  }

  /**
   * fail drops the connection. A browser then reconnects by itself, with
   * Last-Event-ID, unless fatal says the server refused it.
   */
  fail(fatal = false): void {
    this.readyState = fatal
      ? FakeEventSource.CLOSED
      : FakeEventSource.CONNECTING
    const event = new Event("error")
    this.onerror?.(event)
    this.dispatch("error", event as MessageEvent<string>)
  }

  private dispatch(name: string, event: MessageEvent<string>): void {
    for (const listener of this.listeners.get(name) ?? []) listener(event)
  }
}
