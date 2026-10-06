import type { ECElementEvent, TreemapOption } from '@/map/echarts'

type Handler = (event: ECElementEvent) => void

// Tile is one area of the treemap data the Map sets.
export interface Tile {
  id: string
  name: string
  value: number
  itemStyle: { color: string; borderColor: string; borderWidth: number }
}

// FakeChart stands in for an ECharts instance, which needs a canvas jsdom
// lacks. Tests mock '@/map/echarts' so init returns one; it records each
// option set, and emit() delivers a chart event as a pointer would.
export class FakeChart {
  static instances: FakeChart[] = []

  static latest(): FakeChart {
    const latest = FakeChart.instances.at(-1)
    if (latest === undefined) {
      throw new Error('no chart was created')
    }
    return latest
  }

  readonly options: TreemapOption[] = []
  disposed = false
  private readonly handlers = new Map<string, Handler[]>()

  constructor() {
    FakeChart.instances.push(this)
  }

  setOption(option: TreemapOption) {
    this.options.push(option)
  }

  on(name: string, handler: Handler) {
    this.handlers.set(name, [...(this.handlers.get(name) ?? []), handler])
  }

  resize() {}

  dispose() {
    this.disposed = true
  }

  // data is the latest option's treemap data.
  data(): Tile[] {
    const series = this.options.at(-1)?.series
    const first = Array.isArray(series) ? series[0] : series
    return (first?.data ?? []) as Tile[]
  }

  emit(name: string, id: string | null) {
    const event = { data: id === null ? null : { id } } as unknown as ECElementEvent
    for (const handler of this.handlers.get(name) ?? []) {
      handler(event)
    }
  }
}
