<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'

import { ApiError } from '../api/client'
import type { SessionResourceSnapshot } from '../api/contracts'
import { monitorApi } from '../api/monitor'
import { formatCPU, formatMemory } from '../utils/resources'
import { loadResourceRefreshInterval, saveResourceRefreshInterval } from '../utils/resource-refresh'
import ResourceChart from './resource-chart.vue'
import ResourceRefreshControl from './resource-refresh-control.vue'

const props = defineProps<{
  sessionId: string
  getAccessToken: () => string | null
  refreshAccessToken: () => Promise<string | null | boolean>
}>()

interface Sample {
  cpu: number
  memory: number
}

const snapshot = ref<SessionResourceSnapshot | null>(null)
const samples = ref<Sample[]>([])
const error = ref('')
const loading = ref(true)
const refreshIntervalMs = ref(loadResourceRefreshInterval())
const lastRecordedMetricAt = ref('')
const cpuLimitNotice = ref('')
let poller: ReturnType<typeof globalThis.setInterval> | null = null
let cpuLimitNoticeTimer: ReturnType<typeof globalThis.setTimeout> | null = null
let requestActive = false
let mounted = false

const cpuSamples = computed(() => samples.value.map((sample) => sample.cpu))
const memorySamples = computed(() => samples.value.map((sample) => sample.memory))
const agent = computed(() =>
  snapshot.value?.pod.containers.find((container) => container.name === 'agent'),
)

function record(next: SessionResourceSnapshot): void {
  const previousAgent = snapshot.value?.pod.containers.find(
    (container) => container.name === 'agent',
  )
  const nextAgent = next.pod.containers.find((container) => container.name === 'agent')
  const previousLimit = previousAgent?.limits.cpuMillicores || 0
  const nextLimit = nextAgent?.limits.cpuMillicores || 0
  if (previousLimit && nextLimit && previousLimit !== nextLimit) {
    cpuLimitNotice.value = `CPU 分配已从 ${formatCPU(previousLimit)} 调整为 ${formatCPU(nextLimit)}`
    if (cpuLimitNoticeTimer) globalThis.clearTimeout(cpuLimitNoticeTimer)
    cpuLimitNoticeTimer = globalThis.setTimeout(() => {
      cpuLimitNotice.value = ''
      cpuLimitNoticeTimer = null
    }, 8000)
  }
  snapshot.value = next
  error.value = ''
  const metricObservedAt = next.pod.metricsObservedAt
  if (next.pod.usage && metricObservedAt && metricObservedAt !== lastRecordedMetricAt.value) {
    lastRecordedMetricAt.value = metricObservedAt
    samples.value = [
      ...samples.value,
      {
        cpu: next.pod.usage.cpuMillicores,
        memory: next.pod.usage.memoryBytes,
      },
    ].slice(-60)
  }
}

function metricSourceLabel(source: string | undefined): string {
  return source === 'kubelet-summary' ? 'Kubelet 秒级采样' : 'metrics-server'
}

async function fetchSnapshot(): Promise<void> {
  if (requestActive) return
  requestActive = true
  try {
    let token = props.getAccessToken()
    if (!token && (await props.refreshAccessToken())) token = props.getAccessToken()
    if (!token) throw new Error('missing access token')
    try {
      record(await monitorApi.session(token, props.sessionId))
    } catch (cause) {
      if (!(cause instanceof ApiError) || cause.status !== 401) throw cause
      if (!(await props.refreshAccessToken())) throw cause
      token = props.getAccessToken()
      if (!token) throw cause
      record(await monitorApi.session(token, props.sessionId))
    }
  } catch {
    error.value = '暂时无法获取资源用量。'
  } finally {
    requestActive = false
    loading.value = false
  }
}

function startPolling(): void {
  if (poller) globalThis.clearInterval(poller)
  poller = globalThis.setInterval(fetchSnapshot, refreshIntervalMs.value)
}

watch(refreshIntervalMs, (intervalMs) => {
  saveResourceRefreshInterval(intervalMs)
  if (mounted) startPolling()
})

onMounted(async () => {
  mounted = true
  await fetchSnapshot()
  if (mounted) startPolling()
})
onUnmounted(() => {
  mounted = false
  if (poller) globalThis.clearInterval(poller)
  if (cpuLimitNoticeTimer) globalThis.clearTimeout(cpuLimitNoticeTimer)
})
</script>

<template>
  <section class="resource-panel card" aria-label="会话资源用量">
    <Transition name="resource-notice">
      <div v-if="cpuLimitNotice" class="cpu-allocation-notice" role="status">
        <span class="live-dot" />
        <div>
          <strong>CPU 调度更新</strong>
          <span>{{ cpuLimitNotice }}</span>
        </div>
        <button type="button" aria-label="关闭 CPU 调度提示" @click="cpuLimitNotice = ''">×</button>
      </div>
    </Transition>
    <div class="resource-panel-heading">
      <div>
        <p class="eyebrow">LIVE RESOURCES</p>
        <h2>会话资源用量</h2>
      </div>
      <div class="resource-panel-meta">
        <span v-if="snapshot" class="sample-time">
          查询于 {{ new Date(snapshot.observedAt).toLocaleTimeString('zh-CN') }}
        </span>
        <span v-if="snapshot?.pod.metricsObservedAt" class="sample-time">
          指标采样于
          {{ new Date(snapshot.pod.metricsObservedAt).toLocaleTimeString('zh-CN') }}
          · {{ metricSourceLabel(snapshot.pod.metricsSource) }}
        </span>
        <ResourceRefreshControl v-model="refreshIntervalMs" />
      </div>
    </div>
    <p v-if="loading && !snapshot">正在读取资源数据…</p>
    <p v-else-if="error && !snapshot" class="alert" role="alert">{{ error }}</p>
    <template v-else-if="snapshot">
      <p v-if="!snapshot.metricsAvailable" class="metrics-note">
        Kubelet 与 metrics-server 暂无该 Pod 的采样，requests 与 limits 仍可查看。
      </p>
      <p class="resource-limit-summary">
        Agent 容器 Limit：
        <strong>{{ formatCPU(agent?.limits.cpuMillicores || 0) }}</strong>
        CPU /
        <strong>{{ formatMemory(agent?.limits.memoryBytes || 0) }}</strong>
        内存；图表中的 Limit 为包含平台 sidecar 的 Pod 总量。
      </p>
      <p v-if="snapshot.pod.resize" class="metrics-note" role="status">
        Kubernetes 正在应用资源调整：{{ snapshot.pod.resize.reason || snapshot.pod.resize.state }}
      </p>
      <div class="resource-chart-grid">
        <ResourceChart
          title="CPU"
          :current="snapshot.pod.usage?.cpuMillicores || 0"
          :request="snapshot.pod.requests.cpuMillicores"
          :limit="snapshot.pod.limits.cpuMillicores"
          :samples="cpuSamples"
          :formatter="formatCPU"
          :refresh-interval-ms="refreshIntervalMs"
        />
        <ResourceChart
          title="内存"
          :current="snapshot.pod.usage?.memoryBytes || 0"
          :request="snapshot.pod.requests.memoryBytes"
          :limit="snapshot.pod.limits.memoryBytes"
          :samples="memorySamples"
          :formatter="formatMemory"
          :refresh-interval-ms="refreshIntervalMs"
        />
      </div>
      <p v-if="error" class="metrics-note" role="status">{{ error }} 将继续自动重试。</p>
    </template>
  </section>
</template>
