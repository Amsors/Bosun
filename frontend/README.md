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

全局资源监控位于 `/admin`，每 5 秒刷新，可分别显示或隐藏 `kube-system`、`cert-manager`，也可只查看 Agent Pod。该页面按课程展示需求无需登录。
