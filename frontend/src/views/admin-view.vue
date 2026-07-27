<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'

import type {
  ClusterResourceSnapshot,
  ContainerResourceSnapshot,
  PodResourceSnapshot,
  ResourceLoadClass,
} from '../api/contracts'
import { monitorApi } from '../api/monitor'
import AppShell from '../components/app-shell.vue'
import ResourceRefreshControl from '../components/resource-refresh-control.vue'
import StatusPanel from '../components/status-panel.vue'
import { formatCPU, formatMemory, percent } from '../utils/resources'
import { loadResourceRefreshInterval, saveResourceRefreshInterval } from '../utils/resource-refresh'

const snapshot = ref<ClusterResourceSnapshot | null>(null)
const loading = ref(true)
const error = ref('')
const search = ref('')
const showKubeSystem = ref(false)
const showCertManager = ref(false)
const agentOnly = ref(false)
const refreshIntervalMs = ref(loadResourceRefreshInterval())
let poller: ReturnType<typeof globalThis.setInterval> | null = null
let requestActive = false
let mounted = false

const visiblePods = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  return (snapshot.value?.pods || []).filter((pod) => {
    if (!showKubeSystem.value && pod.namespace === 'kube-system') return false
    if (!showCertManager.value && pod.namespace === 'cert-manager') return false
    if (agentOnly.value && !pod.isAgent) return false
    if (!query) return true
    return [pod.namespace, pod.name, pod.nodeName, pod.username, pod.sessionName]
      .filter(Boolean)
      .some((value) => value!.toLocaleLowerCase().includes(query))
  })
})
const agentCount = computed(() => snapshot.value?.pods.filter((pod) => pod.isAgent).length || 0)
const totalAgentCPULimit = computed(() =>
  (snapshot.value?.pods || [])
    .filter((pod) => pod.isAgent)
    .reduce((total, pod) => total + (agentContainer(pod)?.limits.cpuMillicores || 0), 0),
)
const totalAgentCPUUsage = computed(() =>
  (snapshot.value?.pods || [])
    .filter((pod) => pod.isAgent)
    .reduce((total, pod) => total + (agentContainer(pod)?.usage?.cpuMillicores || 0), 0),
)

async function load(): Promise<void> {
  if (requestActive) return
  requestActive = true
  try {
    const next = await monitorApi.cluster()
    snapshot.value = next
    error.value = ''
  } catch {
    error.value = '无法读取集群资源信息。'
  } finally {
    requestActive = false
    loading.value = false
  }
}

function agentContainer(pod: PodResourceSnapshot): ContainerResourceSnapshot | undefined {
  return pod.containers.find((container) => container.name === 'agent')
}

function loadClassLabel(loadClass: ResourceLoadClass): string {
  const labels: Record<ResourceLoadClass, string> = {
    Unknown: '指标未知',
    WarmingUp: '预热中',
    CPUHigh: 'CPU 高负载',
    CPULow: 'CPU 低负载',
    Stable: 'CPU 稳定',
  }
  return labels[loadClass]
}

function formatAppliedAt(timestamp: string): string {
  return new Date(timestamp).toLocaleTimeString('zh-CN')
}

function metricSourceLabel(source: string | undefined): string {
  return source === 'kubelet-summary' ? '秒级' : 'metrics-server'
}

function startPolling(): void {
  if (poller) globalThis.clearInterval(poller)
  poller = globalThis.setInterval(load, refreshIntervalMs.value)
}

watch(refreshIntervalMs, (intervalMs) => {
  saveResourceRefreshInterval(intervalMs)
  if (mounted) startPolling()
})

onMounted(async () => {
  mounted = true
  await load()
  if (mounted) startPolling()
})
onUnmounted(() => {
  mounted = false
  if (poller) globalThis.clearInterval(poller)
})
</script>

