<script setup lang="ts">
import { formatResourceRefreshInterval, resourceRefreshIntervals } from '../utils/resource-refresh'

const model = defineModel<number>({ required: true })
</script>

<template>
  <label class="refresh-control">
    <span>刷新周期</span>
    <select v-model.number="model" aria-label="资源数据刷新周期">
      <option v-for="interval in resourceRefreshIntervals" :key="interval" :value="interval">
        {{ formatResourceRefreshInterval(interval) }}
      </option>
    </select>
  </label>
</template>
