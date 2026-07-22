# Bosun 集群基线

本目录记录 k3s 集群的固定节点身份、安全参数与生产入口配置。真实 kubeconfig、join token 和任何 Secret 不得写入仓库。

无业务数据时的三节点重建、Tailscale 联网、集群初始化和首次发布步骤见
[`rebuild.md`](./rebuild.md)。

## k3s server 参数

新加坡 core 节点 `node-sg-control` 运行唯一的 k3s server。安装或重建时必须至少使用以下参数：

```text
server \
  --node-name node-sg-control \
  --node-label region=sg \
  --node-label role=core \
  --secrets-encryption
```

`--secrets-encryption` 是强制参数。加入 agent 节点所需的 server 地址和 token 只通过安全的运维通道传递，不记录在本仓库。集群使用内置 Traefik；除非后续技术决策变更，不禁用 Traefik，不部署 Redis 或集群内对象存储。

安装后验证：

```bash
sudo k3s secrets-encrypt status
kubectl get nodes --show-labels
kubectl get nodes -o custom-columns=NAME:.metadata.name,TAINTS:.spec.taints
```

## 节点 labels 与 taint

节点名是当前固定资产标识，调度接口使用 `region` 与 `role` labels：

| 节点 | 必须存在的 labels | taint |
|---|---|---|
| `node-sg-control` | `region=sg`, `role=core` | 无 |
| `node-hk-worker` | `region=hk`, `role=worker` | 无 |
| `node-hk-edge` | `region=hk`, `role=edge` | `bosun.io/edge=true:NoSchedule` |

在已加入集群的节点上幂等执行：

```bash
kubectl label node node-sg-control region=sg role=core --overwrite
kubectl label node node-hk-worker region=hk role=worker --overwrite
kubectl label node node-hk-edge region=hk role=edge --overwrite
kubectl taint node node-hk-edge bosun.io/edge=true:NoSchedule --overwrite
```

香港 edge 节点同时是唯一公网入口。k3s ServiceLB 在任意节点首次出现
`svccontroller.k3s.cattle.io/enablelb=true` 后进入 allow-list 模式，因此只给香港节点设置该 label：

```bash
kubectl label node node-hk-edge \
  svccontroller.k3s.cattle.io/enablelb=true --overwrite
```

生产部署工作流会幂等设置该 label，并在发现其他节点也启用了 ServiceLB 时拒绝部署。DNS 必须将
`bosun.amsors.com` 的 A/AAAA 记录指向该节点对应的公网地址，主机防火墙与云安全组必须允许公网
TCP 80/443。80 端口用于 Let's Encrypt HTTP-01 校验和跳转 HTTPS，不得只开放 443。

[`traefik-config.yaml`](./traefik-config.yaml) 是 k3s 内置 Traefik 的持久化 `HelmChartConfig`：它将
Traefik Pod 调度到 edge 节点并设置 `externalTrafficPolicy: Local`，使 ServiceLB 不需要跨节点
SNAT，从而为 Traefik 和 backend 的 IP 限流保留真实客户端地址。工作流会应用该配置，等待
Traefik 完成调度与滚动更新后才继续签发证书。

只有 Traefik 与 frontend 可以容忍 edge taint。平台服务与 PostgreSQL 必须使用 `role=core` 的 nodeSelector，AgentSession 只允许调度到 `role=worker`。namespace、PriorityClass、ServiceAccount、CRD 与 RBAC 均由 `deploy/chart` 统一管理，不再维护独立 bootstrap 清单。

## GitHub Actions 部署 runner

生产部署固定使用位于受控网络内的 repository-level self-hosted runner。该 runner 必须能访问 k3s API，只运行开发者手动触发、引用已通过 CI 且已发布完整 SHA 的 `Deploy production` job；PR 与 main 的代码检查继续使用 GitHub-hosted runner。

runner 使用独立的低权限 Linux 账号运行，禁止使用 root，禁止把 Docker Hub token、kubeconfig 或 GitHub registration token 写入仓库。Bosun 六个业务镜像使用公开 Docker Hub repository，节点不需要 `registries.yaml` 或 `imagePullSecret`。

### 注册与标签

