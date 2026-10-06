import { afterEach, describe, expect, it, vi } from 'vitest'

import { init } from '@/map/echarts'

// fakeContext stands in for a 2D canvas context, which jsdom lacks: every
// method does nothing, and text measures 7 pixels per character.
function fakeContext(): CanvasRenderingContext2D {
  const target: Record<string | symbol, unknown> = {
    measureText: (text: string) => ({ width: text.length * 7 }),
    getImageData: () => ({ data: new Uint8ClampedArray(4) }),
    createLinearGradient: () => ({ addColorStop: () => undefined }),
    createRadialGradient: () => ({ addColorStop: () => undefined }),
    createPattern: () => null,
  }
  return new Proxy(target, {
    get: (object, key) => (key in object ? object[key] : () => undefined),
    set: (object, key, value) => {
      object[key] = value
      return true
    },
  }) as unknown as CanvasRenderingContext2D
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ECharts build', () => {
  it('draws a treemap without adding style elements or inline markup', () => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(
      () => fakeContext() as RenderingContext,
    )
    const added: string[] = []
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          added.push(node.nodeName)
        }
      }
    })
    observer.observe(document, { childList: true, subtree: true })

    const host = document.createElement('div')
    document.body.append(host)
    const chart = init(host, undefined, { renderer: 'canvas', width: 400, height: 300 })
    chart.setOption({
      series: [
        {
          type: 'treemap',
          roam: false,
          nodeClick: false,
          breadcrumb: { show: false },
          data: [
            { id: '1', name: '<img src=x onerror=alert(1)>', value: 300 },
            { id: '2', name: 'Fotos', value: 200 },
          ],
        },
      ],
    })
    chart.resize({ width: 500, height: 300 })
    chart.dispose()
    observer.disconnect()

    expect(document.querySelectorAll('style')).toHaveLength(0)
    expect(added).not.toContain('STYLE')
    expect(added).not.toContain('IMG')
    expect(document.body.innerHTML).not.toContain('onerror')
    host.remove()
  })
})
