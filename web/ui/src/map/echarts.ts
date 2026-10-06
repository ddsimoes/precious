import { TreemapChart, type TreemapSeriesOption } from 'echarts/charts'
import { init, use as register, type ComposeOption, type ECElementEvent, type ECharts } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'

// The Map's ECharts build: only the treemap chart and the canvas renderer
// (design D14). Neither adds a <style> element or inline markup, so the
// strict CSP holds; the tooltip component, which writes styled HTML, is
// deliberately left out.
register([TreemapChart, CanvasRenderer])

export type TreemapOption = ComposeOption<TreemapSeriesOption>
export type { ECElementEvent, ECharts }
export { init }
