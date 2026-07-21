# 本地 k3d 开发环境

本目录提供与生产共用 Helm chart 的本地 k3s 仿真环境。默认创建 `core`、`worker`、`edge` 三个角色节点以及仅供本机使用的 Registry；它用于高频联调，不替代真实跨区集群 E2E。

## 前置依赖

本机需要 Docker、k3d、kubectl、Helm 4、OpenSSL、Git 和 Bash。运行 smoke 还需要 curl、jq、uuidgen 与 sha256sum。

**安装k3d：**

https://k3d.io

**安装helm：**

https://helm.sh/zh/docs/intro/install/

## 启动

大模型 provider 配置通过当前 shell 环境变量注入：

```bash
export BOSUN_DEV_PROVIDER_URL="https://api.example.com"
export BOSUN_DEV_PROVIDER_API_KEY="sk-xxxx"
export BOSUN_DEV_PROVIDER_NAME="platform-default"
export BOSUN_DEV_PROVIDER_AUTH_HEADER="x-api-key"
export BOSUN_DEV_PROVIDER_AUTH_SCHEME=""
make dev-up
```

上方`make dev-up`命令初次执行时间可能比较长，耐心等待即可。

执行完以上命令后，执行一下 `make dev-forward`，之后在浏览器里访问`localhost:18080`，就可以看到网站页面了


## 日常命令

速览（可直接复制）：

```bash
make dev-build COMPONENT=frontend
make dev-deploy
make dev-forward
BOSUN_E2E_PASSWORD='<test-only-password>' make dev-smoke
make dev-reset
make dev-down
```

下表逐条说明行为与前置。所有命令在执行部署或破坏性动作前，都会校验 `kubectl` 当前 context 为 `k3d-bosun`，否则直接拒绝，避免误操作其他集群。

| 命令 | 行为 | 前置与参数 |
|---|---|---|
| `make dev-up` | 首次拉起环境：创建集群 → 构建全部镜像 → 部署 chart。幂等，集群或 Registry 已存在则复用。 | 需注入 provider 环境变量（见「启动」）。 |
| `make dev-build [COMPONENT=<名>]` | 重新构建并推送镜像后滚动重启对应 Deployment。`COMPONENT` 取 `api`、`gateway`、`operator`、`frontend`、`agent`、`egress-proxy`、`all`，缺省 `all`。 | 集群须已存在。`COMPONENT=all` 会顺带重新部署 chart 并重启全部 Deployment，因此需 provider 环境变量；单组件构建不需要。改 `agent` 镜像不替换存量 Pod，需新建测试 session 才生效。 |
| `make dev-deploy` | 用当前镜像 tag 重新 `helm upgrade` 并重启 gateway，不重新构建镜像。 | 集群须已存在，需 provider 环境变量。适用于只改了 chart/values 或想重新注入 provider 配置的场景。 |
| `make dev-forward` | 前台把 `http://127.0.0.1:18080` 转发到 frontend Service，`Ctrl-C` 结束；frontend 容器继续经集群 Service 代理 `/api/`。 | 集群须运行中。 |
| `make dev-smoke` | 临时转发 frontend 并依次运行 smoke A/B（注册登录 → 创建 session → 等待 `Running` → 删除并校验 Pod、PVC、CR 清理），结束后自动收回转发。 | 需 `BOSUN_E2E_PASSWORD`，即 `BOSUN_E2E_PASSWORD='<test-only-password>' make dev-smoke`。 |
| `make dev-reset` | 删除并重建名为 `bosun` 的集群后重新部署，保留 Registry 镜像缓存。 | 需 provider 环境变量。用于集群状态脏了又想省去重新拉镜像。 |
| `make dev-down` | 删除 `bosun` 集群与本地 Registry，回到干净状态。 | 破坏性，不需要 provider 环境变量。 |

本地镜像 tag 默认取当前 commit 的七位 SHA，可用 `BOSUN_DEV_IMAGE_TAG`（须为七位小写十六进制）覆盖；Registry 端口可用 `BOSUN_DEV_REGISTRY_PORT`（默认 5001）调整。API key、JWT 私钥与数据库口令只写入临时目录和本地 k8s Secret。

**修改某些代码后，只需执行一下make build xxx(修改的模块)，就可以自动重新建立集群，相应代码更改会自动生效。但端口转发会断掉，需要重新转发一下(make dev-forward)**

## 调度验证

正常创建的 agent session 首选 `core`。验证 worker fallback 时先执行：

```bash
kubectl cordon k3d-bosun-server-0
# 创建并检查新 session
kubectl uncordon k3d-bosun-server-0
```

新 session 应落在 `k3d-bosun-agent-0`（`worker/region=cn`），不得落在带 `bosun.io/edge=true:NoSchedule` taint 的 edge 节点。
