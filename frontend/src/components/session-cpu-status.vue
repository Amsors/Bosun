<script setup lang="ts">
import { computed } from 'vue'

import { percent } from '../utils/resources'

const props = defineProps<{
  usageMillicores: number
  limitMillicores: number
  direction: 'up' | 'down' | null
}>()

const utilization = computed(() => percent(props.usageMillicores, props.limitMillicores))
const formatCores = (millicores: number): string => `${(millicores / 1000).toFixed(2)} 核`
const stateLabel = computed(() => {
  if (props.direction === 'up') return '刚刚扩容'
  if (props.direction === 'down') return '刚刚缩容'
  if (utilization.value >= 75) return '高负载'
  if (utilization.value < 30) return '低负载'
  return '稳定'
})
</script>

<template>
  <section
    class="session-cpu-status"
    :class="{ 'limit-changing': direction }"
    :data-direction="direction || undefined"
    aria-label="CPU 实时状态"
  >
    <div class="session-cpu-heading">
      <span>CPU 实时调度</span>
      <strong>{{ formatCores(usageMillicores) }} / {{ formatCores(limitMillicores) }}</strong>
      <span class="cpu-state" :data-direction="direction || undefined">
        <template v-if="direction === 'up'">↑</template>
        <template v-else-if="direction === 'down'">↓</template>
        {{ stateLabel }}
      </span>
    </div>
    <div class="session-cpu-track" aria-hidden="true">
      <span :style="{ width: `${utilization}%` }" />
      <i :style="{ left: `${utilization}%` }" />
    </div>
    <div class="session-cpu-scale">
      <span>使用率 {{ Math.round(utilization) }}%</span>
      <span>Limit {{ formatCores(limitMillicores) }}</span>
    </div>
  </section>
</template>
