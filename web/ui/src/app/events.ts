import { useQueryClient, type QueryClient, type QueryKey } from '@tanstack/react-query'
import { useEffect } from 'react'

import { applyJobEventToCleanup, checksQueryRoot, quarantineQueryRoot } from '@/api/cleanup'
import { applyJobEventToDuplicates, duplicatesQueryRoots } from '@/api/content'
import { applyJobEventToDates, datesQueryRoot } from '@/api/dates'
import { applyJobEventToEntries } from '@/api/entries'
import { applyJobEventToHome } from '@/api/home'
import type { JobEvent } from '@/api/jobs'
import { applyJobEventToHistory, historyQueryRoot } from '@/api/organize'
import { applyJobEventToSources, sourcesQueryKey } from '@/api/sources'
import { sessionQueryKey } from '@/app/session'

// liveQueryRoots are the cached responses that job events keep current:
// sources, everything built from the index and its duplicates (Home,
// entries, Search, Opportunities, and Compare), the history of organizing,
// the quarantine and its checks, and the media dates. They are fetched
// again whenever events may have been missed.
const liveQueryRoots: QueryKey[] = [
  sourcesQueryKey,
  ...duplicatesQueryRoots,
  historyQueryRoot,
  quarantineQueryRoot,
  checksQueryRoot,
  datesQueryRoot,
]

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
// live responses are then fetched again. On the stream's first connection
// only the responses older than the stream are (r2b design D9): those
// fetched since then already hold what happened before it, and fetching
// them again doubled every request of a page load.
export class JobEventStream {
  private readonly queryClient: QueryClient
  private source: EventSource | null = null
  private lastEventId = ''
  private retryDelay = minRetryDelay
  private retryTimer: number | undefined
  // startedAt is when start() was called, until the first connection takes
  // it.
  private startedAt: number | undefined

  constructor(queryClient: QueryClient) {
    this.queryClient = queryClient
  }

  start() {
    this.startedAt = Date.now()
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
    const startedAt = this.startedAt
    this.startedAt = undefined

    source.addEventListener('open', () => {
      this.retryDelay = minRetryDelay
      if (refreshOnOpen) {
        refreshOnOpen = false
        this.refreshLive(startedAt)
      }
    })
    source.addEventListener('job', (message: MessageEvent<string>) => {
      this.lastEventId = message.lastEventId
      const event = JSON.parse(message.data) as JobEvent
      applyJobEventToSources(this.queryClient, event)
      applyJobEventToHome(this.queryClient, event)
      applyJobEventToEntries(this.queryClient, event)
      applyJobEventToDuplicates(this.queryClient, event)
      applyJobEventToHistory(this.queryClient, event)
      applyJobEventToCleanup(this.queryClient, event)
      applyJobEventToDates(this.queryClient, event)
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

  // refreshLive fetches the live responses again: all of them, or with
  // olderThan only those last updated before it, without cancelling a fetch
  // already on its way.
  private refreshLive(olderThan?: number) {
    for (const queryKey of liveQueryRoots) {
      if (olderThan === undefined) {
        void this.queryClient.invalidateQueries({ queryKey })
      } else {
        void this.queryClient.invalidateQueries(
          { queryKey, predicate: (query) => query.state.dataUpdatedAt < olderThan },
          { cancelRefetch: false },
        )
      }
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