<template>
  <AppShell>
    <div class="page-heading admin-heading">
      <div>
        <p class="eyebrow">CLUSTER OVERVIEW</p>
        <h1>全局资源监控</h1>
        <p>Node、Pod 与 Agent 会话的实时 Kubernetes 资源快照。</p>
      </div>
      <div v-if="snapshot" class="refresh-indicator">
        <span class="live-dot" />
        <ResourceRefreshControl v-model="refreshIntervalMs" />
        <small>{{ new Date(snapshot.observedAt).toLocaleTimeString('zh-CN') }}</small>
      </div>
    </div>

    <StatusPanel v-if="loading && !snapshot" kind="loading" message="正在读取集群状态…" />
    <StatusPanel v-else-if="error && !snapshot" kind="error" :message="error">
      <button @click="load">重试</button>
    </StatusPanel>
    <template v-else-if="snapshot">
      <p v-if="error" class="alert" role="alert">{{ error }} 页面将继续自动重试。</p>
      <p
        v-if="!snapshot.nodeMetricsAvailable || !snapshot.podMetricsAvailable"
        class="metrics-note"
      >
        metrics-server 暂时不可用；Node / Pod 状态与资源 limits 仍会正常刷新。
      </p>

      <section class="admin-summary" aria-label="集群摘要">
        <div>
          <span>Nodes</span><strong>{{ snapshot.nodes.length }}</strong>
        </div>
        <div>
          <span>Pods</span><strong>{{ snapshot.pods.length }}</strong>
        </div>
        <div>
          <span>Agent Pods</span><strong>{{ agentCount }}</strong>
        </div>
        <div class="cpu-limit-summary">
          <span>Agent CPU 已分配</span>
          <strong>{{ formatCPU(totalAgentCPULimit) }}</strong>
          <small>当前使用 {{ formatCPU(totalAgentCPUUsage) }}</small>
        </div>
        <div>
          <span>当前显示</span><strong>{{ visiblePods.length }}</strong>
        </div>
      </section>

      <section aria-labelledby="nodes-heading">
        <div class="section-heading">
          <div>
            <p class="eyebrow">NODES</p>
            <h2 id="nodes-heading">集群节点</h2>
          </div>
        </div>
        <div class="node-grid">
          <article v-for="node in snapshot.nodes" :key="node.name" class="node-card card">
            <header>
              <div>
                <strong>{{ node.name }}</strong>
                <span>{{ node.roles.join(', ') || '未标记角色' }}</span>
              </div>
              <span class="phase" :data-phase="node.status">{{ node.status }}</span>
            </header>
            <div class="node-resource">
              <div>
                <span>CPU</span>
                <strong>
                  {{ node.usage ? formatCPU(node.usage.cpuMillicores) : '暂无指标' }}
                  / {{ formatCPU(node.allocatable.cpuMillicores) }}
                </strong>
              </div>
              <div class="usage-track">
                <span
                  :style="{
                    width: `${percent(node.usage?.cpuMillicores || 0, node.allocatable.cpuMillicores)}%`,
                  }"
                />
              </div>
            </div>
            <div class="node-resource">
              <div>
                <span>内存</span>
                <strong>
                  {{ node.usage ? formatMemory(node.usage.memoryBytes) : '暂无指标' }}
                  / {{ formatMemory(node.allocatable.memoryBytes) }}
                </strong>
              </div>
              <div class="usage-track memory">
                <span
                  :style="{
                    width: `${percent(node.usage?.memoryBytes || 0, node.allocatable.memoryBytes)}%`,
                  }"
                />
              </div>
            </div>
            <footer>{{ node.kubeletVersion || 'Kubelet 版本未知' }}</footer>
          </article>
        </div>
      </section>

      <section class="pods-section" aria-labelledby="pods-heading">
        <div class="section-heading pods-heading">
          <div>
            <p class="eyebrow">PODS</p>
            <h2 id="pods-heading">工作负载</h2>
          </div>
          <label class="pod-search">
            <span>搜索</span>
            <input v-model="search" placeholder="Pod、namespace、node 或用户名" />
          </label>
        </div>
        <div class="filter-row" aria-label="Pod 过滤">
          <label><input v-model="showKubeSystem" type="checkbox" />显示 kube-system</label>
          <label><input v-model="showCertManager" type="checkbox" />显示 cert-manager</label>
          <label><input v-model="agentOnly" type="checkbox" />仅显示 Agent Pod</label>
        </div>
        <div class="pod-table-shell card">
          <table class="pod-table">
            <thead>
              <tr>
                <th>Pod</th>
                <th>状态 / Node</th>
                <th>CPU 用量 / Limit</th>
                <th>内存用量 / Limit</th>
                <th>Agent 用户</th>
                <th>Agent Limit 调整</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="pod in visiblePods" :key="`${pod.namespace}/${pod.name}`">
                <td>
                  <strong>{{ pod.name }}</strong>
                  <span>{{ pod.namespace }}</span>
                </td>
                <td>
                  <span class="phase" :data-phase="pod.phase">{{ pod.phase || 'Unknown' }}</span>
                  <span>
                    {{ pod.nodeName || '尚未调度' }} · {{ pod.ready ? '已就绪' : '未就绪' }} · 重启
                    {{ pod.restarts }}
                  </span>
                </td>
                <td>
                  <strong>{{ pod.usage ? formatCPU(pod.usage.cpuMillicores) : '—' }}</strong>
                  <span class="pod-cpu-limit"
                    >/
                    {{
                      pod.limits.cpuMillicores ? formatCPU(pod.limits.cpuMillicores) : '未设置'
                    }}</span
                  >
                  <span v-if="pod.metricsObservedAt">
                    {{ metricSourceLabel(pod.metricsSource) }} ·
                    {{ new Date(pod.metricsObservedAt).toLocaleTimeString('zh-CN') }}
                  </span>
                </td>
                <td>
                  <strong>{{ pod.usage ? formatMemory(pod.usage.memoryBytes) : '—' }}</strong>
                  <span
                    >/
                    {{
                      pod.limits.memoryBytes ? formatMemory(pod.limits.memoryBytes) : '未设置'
                    }}</span
                  >
                </td>
                <td>
                  <template v-if="pod.isAgent">
                    <span class="agent-badge">AGENT</span>
                    <span class="agent-badge">公平调度</span>
                    <strong>{{ pod.username || '未知用户' }}</strong>
                    <span>{{ pod.sessionName || pod.sessionID }}</span>
                    <span v-if="pod.resourceScaling?.loadClass">
                      {{ loadClassLabel(pod.resourceScaling.loadClass) }}
                    </span>
                  </template>
                  <span v-else>—</span>
                </td>
                <td>
                  <div v-if="pod.isAgent && pod.resourceScaling" class="resize-form">
                    <span v-if="pod.resourceScaling" class="resize-state">
                      Desired:
                      {{ formatCPU(pod.resourceScaling.desiredResources.cpuMillicores) }} /
                      {{ formatMemory(pod.resourceScaling.desiredResources.memoryBytes) }}
                    </span>
                    <span v-if="pod.resourceScaling.recommendedCPUMillicores" class="resize-state">
                      CPU 推荐:
                      {{ formatCPU(pod.resourceScaling.recommendedCPUMillicores) }}
                    </span>
                    <span v-if="pod.resourceScaling?.lastAppliedAt" class="resize-state">
                      最近应用:
                      {{ formatAppliedAt(pod.resourceScaling.lastAppliedAt) }}
                    </span>
                    <span v-if="pod.resourceScaling?.actualResources" class="resize-state">
                      Actual:
                      {{ formatCPU(pod.resourceScaling.actualResources.cpuMillicores) }} /
                      {{ formatMemory(pod.resourceScaling.actualResources.memoryBytes) }}
                    </span>
                    <span
                      v-else-if="
                        pod.resourceScaling && !pod.resourceScaling.actualResourcesAvailable
                      "
                      class="resize-state"
                    >
                      Actual resources 暂不可用
                    </span>
                    <span v-if="pod.resize" class="resize-state">
                      {{ pod.resize.reason || 'Kubernetes 正在应用新 Limit' }}
                    </span>
                    <span v-if="pod.resourceScaling?.lastError" class="resize-error" role="alert">
                      {{ pod.resourceScaling.lastError }}
                    </span>
                  </div>
                  <span v-else>—</span>
                </td>
              </tr>
              <tr v-if="!visiblePods.length">
                <td colspan="6" class="empty-cell">没有符合当前过滤条件的 Pod。</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </AppShell>
</template>
