<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'

import type {
  ClusterResourceSnapshot,
  ContainerResourceSnapshot,
  PodResourceSnapshot,
} from '../api/contracts'
import { monitorApi } from '../api/monitor'
import AppShell from '../components/app-shell.vue'
import StatusPanel from '../components/status-panel.vue'
import { formatCPU, formatMemory, percent } from '../utils/resources'

const snapshot = ref<ClusterResourceSnapshot | null>(null)
const loading = ref(true)
const error = ref('')
const search = ref('')
const showKubeSystem = ref(false)
const showCertManager = ref(false)
const agentOnly = ref(false)
const resizeDrafts = ref<Record<string, { cpuMillicores: number; memoryMiB: number }>>({})
const resizeDirty = ref<Record<string, boolean>>({})
const resizeBusy = ref<Record<string, boolean>>({})
const resizeErrors = ref<Record<string, string>>({})
let poller: ReturnType<typeof globalThis.setInterval> | null = null
let requestActive = false
const mebibyte = 1024 * 1024

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

async function load(): Promise<void> {
  if (requestActive) return
  requestActive = true
  try {
    const next = await monitorApi.cluster()
    syncResizeDrafts(next.pods)
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

function draftFromPod(pod: PodResourceSnapshot): { cpuMillicores: number; memoryMiB: number } {
  const limits = agentContainer(pod)?.limits
  return {
    cpuMillicores: limits?.cpuMillicores || 0,
    memoryMiB: Math.round((limits?.memoryBytes || 0) / mebibyte),
  }
}

function sameDraft(
  left: { cpuMillicores: number; memoryMiB: number } | undefined,
  right: { cpuMillicores: number; memoryMiB: number },
): boolean {
  return left?.cpuMillicores === right.cpuMillicores && left.memoryMiB === right.memoryMiB
}

function syncResizeDrafts(pods: PodResourceSnapshot[]): void {
  for (const pod of pods) {
    if (!pod.isAgent || !pod.sessionID || !agentContainer(pod)) continue
    const current = draftFromPod(pod)
    if (!resizeDrafts.value[pod.sessionID] || !resizeDirty.value[pod.sessionID]) {
      resizeDrafts.value[pod.sessionID] = current
    }
  }
}

function startResizeEdit(sessionID: string): void {
  resizeDirty.value[sessionID] = true
}

function finishResizeEdit(pod: PodResourceSnapshot): void {
  if (!pod.sessionID) return
  resizeDirty.value[pod.sessionID] = !sameDraft(
    resizeDrafts.value[pod.sessionID],
    draftFromPod(pod),
  )
}

function minimumCPU(pod: PodResourceSnapshot): number {
  return agentContainer(pod)?.requests.cpuMillicores || 1
}

function minimumMemoryMiB(pod: PodResourceSnapshot): number {
  return Math.ceil((agentContainer(pod)?.requests.memoryBytes || 1) / mebibyte)
}

function canResize(pod: PodResourceSnapshot): boolean {
  if (!pod.sessionID || pod.phase !== 'Running' || resizeBusy.value[pod.sessionID]) return false
  const draft = resizeDrafts.value[pod.sessionID]
  if (
    !draft ||
    !Number.isInteger(draft.cpuMillicores) ||
    !Number.isInteger(draft.memoryMiB) ||
    draft.cpuMillicores < minimumCPU(pod) ||
    draft.memoryMiB < minimumMemoryMiB(pod)
  )
    return false
  return !sameDraft(draft, draftFromPod(pod))
}

async function applyResize(pod: PodResourceSnapshot): Promise<void> {
  if (!pod.sessionID || !canResize(pod)) return
  const sessionID = pod.sessionID
  const draft = resizeDrafts.value[sessionID]!
  resizeBusy.value[sessionID] = true
  resizeErrors.value[sessionID] = ''
  try {
    const result = await monitorApi.resizeAgent(sessionID, {
      cpuMillicores: draft.cpuMillicores,
      memoryBytes: draft.memoryMiB * mebibyte,
    })
    const updated = {
      ...result.pod,
      username: pod.username,
      sessionName: pod.sessionName,
    }
    if (snapshot.value) {
      snapshot.value = {
        ...snapshot.value,
        observedAt: result.observedAt,
        pods: snapshot.value.pods.map((item) =>
          item.namespace === pod.namespace && item.name === pod.name ? updated : item,
        ),
      }
    }
    resizeDirty.value[sessionID] = false
    syncResizeDrafts([updated])
  } catch {
    resizeErrors.value[sessionID] = '调整失败，请检查输入值和集群的 in-place resize 状态。'
  } finally {
    resizeBusy.value[sessionID] = false
  }
}

onMounted(async () => {
  await load()
  poller = globalThis.setInterval(load, 5000)
})
onUnmounted(() => {
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
        <span class="live-dot" />每 5 秒刷新
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
                  <span
                    >/
                    {{
                      pod.limits.cpuMillicores ? formatCPU(pod.limits.cpuMillicores) : '未设置'
                    }}</span
                  >
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
                    <strong>{{ pod.username || '未知用户' }}</strong>
                    <span>{{ pod.sessionName || pod.sessionID }}</span>
                  </template>
                  <span v-else>—</span>
                </td>
                <td>
                  <form
                    v-if="pod.isAgent && pod.sessionID && resizeDrafts[pod.sessionID]"
                    class="resize-form"
                    @submit.prevent="applyResize(pod)"
                  >
                    <label>
                      <span>CPU (m)</span>
                      <input
                        v-model.number="resizeDrafts[pod.sessionID].cpuMillicores"
                        type="number"
                        step="1"
                        :min="minimumCPU(pod)"
                        :disabled="resizeBusy[pod.sessionID] || pod.phase !== 'Running'"
                        @focus="startResizeEdit(pod.sessionID)"
                        @input="startResizeEdit(pod.sessionID)"
                        @blur="finishResizeEdit(pod)"
                      />
                    </label>
                    <label>
                      <span>内存 (MiB)</span>
                      <input
                        v-model.number="resizeDrafts[pod.sessionID].memoryMiB"
                        type="number"
                        step="1"
                        :min="minimumMemoryMiB(pod)"
                        :disabled="resizeBusy[pod.sessionID] || pod.phase !== 'Running'"
                        @focus="startResizeEdit(pod.sessionID)"
                        @input="startResizeEdit(pod.sessionID)"
                        @blur="finishResizeEdit(pod)"
                      />
                    </label>
                    <button class="primary" type="submit" :disabled="!canResize(pod)">
                      {{ resizeBusy[pod.sessionID] ? '调整中…' : '应用' }}
                    </button>
                    <span v-if="pod.resize" class="resize-state">
                      {{ pod.resize.reason || 'Kubernetes 正在应用新 Limit' }}
                    </span>
                    <span v-if="resizeErrors[pod.sessionID]" class="resize-error" role="alert">
                      {{ resizeErrors[pod.sessionID] }}
                    </span>
                  </form>
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
