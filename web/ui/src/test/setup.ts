import '@testing-library/jest-dom/vitest'
import '@/i18n'

import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'

import { chooseCompareFirst } from '@/compare/compareChoice'
import { MockEventSource } from '@/test/eventSource'

// jsdom has no modal dialogs: showModal opens the dialog in place.
if (typeof HTMLDialogElement.prototype.showModal !== 'function') {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute('open', '')
  }
}

beforeEach(() => {
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  // "Compare with…" remembers its first side in session storage.
  chooseCompareFirst(null)
})

// jsdom lays nothing out. Virtualized lists read their scroll container's
// size from offsetWidth and offsetHeight, and scroll with scrollTo: every
// element measures as a 1000×640 box, and scrollTo moves scrollTop.
Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get: () => 1000 })
Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get: () => 640 })
if (typeof Element.prototype.scrollTo !== 'function') {
  Element.prototype.scrollTo = function scrollTo(this: Element, options?: ScrollToOptions | number) {
    if (typeof options === 'object' && options.top !== undefined) {
      this.scrollTop = options.top
    }
  } as Element['scrollTo']
}
