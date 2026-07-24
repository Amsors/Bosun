<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  title: string
  current: number
  request: number
  limit: number
  samples: number[]
  formatter: (value: number) => string
}>()

const ceiling = computed(
  () => Math.max(1, props.limit, props.request, props.current, ...props.samples) * 1.1,
)
const points = computed(() => {
  if (!props.samples.length) return ''
  const denominator = Math.max(1, props.samples.length - 1)
  return props.samples
    .map((value, index) => {
      const x = (index / denominator) * 100
      const y = 38 - (Math.min(value, ceiling.value) / ceiling.value) * 34
      return `${x.toFixed(2)},${y.toFixed(2)}`
    })
    .join(' ')
})
const limitY = computed(() => 38 - (Math.min(props.limit, ceiling.value) / ceiling.value) * 34)
const requestY = computed(() => 38 - (Math.min(props.request, ceiling.value) / ceiling.value) * 34)
</script>

<template>
  <article class="resource-chart">
    <header>
      <div>
        <span>{{ title }}</span>
        <strong>{{ formatter(current) }}</strong>
      </div>
      <dl>
        <div>
          <dt>Request</dt>
          <dd>{{ formatter(request) }}</dd>
        </div>
        <div>
          <dt>Limit</dt>
          <dd>{{ limit ? formatter(limit) : '未设置' }}</dd>
        </div>
      </dl>
    </header>
    <svg
      viewBox="0 0 100 40"
      preserveAspectRatio="none"
      role="img"
      :aria-label="`${title} 用量趋势`"
    >
      <line class="chart-grid" x1="0" y1="38" x2="100" y2="38" />
      <line v-if="request" class="chart-request" x1="0" :y1="requestY" x2="100" :y2="requestY" />
      <line v-if="limit" class="chart-limit" x1="0" :y1="limitY" x2="100" :y2="limitY" />
      <polyline v-if="points" class="chart-area" :points="`0,38 ${points} 100,38`" />
      <polyline v-if="points" class="chart-line" :points="points" />
    </svg>
    <footer>
      <span>最近 {{ samples.length }} 个采样点</span><span>每 5 秒刷新</span>
    </footer>
  </article>
</template>
