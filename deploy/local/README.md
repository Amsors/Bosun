# 本地 k3d 开发环境

本目录提供与生产共用 Helm chart 的本地 k3s 仿真环境。默认创建 `core`、`worker`、`edge` 三个角色节点以及仅供本机使用的 Registry；它用于高频联调，不替代真实跨区集群 E2E。

## 前置依赖

本机需要 Docker、k3d、kubectl、Helm 4、OpenSSL、Git 和 Bash。运行 smoke 还需要 curl、jq、uuidgen 与 sha256sum。

**安装 k3d：**

https://k3d.io

**安装 Helm：**

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

上方 `make dev-up` 命令初次执行需要拉取基础镜像，耗时会相对较长。

完成后执行 `make dev-forward`，再在浏览器访问 `http://localhost:18080`。


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

数据库清空或集群重建后，可直接在登录页使用用户名 `admin` 和至少 8 个字符的密码登录。若 `admin` 尚不存在，首次登录会自动创建该用户；已存在时不会重置密码。

下表逐条说明行为与前置。所有命令在执行部署或破坏性动作前，都会校验 `kubectl` 当前 context 为 `k3d-bosun`，否则直接拒绝，避免误操作其他集群。

| 命令 | 行为 | 前置与参数 |
|---|---|---|
| `make dev-up` | 首次拉起环境：创建集群 → 构建全部镜像 → 部署 chart。幂等，集群或 Registry 已存在则复用。 | 需注入 provider 环境变量（见「启动」）。 |
| `make dev-build [COMPONENT=<名>]` | 先删除宿主机 Docker 中对应组件的旧版本镜像，再重新构建、推送并滚动重启对应 Deployment。`COMPONENT` 取 `api`、`gateway`、`operator`、`frontend`、`agent`、`egress-proxy`、`all`，缺省 `all`。 | 集群须已存在。`COMPONENT=all` 会逐个清理并构建所有组件，同时重新部署 chart 并重启全部 Deployment，因此需 provider 环境变量；单组件构建不需要。改 `agent` 镜像不替换存量 Pod，需新建测试 session 才生效。 |
| `make dev-deploy` | 用当前镜像 tag 重新 `helm upgrade` 并重启 gateway，不重新构建镜像。 | 集群须已存在，需 provider 环境变量。适用于只改了 chart/values 或想重新注入 provider 配置的场景。 |
| `make dev-forward` | 前台把 `http://127.0.0.1:18080` 转发到 frontend Service，转发因集群重建等原因退出时每 2 秒自动重试并打印重试次数，`Ctrl-C` 结束；frontend 容器继续经集群 Service 代理 `/api/`。 | 启动时集群须运行中。 |
| `make dev-smoke` | 临时转发 frontend 并依次运行 smoke A/B（注册登录 → 创建 session → 等待 `Running` → 删除并校验 Pod、PVC、CR 清理），结束后自动收回转发。 | 需 `BOSUN_E2E_PASSWORD`，即 `BOSUN_E2E_PASSWORD='<test-only-password>' make dev-smoke`。 |
| `make dev-reset` | 删除并重建名为 `bosun` 的集群后重新部署，保留 Registry 镜像缓存。 | 需 provider 环境变量。用于集群状态脏了又想省去重新拉镜像。 |
| `make dev-down` | 删除 `bosun` 集群与本地 Registry，回到干净状态。 | 破坏性，不需要 provider 环境变量。 |

本地镜像 tag 默认取当前 commit 的七位 SHA，可用 `BOSUN_DEV_IMAGE_TAG`（须为七位小写十六进制）覆盖；Registry 端口可用 `BOSUN_DEV_REGISTRY_PORT`（默认 5001）调整。API key、JWT 私钥与数据库口令只写入临时目录和本地 k8s Secret。

## 调度验证

正常创建的 AgentSession 必须调度到 `role=worker`，不会使用 core 或带 taint 的 edge。创建测试会话后执行：

```bash
kubectl get pods -A \
  -l bosun.io/session \
  -o wide
```

新 session 应落在 `k3d-bosun-agent-0`（`role=worker`、`region=hk`），不得落在 `k3d-bosun-server-0` 或带 `bosun.io/edge=true:NoSchedule` taint 的 edge 节点。
