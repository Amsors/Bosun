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

API 通过 Kubernetes `metrics.k8s.io/v1beta1` 实时读取 Node 与 Pod 的 CPU、内存用量，并结合 Pod spec 返回 requests/limits：

- `GET /api/v1/sessions/:id/resources`：需要登录，只允许读取当前用户的会话；
- `GET /api/v1/admin/cluster`：课程展示用公开接口，返回全局 Node、Pod 与 Agent 所属用户。
- `PUT /api/v1/admin/sessions/:id/resources`：课程展示用公开接口，通过 Kubernetes
  `pods/resize` subresource 原地调整 `agent` 容器的 CPU / memory limit。请求体为
  `{"cpuMillicores":700,"memoryBytes":1073741824}`，limit 不得低于该容器的 request。

接口不保存资源采样。集群未提供 metrics-server 时仍返回 Node、Pod 和资源规格，并通过 availability 字段标记实时指标暂不可用。
资源调整不会修改平台 `auth-proxy` sidecar；返回的 Pod 总 limit 仍包含这些固定平台开销。