在 GitHub 仓库的 `Settings → Actions → Runners → New self-hosted runner` 获取当前版本的安装和注册命令。registration token 只用于注册且短时有效，不保存到脚本或 shell history。注册时增加部署专用标签：

```text
bosun-deploy
```

最终 runner 必须同时具有 `self-hosted`、`linux`、`bosun-deploy` 标签，与 `.github/workflows/deploy.yml` 的 `runs-on` 完全匹配。学生项目可以在独立低权限账号下以前台 `./run.sh` 运行；终端关闭或主机休眠后 runner 会离线。需要无人值守部署时，再按 GitHub Actions runner 的官方说明安装为 systemd service。

### 主机要求

- 安装 Git、GNU coreutils、curl、kubectl、Helm 4.2.3 所需运行依赖和受支持版本的 GitHub Actions runner；
- runner 账号只能写自身工作目录和临时目录，不加入 `sudo`、`docker` 或 k3s 管理组；
- 到 GitHub Actions 所需 HTTPS 端点只开放出站访问；无需从公网开放 runner 入站端口；
- 到 k3s API 只开放部署所需地址和端口；
- `production` Environment 中的 `KUBECONFIG_B64` 使用最小权限部署身份，不使用默认 admin kubeconfig；
- runner 主机不得运行来自 fork 或未受保护分支的 job；仓库为 public 时不得复用该常驻 runner；
- runner 软件保持自动更新，操作系统按受控节点的补丁策略维护。

workflow 将 kubeconfig 解码到 `${RUNNER_TEMP}`、设置 `0600` 等效权限并在部署 step 退出时删除。self-hosted runner 不保证每次 job 都是全新环境，因此不得依赖工作目录保存 Secret，也不得在日志中输出 kubeconfig。

### GitHub 配置

- Repository Variable：`DOCKERHUB_NAMESPACE`，填 Docker Hub 用户名或 organization；
- Repository Variable：`ACME_EMAIL`，填 Let's Encrypt 账号与证书到期通知邮箱；
- Repository Variables：`DEFAULT_PROVIDER_NAME`、`DEFAULT_PROVIDER_UPSTREAM_URL`、`DEFAULT_PROVIDER_AUTH_HEADER`、`DEFAULT_PROVIDER_AUTH_SCHEME`；
- `registry` Environment Secrets：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`；
- `production` Environment Secrets：`KUBECONFIG_B64`、`JWT_PRIVATE_KEY`、`DEFAULT_PROVIDER_API_KEY`；
- `production` 只允许受保护的 `main` 分支，并在 GitHub 套餐支持时启用 required reviewer；
- `main` 分支保护只要求始终出现的 `CI / ci-gate`，不把可能按变更范围跳过的模块 job 单独设为 required check；
- `bosun-database` Secret 由运维人员直接在 `bosun-platform` namespace 注入，不经过 GitHub Actions。

### 公网入口与证书

生产部署将 cert-manager 作为独立 Helm release 安装在 `cert-manager` namespace，版本固定在
workflow 中；它不作为 Bosun chart 的 subchart，避免多个 release 竞争集群级 CRD。controller、
webhook、cainjector 与 startup API check 固定调度到 `role=core` 节点。

Bosun chart 使用 Let's Encrypt production ACME endpoint 创建 `bosun-letsencrypt-production`
ClusterIssuer，通过 Traefik `web` entrypoint 完成 HTTP-01，solver Pod 固定在 core 节点，并自动维护
`bosun-tls` Secret。
`websecure` 入口仅使用该 Secret 提供 HTTPS，普通 HTTP 请求永久跳转到 HTTPS。部署完成前
workflow 会等待 Certificate `Ready` 并实际请求公网 HTTP/HTTPS 端点；DNS、端口或证书签发
任何一项未就绪都会使部署明确失败。

发布与部署均为显式人工操作：先确认目标 commit 的 `CI / ci-gate` 绿色，再手动运行 `Publish images` 并输入完整 40 位 commit SHA；该工作流全量构建六个统一七位 SHA tag 的镜像并仅推送到 Docker Hub。镜像全部发布成功后，手动运行 `Deploy production` 并输入相同完整 SHA。工作流不再因 main 提交或镜像发布成功自动部署生产。任何缺失的 Variable 或 Secret 都会在使用前失败，检查过程只报告配置项名称，不输出其值。
