# 会话优先级调度验证报告

## 1. 测试结论

**测试通过。**

本测试证明了 Bosun 使用的 Kubernetes 非抢占式优先级调度符合以下预期：

1. 低、普通、高优先级分别映射为数值 `1000`、`2000`、`3000`。
2. 资源不足时，尚未获得资源的工作负载保持 `Pending`。
3. 已经运行的低优先级工作负载不会被高优先级工作负载强制中断。
4. 资源释放后，高优先级工作负载先于普通优先级工作负载获得资源。

## 2. 调度策略

| 用户选择 | Kubernetes PriorityClass | 数值 | 抢占策略 |
|---|---|---:|---|
| 低优先级 | `bosun-free` | 1000 | `Never` |
| 普通优先级 | `bosun-normal` | 2000 | `Never` |
| 高优先级 | `bosun-high` | 3000 | `Never` |

`preemptionPolicy: Never` 表示高优先级会话不会杀死已经运行的低优先级会话。当资源释放且多个会话正在等待时，Kubernetes Scheduler 会优先调度数值更高的会话。

## 3. 测试环境

- 测试日期：2026-07-23
- 集群：本地 k3d Bosun 集群
- 目标节点：`k3d-bosun-agent-0`
- 节点标签：`role=worker`
- 测试容器：`busybox:1.36`
- 单个测试 Pod 的内存请求：`8277355Ki`
- 请求比例：目标节点可分配内存的 51%

每个测试 Pod 请求目标节点 51% 的可分配内存，因此该节点同时最多容纳一个测试 Pod。容器实际上不会使用这些内存；这里使用 Kubernetes resource request 人为制造可控的调度容量限制。

## 4. 前置检查

确认三个 PriorityClass 已部署：

```bash
kubectl get priorityclass bosun-free bosun-normal bosun-high
```

确认应用会话的 AgentSession 和 Agent Pod 使用了对应 PriorityClass：

```bash
kubectl get agentsessions -A \
  -o custom-columns='NAMESPACE:.metadata.namespace,SESSION:.metadata.name,PRIORITY:.spec.priorityClassName,PHASE:.status.phase'

kubectl get pods -A -l bosun.io/session \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,CLASS:.spec.priorityClassName,VALUE:.spec.priority,NODE:.spec.nodeName,PHASE:.status.phase'
```

## 5. 测试步骤

### 5.1 计算单个测试 Pod 的资源请求

```bash
NODE=$(kubectl get nodes -l role=worker -o jsonpath='{.items[0].metadata.name}')
MEM_KI=$(kubectl get node "$NODE" -o jsonpath='{.status.allocatable.memory}' | sed 's/Ki$//')
REQ_KI=$((MEM_KI * 51 / 100))

echo "测试节点：$NODE"
echo "每个测试 Pod 请求：${REQ_KI}Ki 内存"
```

本次实际输出：

```text
测试节点：k3d-bosun-agent-0
每个测试 Pod 请求：8277355Ki 内存
```

### 5.2 创建低优先级 Pod 并占用唯一调度槽位

```bash
kubectl create namespace bosun-priority-test \
  --dry-run=client -o yaml | kubectl apply -f -

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: priority-low
  namespace: bosun-priority-test
spec:
  priorityClassName: bosun-free
  nodeSelector:
    kubernetes.io/hostname: ${NODE}
  containers:
    - name: test
      image: busybox:1.36
      command: ["sh", "-c", "sleep 3600"]
      resources:
        requests:
          cpu: 10m
          memory: "${REQ_KI}Ki"
        limits:
          memory: "${REQ_KI}Ki"
EOF

kubectl get pod priority-low -n bosun-priority-test -w
```

本次实际输出：

```text
NAME           READY   STATUS              RESTARTS   AGE
priority-low   0/1     ContainerCreating   0          3s
priority-low   1/1     Running             0          7s
```

这证明低优先级 Pod 成功获得了目标节点上的调度槽位。

### 5.3 创建普通和高优先级等待者

```bash
for SPEC in normal:bosun-normal high:bosun-high; do
  NAME=${SPEC%%:*}
  CLASS=${SPEC##*:}

  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: priority-${NAME}
  namespace: bosun-priority-test
spec:
  priorityClassName: ${CLASS}
  nodeSelector:
    kubernetes.io/hostname: ${NODE}
  containers:
    - name: test
      image: busybox:1.36
      command: ["sh", "-c", "sleep 3600"]
      resources:
        requests:
          cpu: 10m
          memory: "${REQ_KI}Ki"
        limits:
          memory: "${REQ_KI}Ki"
EOF
done
```

查看调度状态：

```bash
kubectl get pods -n bosun-priority-test \
  -o custom-columns='NAME:.metadata.name,PRIORITY:.spec.priority,NODE:.spec.nodeName,STATUS:.status.phase'
```

本次实际输出：

```text
NAME              PRIORITY   NODE                STATUS
priority-high     3000       <none>              Pending
priority-low      1000       k3d-bosun-agent-0   Running
priority-normal   2000       <none>              Pending
```

此时可以确认：

- 节点容量只允许一个测试 Pod 运行。
- 已运行的低优先级 Pod 没有被高优先级 Pod 抢占。
- 普通和高优先级 Pod 都在等待资源。

### 5.4 释放资源并观察调度顺序

```bash
kubectl delete pod priority-low -n bosun-priority-test

kubectl get pods -n bosun-priority-test \
  -o custom-columns='NAME:.metadata.name,PRIORITY:.spec.priority,NODE:.spec.nodeName,STATUS:.status.phase' \
  --watch
```

本次实际输出：

```text
pod "priority-low" deleted from bosun-priority-test namespace
NAME              PRIORITY   NODE                STATUS
priority-high     3000       k3d-bosun-agent-0   Running
priority-normal   2000       <none>              Pending
```

资源释放后，高优先级 Pod 获得节点，普通优先级 Pod 继续等待。这直接证明 Kubernetes Scheduler 按预期优先服务高优先级工作负载。

## 6. 证据链

| 验证点 | 实际结果 | 判定 |
|---|---|---|
| 优先级数值正确 | low=1000、normal=2000、high=3000 | 通过 |
| 容量限制有效 | 51% 内存请求使节点只能运行一个测试 Pod | 通过 |
| 非抢占策略有效 | low 运行时，high 保持 Pending | 通过 |
| 等待队列有效 | high 和 normal 在资源不足时保持 Pending | 通过 |
| 释放后按优先级调度 | 删除 low 后，high 先于 normal 运行 | 通过 |

因此，Bosun 的“资源不足时排队、资源释放后优先启动高优先级会话”调度基础已经得到可重复验证。

## 7. 清理测试资源

完成验证后删除独立测试命名空间：

```bash
kubectl delete namespace bosun-priority-test
```

该命令只会删除本测试创建的三个测试 Pod 和测试命名空间，不会删除 Bosun 的用户会话或平台组件。
