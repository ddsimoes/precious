import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { homeQueryKey, type Home } from '@/api/home'
import { sourcesQueryKey, type SourcesResponse } from '@/api/sources'
import { JobEventStream } from '@/app/events'
import { sessionQueryKey } from '@/app/session'
import { MockEventSource } from '@/test/eventSource'
import { fotosSource, homeResponse, scanEvent, usbSource } from '@/test/fixtures'

function seededClient() {
  const queryClient = new QueryClient()
  queryClient.setQueryData<SourcesResponse>(sourcesQueryKey, { sources: [fotosSource(), usbSource()] })
  queryClient.setQueryData<Home>(homeQueryKey(null), homeResponse())
  queryClient.setQueryData<Home>(homeQueryKey('old-disk'), homeResponse())
  queryClient.setQueryData(sessionQueryKey, { authenticated: true, csrf_token: 't', admin_exists: true })
  return queryClient
}

function invalidated(queryClient: QueryClient, key: readonly unknown[]) {
  return queryClient.getQueryState(key)?.isInvalidated
}

describe('job event stream', () => {
  let stream: JobEventStream | undefined

  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    stream?.stop()
    vi.useRealTimers()
  })

  it('applies scan progress to the sources and Home caches', () => {
    const queryClient = seededClient()
    stream = new JobEventStream(queryClient)
    stream.start()

    const source = MockEventSource.latest()
    expect(source.url).toBe('/api/events')
    source.emit('job', scanEvent(), '7')

    const fotos = queryClient.getQueryData<SourcesResponse>(sourcesQueryKey)?.sources[0]
    expect(fotos?.active_job).toEqual({
      job_id: '42',
      state: 'running',
      progress: { phase: 1, dirs: 10, files: 100, bytes: 1024 },
    })
    expect(queryClient.getQueryData<Home>(homeQueryKey(null))?.scans).toEqual([
      { source_id: 'fotos', job_id: '42', state: 'running', progress: { phase: 1, dirs: 10, files: 100, bytes: 1024 } },
    ])
    // Home filtered to another source does not show this scan.
    expect(queryClient.getQueryData<Home>(homeQueryKey('old-disk'))?.scans).toEqual([])

    source.emit('job', scanEvent({ cancel_requested: true, progress: { dirs: 20 } }), '8')
    expect(queryClient.getQueryData<SourcesResponse>(sourcesQueryKey)?.sources[0]?.active_job).toEqual({
      job_id: '42',
      state: 'cancel_requested',
      progress: { dirs: 20 },
    })
  })

  it('clears a finished scan and refetches the figures it changed', () => {
    const queryClient = seededClient()
    stream = new JobEventStream(queryClient)
    stream.start()
    const source = MockEventSource.latest()
    source.emit('job', scanEvent(), '7')

    source.emit('job', scanEvent({ state: 'succeeded' }), '8')

    expect(queryClient.getQueryData<SourcesResponse>(sourcesQueryKey)?.sources[0]?.active_job).toBeNull()
    expect(queryClient.getQueryData<Home>(homeQueryKey(null))?.scans).toEqual([])
    expect(invalidated(queryClient, sourcesQueryKey)).toBe(true)
    expect(invalidated(queryClient, homeQueryKey(null))).toBe(true)
  })

  it('reconnects from the last event after the browser gives up', () => {
    const queryClient = seededClient()
    stream = new JobEventStream(queryClient)
    stream.start()
    const first = MockEventSource.latest()
    first.open()
    first.emit('job', scanEvent(), '7')

    // A dropped connection the browser retries by itself (with
    // Last-Event-ID) needs nothing from the stream.
    first.fail(MockEventSource.CONNECTING)
    vi.advanceTimersByTime(60_000)
    expect(MockEventSource.instances).toHaveLength(1)

    // Once the browser gives up, the stream checks the session and reconnects
    // after a delay, resuming after event 7.
    first.fail(MockEventSource.CLOSED)
    expect(invalidated(queryClient, sessionQueryKey)).toBe(true)
    vi.advanceTimersByTime(999)
    expect(MockEventSource.instances).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(MockEventSource.instances).toHaveLength(2)
    const second = MockEventSource.latest()
    expect(second.url).toBe('/api/events?last_event_id=7')

    // A resumed stream replays what was missed: no snapshot refetch.
    queryClient.setQueryData<SourcesResponse>(sourcesQueryKey, { sources: [fotosSource()] })
    second.open()
    expect(invalidated(queryClient, sourcesQueryKey)).toBe(false)
    second.emit('job', scanEvent({ progress: { dirs: 30 } }), '9')
    expect(queryClient.getQueryData<SourcesResponse>(sourcesQueryKey)?.sources[0]?.active_job?.progress).toEqual({
      dirs: 30,
    })

    // The next drop resumes after event 9.
    second.fail(MockEventSource.CLOSED)
    vi.advanceTimersByTime(1_000)
    expect(MockEventSource.latest().url).toBe('/api/events?last_event_id=9')
  })

  it('waits longer after each failed attempt, up to 30 seconds', () => {
    stream = new JobEventStream(seededClient())
    stream.start()
    const delays: number[] = []
    for (let attempt = 0; attempt < 7; attempt++) {
      const before = MockEventSource.instances.length
      MockEventSource.latest().fail(MockEventSource.CLOSED)
      let waited = 0
      while (MockEventSource.instances.length === before) {
        vi.advanceTimersByTime(1_000)
        waited += 1_000
      }
      delays.push(waited)
    }
    expect(delays).toEqual([1_000, 2_000, 4_000, 8_000, 16_000, 30_000, 30_000])

    // A connection that opens resets the delay.
    MockEventSource.latest().open()
    MockEventSource.latest().fail(MockEventSource.CLOSED)
    vi.advanceTimersByTime(1_000)
    expect(MockEventSource.instances).toHaveLength(9)
  })

  it('refetches the live figures on reset and on a fresh start', () => {
    const queryClient = seededClient()
    stream = new JobEventStream(queryClient)
    stream.start()
    const source = MockEventSource.latest()

    // A stream without a position starts at the newest event: what happened
    // before it opened is read again.
    source.open()
    expect(invalidated(queryClient, sourcesQueryKey)).toBe(true)
    expect(invalidated(queryClient, homeQueryKey(null))).toBe(true)
    expect(invalidated(queryClient, sessionQueryKey)).toBe(false)

    queryClient.setQueryData<SourcesResponse>(sourcesQueryKey, { sources: [fotosSource()] })
    source.emit('reset', {}, '120')
    expect(invalidated(queryClient, sourcesQueryKey)).toBe(true)

    // The reset's id is the new position.
    source.fail(MockEventSource.CLOSED)
    vi.advanceTimersByTime(1_000)
    expect(MockEventSource.latest().url).toBe('/api/events?last_event_id=120')
  })

  it('closes the stream and any pending reconnect when stopped', () => {
    stream = new JobEventStream(seededClient())
    stream.start()
    const source = MockEventSource.latest()
    stream.stop()
    expect(source.readyState).toBe(MockEventSource.CLOSED)

    stream.start()
    MockEventSource.latest().fail(MockEventSource.CLOSED)
    stream.stop()
    vi.advanceTimersByTime(60_000)
    expect(MockEventSource.instances).toHaveLength(2)
  })
})
