# Bosun 集群基线

本目录记录 k3s 集群的固定节点身份、安全参数与 bootstrap 资源。真实 kubeconfig、join token 和任何 Secret 不得写入仓库。

## k3s server 参数

新加坡 core 节点 `node-sg-local-4c12g` 运行唯一的 k3s server。安装或重建时必须至少使用以下参数：

```text
server \
  --node-name node-sg-local-4c12g \
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
| `node-sg-local-4c12g` | `region=sg`, `role=core` | 无 |
| `node-cn-aliyun-2c2g` | `region=cn`, `role=worker` | 无 |
| `node-cn-tencent-2c2g` | `region=cn`, `role=worker` | 无 |
| `node-hk-dogyun-1c1g` | `region=hk`, `role=edge` | `bosun.io/edge=true:NoSchedule` |

在已加入集群的节点上幂等执行：

```bash
kubectl label node node-sg-local-4c12g region=sg role=core --overwrite
kubectl label node node-cn-aliyun-2c2g region=cn role=worker --overwrite
kubectl label node node-cn-tencent-2c2g region=cn role=worker --overwrite
kubectl label node node-hk-dogyun-1c1g region=hk role=edge --overwrite
kubectl taint node node-hk-dogyun-1c1g bosun.io/edge=true:NoSchedule --overwrite
```

只有 ingress 与 frontend 可以在后续 Helm chart 中声明该 edge taint 的 toleration。平台有状态服务必须使用 `role=core` 的 nodeSelector。

## Bootstrap 资源

`bootstrap.yaml` 建立：

- `bosun-platform` namespace；
- 不抢占的 `bosun-free`、`bosun-paid` PriorityClass；
- backend API、gateway、operator 的 ServiceAccount；
- 集群级最小权限与绑定；
- 供后续 `UserEnvironment` controller 在用户 namespace 内绑定的 Role。

应用与检查：

```bash
kubectl apply -f deploy/cluster/bootstrap.yaml
kubectl auth can-i create agentsessions.bosun.io \
  --as system:serviceaccount:bosun-platform:bosun-backend-api
kubectl auth can-i create tokenreviews.authentication.k8s.io \
  --as system:serviceaccount:bosun-platform:bosun-gateway
kubectl auth can-i get secrets --all-namespaces \
  --as system:serviceaccount:bosun-platform:bosun-gateway
```

最后一条在 bootstrap 阶段必须返回 `no`。gateway 的 platform Secret 读取权由 Helm 在 `bosun-platform` 内以 RoleBinding 授予；用户 credential Secret 与 backend `pods/exec` 权限由 operator 在对应用户 namespace 内绑定 `bosun-user-gateway`、`bosun-user-backend-terminal` Role，禁止创建全局 Secret 或 `pods/exec` 绑定。

T0.6 会将这些长期资源纳入 `deploy/chart`。在此之前本清单是集群初始化入口；迁移到 Helm 后不得同时由两套来源维护字段。

## GitHub Actions 部署 runner

生产部署固定使用位于受控网络内的 repository-level self-hosted runner。该 runner 必须能访问 k3s API，只运行来自受保护 `main` 分支且已经通过 CI 和镜像发布的 `Deploy production` job；PR 的代码检查和镜像构建继续使用 GitHub-hosted runner。

runner 使用独立的低权限 Linux 账号运行，禁止使用 root，禁止把 ACR 密码、kubeconfig 或 GitHub registration token 写入仓库。节点本地已有的 k3s `registries.yaml` 负责工作负载拉取私有 ACR 镜像，workflow 不创建或分发 `imagePullSecret`。

### 注册与标签

在 GitHub 仓库的 `Settings → Actions → Runners → New self-hosted runner` 获取当前版本的安装和注册命令。registration token 只用于注册且短时有效，不保存到脚本或 shell history。注册时增加部署专用标签：

```text
bosun-deploy
```

最终 runner 必须同时具有 `self-hosted`、`linux`、`bosun-deploy` 标签，与 `.github/workflows/deploy.yml` 的 `runs-on` 完全匹配。注册完成后按 GitHub 页面给出的 `svc.sh` 命令安装为 systemd service，并确认服务状态为 `Active (running)`、GitHub 页面状态为 `Idle`。

### 主机要求

- 安装 Git、GNU coreutils、Helm 4.2.3 所需运行依赖和受支持版本的 GitHub Actions runner；
- runner 账号只能写自身工作目录和临时目录，不加入 `sudo`、`docker` 或 k3s 管理组；
- 到 GitHub Actions 所需 HTTPS 端点只开放出站访问；无需从公网开放 runner 入站端口；
- 到 k3s API 只开放部署所需地址和端口；
- `production` Environment 中的 `KUBECONFIG_B64` 使用最小权限部署身份，不使用默认 admin kubeconfig；
- runner 主机不得运行来自 fork 或未受保护分支的 job；仓库为 public 时不得复用该常驻 runner；
- runner 软件保持自动更新，操作系统按受控节点的补丁策略维护。

workflow 将 kubeconfig 解码到 `${RUNNER_TEMP}`、设置 `0600` 等效权限并在部署 step 退出时删除。self-hosted runner 不保证每次 job 都是全新环境，因此不得依赖工作目录保存 Secret，也不得在日志中输出 kubeconfig。

### GitHub 配置

- Repository Variable：`ACR_REGISTRY`，只填 registry endpoint，不含 `/bosun`；
- `registry` Environment Secrets：`ACR_USERNAME`、`ACR_PASSWORD`；
- `production` Environment Secret：`KUBECONFIG_B64`；
- `production` 只允许受保护的 `main` 分支，并在 GitHub 套餐支持时启用 required reviewer；
- `bosun-database` Secret 由运维人员直接在 `bosun-platform` namespace 注入，不经过 GitHub Actions。

发布链路固定为 `CI → Publish images → Deploy production`。`Publish images` 仅在 CI 成功后运行并上传包含完整 commit SHA 的短期 metadata artifact；`Deploy production` 只在五类镜像全部推送成功后运行，也可输入已经发布镜像对应的完整 40 位 commit SHA 单独手动重试。任何缺失的 Variable 或 Secret 都会在使用前失败，检查过程只报告配置项名称，不输出其值。
