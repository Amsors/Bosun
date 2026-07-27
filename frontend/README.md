# Bosun frontend

frontend 是基于 Vue 3、Vite 和 TypeScript 的单页应用，提供注册登录、AgentSession 管理和 WebSocket 终端。生产镜像由 Nginx 提供静态文件，并将 `/api/` 转发到 backend API。

## 模块检查

```bash
npm ci
npm run lint
npm run format:check
npm test
npm run build
```

`npm run dev` 只启动 Vite 开发服务器，仓库没有为它配置独立的 backend proxy。需要完整 API、WebSocket 和 Kubernetes 联调时，应在仓库根目录使用本地 k3d 环境：

```bash
make dev-up
make dev-build COMPONENT=frontend
make dev-forward
```

随后访问 `http://localhost:18080`。完整的环境变量、重建和 smoke test 说明见 `deploy/local/README.md`。

## 目录说明

- `src/views/`：登录、注册、会话列表、创建、详情与公开的全局资源监控页面；
- `src/api/`：REST API contract 和 client；
- `src/stores/`：认证与会话状态；
- `src/components/terminal-panel.vue`：浏览器终端与重连逻辑；
- `src/components/resource-usage-panel.vue`：会话 CPU、内存实时图表；最近 60 个采样点仅保存在页面内存；
- `nginx.conf`：生产静态文件、API 和 WebSocket 反向代理配置。

全局资源监控位于 `/admin`，资源刷新周期可选择 1、2、5、10、30 或 60 秒（默认 5 秒），
设置会保存在浏览器中，并与会话详情资源图表共用。页面可分别显示或隐藏 `kube-system`、
`cert-manager`，也可只查看 Agent Pod。页面展示 Agent 的 desired / actual resources、
Kubernetes resize condition、CPU load class、CPU recommendation 和最近应用时间。
资源由 Operator 按优先级和节点容量自动调度，该页面按课程展示需求无需登录。

创建会话时可指定 1–64 GiB 的 Agent memory request。Operator 会将 Agent 容器的
memory limit 设为相同值，并在容量预留时排除内存不足的 worker，便于课堂展示异构节点调度。

1 秒刷新配合本地 Kubelet 1 秒 cAdvisor housekeeping，可通过 Kubelet Summary API
显示 `usageNanoCores` 提供的秒级负载；页面分别展示 API 查询时间与指标采样时间，并只在
真实采样时间变化时追加图表点。实时源不可用时自动显示 metrics-server 回退数据。
