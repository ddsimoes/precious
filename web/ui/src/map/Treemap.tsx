import { useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { isDrillable, type EntryRow, type Treemap as TreemapData } from '@/api/entries'
import { Button } from '@/components/ui/button'
import { colorOf, legendOf, otherColor, type ColorContext, type ColorMode } from '@/map/colors'
import { init, type ECElementEvent, type ECharts, type TreemapOption } from '@/map/echarts'
import { useFormat } from '@/lib/format'

// otherId marks the area of the remaining children; entry IDs are numbers,
// so it never names an entry.
export const otherId = 'other'

interface TreemapProps {
  data: TreemapData
  folderName: string
  colorMode: ColorMode
  colorContext: ColorContext
  hoveredId: string | null
  selectedId: string | null
  onHover: (id: string | null) => void
  // onDrill opens a folder; onSelect opens an entry's detail panel; onOther
  // sends the owner to the table for the children too small to draw.
  onDrill: (id: string) => void
  onSelect: (id: string) => void
  onOther: () => void
}

interface Handlers {
  rows: Map<string, EntryRow>
  onHover: (id: string | null) => void
  onDrill: (id: string) => void
  onSelect: (id: string) => void
  onOther: () => void
}

// Treemap draws one folder level with ECharts: its largest children by
// bytes, and one area for the rest. Hovering an area reports it, so the
// table highlights the same row; clicking a folder drills into it, clicking
// a file opens its details, and clicking the rest leads to the table
// (r2b design D9). A list of the same areas gives screen readers and
// keyboards the same actions.
export function Treemap({
  data,
  folderName,
  colorMode,
  colorContext,
  hoveredId,
  selectedId,
  onHover,
  onDrill,
  onSelect,
  onOther,
}: TreemapProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const hostRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<ECharts | null>(null)
  const handlers = useRef<Handlers>({ rows: new Map(), onHover, onDrill, onSelect, onOther })

  const otherLabel = t('map.remaining', { count: data.other.count, formatted: fmt.count(data.other.count) })

  useEffect(() => {
    handlers.current = {
      rows: new Map(data.items.map((row) => [row.id, row])),
      onHover,
      onDrill,
      onSelect,
      onOther,
    }
  }, [data, onHover, onDrill, onSelect, onOther])

  useEffect(() => {
    const host = hostRef.current
    if (host === null) {
      return
    }
    const chart = init(host, undefined, { renderer: 'canvas' })
    chartRef.current = chart
    const idOf = (event: ECElementEvent) => (event.data as { id?: string } | null)?.id ?? null
    chart.on('mouseover', (event: ECElementEvent) => {
      const id = idOf(event)
      handlers.current.onHover(id === otherId ? null : id)
    })
    chart.on('mouseout', () => handlers.current.onHover(null))
    chart.on('click', (event: ECElementEvent) => {
      const id = idOf(event)
      if (id === otherId) {
        handlers.current.onOther()
        return
      }
      const row = handlers.current.rows.get(id ?? '')
      if (row === undefined) {
        return
      }
      if (isDrillable(row)) {
        handlers.current.onDrill(row.id)
      } else {
        handlers.current.onSelect(row.id)
      }
    })
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => chart.resize())
    observer?.observe(host)
    return () => {
      observer?.disconnect()
      chart.dispose()
      chartRef.current = null
    }
  }, [])

  const option = useMemo<TreemapOption>(() => {
    const items = data.items.map((row) => {
      const marked = row.id === selectedId || row.id === hoveredId
      return {
        id: row.id,
        name: row.name,
        value: row.total_bytes,
        itemStyle: {
          color: colorOf(row, colorMode, colorContext),
          borderColor: row.id === selectedId ? '#0f172a' : marked ? '#475569' : '#ffffff',
          borderWidth: marked ? 3 : 1,
        },
      }
    })
    if (data.other.count > 0) {
      items.push({
        id: otherId,
        name: otherLabel,
        value: data.other.bytes,
        itemStyle: { color: otherColor, borderColor: '#ffffff', borderWidth: 1 },
      })
    }
    return {
      animation: false,
      series: [
        {
          type: 'treemap',
          left: 0,
          top: 0,
          right: 0,
          bottom: 0,
          roam: false,
          nodeClick: false,
          breadcrumb: { show: false },
          label: { show: true, overflow: 'truncate', color: '#0f172a' },
          itemStyle: { gapWidth: 1 },
          data: items,
        },
      ],
    }
  }, [data, colorMode, colorContext, hoveredId, selectedId, otherLabel])

  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge: true })
  }, [option])

  const legend = legendOf(colorMode, t)

  return (
    <div className="grid content-start gap-2">
      <div
        ref={hostRef}
        role="img"
        aria-label={t('map.treemap', { name: folderName })}
        className="h-[60vh] min-h-64 w-full overflow-hidden rounded-lg border bg-card"
      />
      <ul aria-label={t('map.treemapAreas')} className="sr-only">
        {data.items.map((row) => (
          <li key={row.id}>
            <Button
              variant="ghost"
              onFocus={() => onHover(row.id)}
              onBlur={() => onHover(null)}
              onClick={() => (isDrillable(row) ? onDrill(row.id) : onSelect(row.id))}
            >
              {isDrillable(row)
                ? t('map.openFolder', { name: row.name })
                : t('map.showDetails', { name: row.name })}{' '}
              ({fmt.bytes(row.total_bytes)})
            </Button>
          </li>
        ))}
        {data.other.count > 0 && (
          <li>
            <Button variant="ghost" onClick={onOther}>
              {t('map.otherArea', { label: otherLabel, bytes: fmt.bytes(data.other.bytes) })}
            </Button>
          </li>
        )}
      </ul>
      <ul aria-label={t('map.legend')} className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {legend.map((item) => (
          <li key={item.key} className="flex items-center gap-1">
            <span aria-hidden="true" className="size-3 rounded-sm" style={{ backgroundColor: item.color }} />
            {item.label}
          </li>
        ))}
        {data.other.count > 0 && (
          <li className="flex items-center gap-1">
            <span aria-hidden="true" className="size-3 rounded-sm" style={{ backgroundColor: otherColor }} />
            {t('map.other')}
          </li>
        )}
      </ul>
    </div>
  )
}
