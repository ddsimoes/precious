// MockEventSource stands in for the browser's EventSource, which jsdom lacks.
// Tests drive it: open() accepts the connection, emit() delivers a named
// event with its id, and fail() reports an error in a given ready state.
export class MockEventSource extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2

  // instances lists every stream created since the test began.
  static instances: MockEventSource[] = []

  static latest(): MockEventSource {
    const latest = MockEventSource.instances.at(-1)
    if (latest === undefined) {
      throw new Error('no EventSource was created')
    }
    return latest
  }

  readonly url: string
  readyState = MockEventSource.CONNECTING

  constructor(url: string | URL) {
    super()
    this.url = String(url)
    MockEventSource.instances.push(this)
  }

  close() {
    this.readyState = MockEventSource.CLOSED
  }

  open() {
    this.readyState = MockEventSource.OPEN
    this.dispatchEvent(new Event('open'))
  }

  emit(type: string, data: unknown, id: string) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data), lastEventId: id }))
  }

  fail(readyState: number) {
    this.readyState = readyState
    this.dispatchEvent(new Event('error'))
  }
}
