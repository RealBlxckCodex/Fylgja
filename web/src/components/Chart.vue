<script setup lang="ts">
// ECharts-Wrapper (Zeitreihen, Balken, Topologie) mit Theme aus CSS-Variablen.
import { onMounted, onBeforeUnmount, ref, watch, shallowRef } from 'vue'
import * as echarts from 'echarts'
const props = defineProps<{ option: any; height?: string }>()
const el = ref<HTMLDivElement>()
const chart = shallowRef<echarts.ECharts>()
function css(v: string) { return getComputedStyle(document.documentElement).getPropertyValue(v).trim() }
function apply() {
  if (!chart.value) return
  const text = css('--muted'), line = css('--line')
  chart.value.setOption({
    backgroundColor: 'transparent',
    textStyle: { color: text, fontFamily: 'Inter, system-ui, sans-serif' },
    grid: { left: 48, right: 16, top: 24, bottom: 28 },
    color: ['#d4313f', '#5b8def', '#3fb68b', '#e6b422', '#a371f7', '#8b8b95'],
    tooltip: { trigger: 'axis', backgroundColor: css('--panel'), borderColor: line, textStyle: { color: css('--text') } },
    ...props.option,
    xAxis: props.option.xAxis ? { axisLine: { lineStyle: { color: line } }, splitLine: { lineStyle: { color: line } }, ...props.option.xAxis } : undefined,
    yAxis: props.option.yAxis ? { axisLine: { lineStyle: { color: line } }, splitLine: { lineStyle: { color: line } }, ...props.option.yAxis } : undefined,
  }, true)
}
let ro: ResizeObserver | undefined
onMounted(() => {
  chart.value = echarts.init(el.value!, undefined, { renderer: 'canvas' })
  apply()
  ro = new ResizeObserver(() => chart.value?.resize())
  ro.observe(el.value!)
})
watch(() => props.option, apply, { deep: true })
onBeforeUnmount(() => { ro?.disconnect(); chart.value?.dispose() })
</script>
<template>
  <div ref="el" :style="{ height: height ?? '220px', width: '100%' }" role="img" aria-label="Diagramm" />
</template>
