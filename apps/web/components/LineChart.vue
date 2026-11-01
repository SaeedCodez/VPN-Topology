<script setup lang="ts">
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, MarkLineComponent } from 'echarts/components'
import { SVGRenderer } from 'echarts/renderers'
import type { Series } from '#contract'

echarts.use([LineChart, GridComponent, TooltipComponent, MarkLineComponent, SVGRenderer])

const props = defineProps<{ series: Series[]; unit: string; height?: number; marker?: number | null }>()
const el = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
const { effective } = useTheme()

function fmt(v: number) {
  return fmtMetric(v, props.unit === 'users' || props.unit === 'conns' ? '' : props.unit)
}

function render() {
  if (!el.value) return
  chart ??= echarts.init(el.value, undefined, { renderer: 'svg' })
  const palette = [rgbVar('fg'), rgbVar('fg-3'), rgbVar('accent')]
  chart.setOption(
    {
      animation: false,
      grid: { left: 52, right: 10, top: 10, bottom: 22 },
      tooltip: {
        trigger: 'axis',
        backgroundColor: rgbVar('surface'),
        borderColor: rgbVar('line'),
        textStyle: { color: rgbVar('fg'), fontFamily: 'Geist', fontSize: 12 },
        valueFormatter: (v: any) => (v == null ? '–' : fmt(Number(v))),
      },
      xAxis: {
        type: 'time',
        axisLine: { lineStyle: { color: rgbVar('line') } },
        axisTick: { show: false },
        splitLine: { show: false },
        axisLabel: { color: rgbVar('fg-3'), fontSize: 11, fontFamily: 'Geist', formatter: (v: number) => fmtTime(v).slice(0, 5), hideOverlap: true },
      },
      yAxis: {
        type: 'value',
        splitNumber: 3,
        axisLabel: { color: rgbVar('fg-3'), fontSize: 11, fontFamily: 'Geist Mono', formatter: (v: number) => fmt(v) },
        splitLine: { lineStyle: { color: rgbVar('line'), type: 'dashed' } },
      },
      series: props.series.map((s, i) => ({
        name: s.label,
        type: 'line',
        showSymbol: false,
        connectNulls: false,
        lineStyle: { width: 1.5, color: palette[i % palette.length] },
        itemStyle: { color: palette[i % palette.length] },
        areaStyle: i === 0 ? { color: rgbVar('fg', 0.06) } : undefined,
        data: s.points,
        markLine: i === 0 && props.marker ? { symbol: 'none', silent: true, lineStyle: { color: rgbVar('accent'), type: 'solid' }, label: { show: false }, data: [{ xAxis: props.marker }] } : undefined,
      })),
    },
    true,
  )
}

onMounted(render)
watch(() => [props.series, props.marker, effective.value], () => nextTick(render), { deep: false })
onBeforeUnmount(() => chart?.dispose())
useResizeObserver(el, () => chart?.resize())

function useResizeObserver(target: Ref<HTMLElement | undefined>, cb: () => void) {
  let ro: ResizeObserver | null = null
  onMounted(() => {
    ro = new ResizeObserver(cb)
    if (target.value) ro.observe(target.value)
  })
  onBeforeUnmount(() => ro?.disconnect())
}
</script>

<template>
  <div ref="el" :style="{ height: `${height ?? 120}px` }" class="w-full" />
</template>
