# Bosun backend

backend 是一个 Go module，构建两个独立进程：

- `cmd/api`：REST API 与后续 terminal proxy；
- `cmd/gateway`：Anthropic-compatible gateway 与后续 archive gateway。

本地命令：

```bash
make build
make test
make lint
make sqlc
make migrate-up
```

两个进程都要求 `BOSUN_DATABASE_URL`。API 默认监听 `:8080`，gateway 默认监听 `:8081`；可分别用 `BOSUN_API_LISTEN_ADDRESS`、`BOSUN_GATEWAY_LISTEN_ADDRESS` 覆盖。所有配置都在 `internal/config` 集中解析。

## 资源监控

API 优先通过 Kubernetes API Server 的 Node proxy 读取 Kubelet Summary API：内存使用
working set，CPU 使用相邻累计计数器按 Summary 中的真实采样时间差换算为 millicores。
重复的底层样本会被去重，不会计算成瞬时 `0m`。PodMetrics
`metrics.k8s.io/v1beta1` 用于第一次请求尚未形成 CPU 差分、或 Node proxy 不可用时的
回退；Node 总用量仍来自 metrics-server。响应同时携带指标来源、真实采样时间与采样窗口，
并结合 Pod spec 返回 requests/limits：

- `GET /api/v1/sessions/:id/resources`：需要登录，只允许读取当前用户的会话；
- `GET /api/v1/admin/cluster`：课程展示用公开接口，返回全局 Node、Pod 与 Agent 所属用户。
- `PUT /api/v1/admin/sessions/:id/resources`：课程展示用公开接口，将
  `AgentSession.spec.resourceScaling` 持久化为 Manual intent。请求体为
  `{"cpuMillicores":700,"memoryBytes":1073741824}`，CPU / memory 必须位于对应 tier
  的 hard bounds。
- `DELETE /api/v1/admin/sessions/:id/resources`：将会话恢复为 Auto 模式并清空 Manual
  intent。

backend 只在内存中保留每个容器上一次 CPU 累计计数器，不持久化资源历史。集群未提供
metrics-server 时仍返回 Node、Pod 和资源规格；Kubelet 实时采样完成两次请求后可继续
提供 Pod CPU/内存，Node 总用量通过 availability 字段标记暂不可用。
资源 API 返回 Pod desired、container actual、Pod resize condition 和 Auto/Manual
状态。backend 不直接调用 `pods/resize`；Operator 是唯一资源写入者，且不会修改平台
`auth-proxy` sidecar。
