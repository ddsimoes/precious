import { useQueryClient, type QueryClient, type QueryKey } from '@tanstack/react-query'
import { useEffect } from 'react'

import { applyJobEventToDuplicates, duplicatesQueryRoots } from '@/api/content'
import { applyJobEventToEntries } from '@/api/entries'
import { applyJobEventToHome } from '@/api/home'
import type { JobEvent } from '@/api/jobs'
import { applyJobEventToSources, sourcesQueryKey } from '@/api/sources'
import { sessionQueryKey } from '@/app/session'

// liveQueryRoots are the cached responses that job events keep current:
// sources, and everything built from the index and its duplicates (Home,
// entries, Search, Opportunities, Gems, and Compare). They are fetched again
// whenever events may have been missed.
const liveQueryRoots: QueryKey[] = [sourcesQueryKey, ...duplicatesQueryRoots]

const minRetryDelay = 1_000
const maxRetryDelay = 30_000

// JobEventStream follows GET /api/events and applies each job event to the
// query cache.
//
// While a connection drops and comes back, the browser resumes it by itself
// with the Last-Event-ID header. When the browser gives up (the server
// answered with an error, or was unreachable), the stream reconnects after a
// growing delay with ?last_event_id= set to the last event received, so no
// event is lost. A `reset` event (that position is no longer kept), or a
// connection that starts at the newest event, may have skipped events: the
// live responses are then fetched again.
export class JobEventStream {
  private readonly queryClient: QueryClient
  private source: EventSource | null = null
  private lastEventId = ''
  private retryDelay = minRetryDelay
  private retryTimer: number | undefined

  constructor(queryClient: QueryClient) {
    this.queryClient = queryClient
  }

  start() {
    this.connect()
  }

  stop() {
    window.clearTimeout(this.retryTimer)
    this.retryTimer = undefined
    this.source?.close()
    this.source = null
  }

  private connect() {
    const resuming = this.lastEventId !== ''
    const url = resuming
      ? `/api/events?${new URLSearchParams({ last_event_id: this.lastEventId })}`
      : '/api/events'
    const source = new EventSource(url)
    this.source = source
    let refreshOnOpen = !resuming

    source.addEventListener('open', () => {
      this.retryDelay = minRetryDelay
      if (refreshOnOpen) {
        refreshOnOpen = false
        this.refreshLive()
      }
    })
    source.addEventListener('job', (message: MessageEvent<string>) => {
      this.lastEventId = message.lastEventId
      const event = JSON.parse(message.data) as JobEvent
      applyJobEventToSources(this.queryClient, event)
      applyJobEventToHome(this.queryClient, event)
      applyJobEventToEntries(this.queryClient, event)
      applyJobEventToDuplicates(this.queryClient, event)
    })
    source.addEventListener('reset', (message: MessageEvent<string>) => {
      this.lastEventId = message.lastEventId
      this.refreshLive()
    })
    source.addEventListener('error', () => {
      if (source.readyState !== EventSource.CLOSED || this.source !== source) {
        return
      }
      this.source = null
      // A stream refused because the session ended shows only as an error
      // here; the session check then sends AppLayout to /login, which stops
      // this stream.
      void this.queryClient.invalidateQueries({ queryKey: sessionQueryKey })
      this.retryTimer = window.setTimeout(() => this.connect(), this.retryDelay)
      this.retryDelay = Math.min(this.retryDelay * 2, maxRetryDelay)
    })
  }

  private refreshLive() {
    for (const queryKey of liveQueryRoots) {
      void this.queryClient.invalidateQueries({ queryKey })
    }
  }
}

// useJobEvents keeps one event stream open while the calling component is
// mounted.
export function useJobEvents() {
  const queryClient = useQueryClient()
  useEffect(() => {
    const stream = new JobEventStream(queryClient)
    stream.start()
    return () => stream.stop()
  }, [queryClient])
}
