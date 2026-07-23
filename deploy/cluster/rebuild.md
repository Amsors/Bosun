# 四节点生产集群从零重建

本文适用于没有业务数据、允许丢失 PostgreSQL 和 PVC 的学生项目。不做 etcd、PostgreSQL 或 workspace 数据迁移，而是直接销毁旧 k3s 集群并重建。

## 目标拓扑

| 节点名 | 位置与用途 | labels | 调度内容 |
|---|---|---|---|
| `node-sg-control` | 新加坡本地机，k3s server | `region=sg`, `role=core` | API、gateway、operator、PostgreSQL、cert-manager |
| `node-hk-worker` | 香港大内存云主机，k3s agent | `region=hk`, `role=worker` | 所有 `AgentSession` |
| `node-hk-worker-1` | 香港大内存云主机，k3s agent | `region=hk`, `role=worker` | 所有 `AgentSession` |
| `node-hk-edge` | 香港小内存云主机，k3s agent | `region=hk`, `role=edge` | Traefik、ServiceLB、frontend |

这是单 control plane，不具备 HA。新加坡机故障时整个集群无法管理，但对当前项目规模是可接受的简化。`role=core` 节点不添加 control-plane taint，因为当前 Helm chart 明确要求平台服务和 PostgreSQL 运行在该节点。

四台机器均使用 Ubuntu 24.04 LTS x86_64，并使用 SSD。edge 节点理论上 1 GiB 可以启动，但 OS、k3s agent、Traefik 和 frontend 同时运行时容易 OOM，建议至少 2 GiB。

## 1. 下线旧集群

先确认当前 context 确实是要丢弃的集群：

```bash
kubectl config current-context
kubectl get nodes -o wide
```


```bash
kubectl delete node xxx xxx
```

在每台旧 agent 上执行：

```bash
sudo /usr/local/bin/k3s-agent-uninstall.sh
```

最后在旧 server 上执行：

```bash
sudo /usr/local/bin/k3s-uninstall.sh
```

卸载 server 会删除本地集群数据、kubeconfig 和 k3s 工具。如果某台旧主机已经不可访问，只删除它的 Node 对象即可；整个旧 server 随后也会被销毁。

在 Tailscale Admin Console 中移除不再使用的旧设备。Bosun 业务镜像直接使用 Docker Hub，节点不需要 `/etc/rancher/k3s/registries.yaml`；重用装过其他集群的主机时，应先确认没有遗留的自定义 registry 配置。

## 2. 准备新主机

在新机器上都执行：

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl openssl
sudo timedatectl set-ntp true
sudo swapoff -a
curl -fsSL https://tailscale.com/install.sh | sh
```

同时在 `/etc/fstab` 中注释 swap 行，避免重启后重新开启。Docker 不需要安装，k3s 自带 containerd。新加坡本地机必须关闭自动休眠。

依次在四台机器上登录 Tailscale：

```bash
# 新加坡机
sudo tailscale up --hostname=node-sg-control

# 香港 worker
sudo tailscale up --hostname=node-hk-worker

# 另一台香港 worker
sudo tailscale up --hostname=node-hk-worker-1

# 香港 edge
sudo tailscale up --hostname=node-hk-edge
```

每次只在对应主机执行其中一条。记录四台机的 Tailscale IPv4：

```bash
tailscale ip -4
tailscale status
```

### 为 k3s 配置专用 resolver

Ubuntu 的 DHCP 或 Tailscale 可能向节点 `/etc/resolv.conf` 注入 `localdomain` 和
MagicDNS search domain。kubelet 默认会把它们追加到 Pod 的 DNS search 列表，而
Kubernetes 默认的 `ndots:5` 会使 Go resolver 先尝试
`api.deepseek.com.localdomain` 之类的扩展名。如果上游 DNS 对 `*.localdomain`
返回了错误地址，cert-manager 和 gateway 会连接到错误主机。

在四台节点上都创建无 search domain 的专用 resolver 文件：

```bash
sudo install -d -m 0755 /etc/rancher/k3s
printf '%s\n' \
  'nameserver 1.1.1.1' \
  'nameserver 8.8.8.8' |
  sudo tee /etc/rancher/k3s/resolv.conf >/dev/null
```

如果节点无法访问上述公共 DNS，将它们替换为当前网络可达且不会对
`*.localdomain` 做通配解析的 resolver。不要在该文件中添加 `search`
或 `domain` 行。

在每台节点的 `/etc/rancher/k3s/config.yaml` 中都加入以下配置；文件已存在时必须保留其他字段，不要整个覆盖：

```yaml
resolv-conf: /etc/rancher/k3s/resolv.conf
```

从零安装时，后续 k3s server/agent 服务会在首次启动时读取该配置。
如果是给已运行的集群补配，在香港 worker 和 edge 分别执行：

```bash
sudo systemctl restart k3s-agent
```

在新加坡 control plane 执行：

```bash
sudo systemctl restart k3s
```

已存在的 Pod 不会自动重建 DNS sandbox。补配完成且四个节点恢复
`Ready` 后，重建 CoreDNS、cert-manager 和 Bosun Deployment：

```bash
kubectl rollout restart deployment/coredns -n kube-system
kubectl rollout restart deployment/cert-manager -n cert-manager
kubectl rollout restart deployment -n bosun-platform
```

使用了自定义 Tailscale ACL 时，要允许四个节点之间的流量。主机开启 UFW 时，可用下列简化规则允许 tailnet 入站：

```bash
sudo ufw allow in on tailscale0
```

云安全组不要向公网开放 k3s 的 `6443/tcp`、`8472/udp` 或 `10250/tcp`。它们只需在 Tailscale 内可达。只在 edge 节点的云安全组和 UFW 中向公网开放 `80/tcp` 和 `443/tcp`：

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
```

## 3. 安装新加坡 control plane

在新加坡机执行：

```bash
CONTROL_TS_IP="$(tailscale ip -4)"
curl -sfL https://get.k3s.io | sudo env INSTALL_K3S_CHANNEL=stable sh -s - server \
  --node-name node-sg-control \
  --node-ip "${CONTROL_TS_IP}" \
  --advertise-address "${CONTROL_TS_IP}" \
  --tls-san "${CONTROL_TS_IP}" \
  --flannel-iface tailscale0 \
  --node-label region=sg \
  --node-label role=core \
  --secrets-encryption
```

等待 server 就绪并记录实际安装的 k3s 版本：

```bash
sudo systemctl status k3s --no-pager
sudo k3s kubectl wait node/node-sg-control \
  --for=condition=Ready --timeout=180s
sudo k3s --version
sudo k3s secrets-encrypt status
sudo cat /var/lib/rancher/k3s/server/agent-token
```

`agent-token` 是 Secret，只复制到两台 agent 的临时会话，不写入仓库、笔记或 shell history。两台 agent 应使用与 server 相同的 k3s 版本；在下一节把 `<K3S_VERSION>` 替换为 `sudo k3s --version` 第一行中的 `v...+k3s...` 版本号。

## 4. 加入香港 worker 和 edge

在香港第一台大内存 worker 上执行：

```bash
export CONTROL_TS_IP='<control plane Tailscale IPv4>'
export K3S_VERSION='<K3S_VERSION>'
read -rsp 'K3s agent token: ' K3S_AGENT_TOKEN
echo
WORKER_TS_IP="$(tailscale ip -4)"
curl -sfL https://get.k3s.io | sudo env \
  INSTALL_K3S_VERSION="${K3S_VERSION}" \
  K3S_URL="https://${CONTROL_TS_IP}:6443" \
  K3S_TOKEN="${K3S_AGENT_TOKEN}" \
  sh -s - agent \
  --node-name node-hk-worker \
  --node-ip "${WORKER_TS_IP}" \
  --flannel-iface tailscale0 \
  --node-label region=hk \
  --node-label role=worker
unset K3S_AGENT_TOKEN
```

在香港第二台大内存 worker 上执行：

```bash
export CONTROL_TS_IP='<control plane Tailscale IPv4>'
export K3S_VERSION='<K3S_VERSION>'
read -rsp 'K3s agent token: ' K3S_AGENT_TOKEN
echo
WORKER_TS_IP="$(tailscale ip -4)"
curl -sfL https://get.k3s.io | sudo env \
  INSTALL_K3S_VERSION="${K3S_VERSION}" \
  K3S_URL="https://${CONTROL_TS_IP}:6443" \
  K3S_TOKEN="${K3S_AGENT_TOKEN}" \
  sh -s - agent \
  --node-name node-hk-worker-1 \
  --node-ip "${WORKER_TS_IP}" \
  --flannel-iface tailscale0 \
  --node-label region=hk \
  --node-label role=worker
unset K3S_AGENT_TOKEN
```

在香港 edge 上执行：

```bash
export CONTROL_TS_IP='<control plane Tailscale IPv4>'
export K3S_VERSION='<K3S_VERSION>'
read -rsp 'K3s agent token: ' K3S_AGENT_TOKEN
echo
EDGE_TS_IP="$(tailscale ip -4)"
curl -sfL https://get.k3s.io | sudo env \
  INSTALL_K3S_VERSION="${K3S_VERSION}" \
  K3S_URL="https://${CONTROL_TS_IP}:6443" \
  K3S_TOKEN="${K3S_AGENT_TOKEN}" \
  sh -s - agent \
  --node-name node-hk-edge \
  --node-ip "${EDGE_TS_IP}" \
  --flannel-iface tailscale0 \
  --node-label region=hk \
  --node-label role=edge
unset K3S_AGENT_TOKEN
```

回到 control plane，等待节点就绪，然后添加 edge taint 与 ServiceLB allow-list label：

```bash
sudo k3s kubectl wait nodes --all --for=condition=Ready --timeout=300s
sudo k3s kubectl taint node node-hk-edge \
  bosun.io/edge=true:NoSchedule --overwrite
sudo k3s kubectl label node node-hk-edge \
  svccontroller.k3s.cattle.io/enablelb=true --overwrite
sudo k3s kubectl get nodes \
  -o custom-columns=NAME:.metadata.name,INTERNAL-IP:.status.addresses[0].address,REGION:.metadata.labels.region,ROLE:.metadata.labels.role,TAINTS:.spec.taints
```

期望只有四个 `Ready` 节点，且它们的 `INTERNAL-IP` 是 Tailscale IP。

部署出 frontend 后，确认 Pod 已使用新 resolver。`search` 行中不应再出现
`localdomain` 或 Tailscale MagicDNS domain，外部域名也不应解析成
`*.localdomain`：

```bash
kubectl exec -n bosun-platform deployment/bosun-frontend -- \
  cat /etc/resolv.conf
kubectl exec -n bosun-platform deployment/bosun-frontend -- \
  getent ahostsv4 api.deepseek.com
```

## 5. 准备 DNS、Docker Hub 和 GitHub

1. 将 `bosun.amsors.com` 的 A 记录指向 `node-hk-edge` 的公网 IPv4。不要把 DNS 指向 Tailscale IP。
2. 在 Docker Hub 准备 `backend-api`、`gateway`、`operator`、`frontend`、`agent`、`egress-proxy` 六个公开 repository。公开 repository 让 k3s 节点不需要 registry credential。
3. 创建 Repository Variable `DOCKERHUB_NAMESPACE`，值为 Docker Hub 用户名或 organization。
4. 在 `registry` Environment 创建 `DOCKERHUB_USERNAME` 和只有 push 权限的 `DOCKERHUB_TOKEN` Secret。
5. 在 Repository Variables 中配置 `ACME_EMAIL`、`DEFAULT_PROVIDER_NAME`、`DEFAULT_PROVIDER_UPSTREAM_URL`、`DEFAULT_PROVIDER_AUTH_HEADER` 和 `DEFAULT_PROVIDER_AUTH_SCHEME`。使用 `x-api-key` 时，`DEFAULT_PROVIDER_AUTH_SCHEME` 必须是字面值 `__EMPTY__`。
6. 在 `production` Environment 创建 `DEFAULT_PROVIDER_API_KEY`、`JWT_PRIVATE_KEY` 和 `KUBECONFIG_B64` Secret。

首次可以在临时目录生成 JWT key 和可远程访问的 kubeconfig：

```bash
umask 077
OPS_SECRET_DIR="$(mktemp -d)"
openssl genpkey -algorithm ED25519 \
  -out "${OPS_SECRET_DIR}/jwt-private-key.pem"
CONTROL_TS_IP='<control plane Tailscale IPv4>'
sudo k3s kubectl config view --raw |
  sed "s#server: https://127.0.0.1:6443#server: https://${CONTROL_TS_IP}:6443#" \
  > "${OPS_SECRET_DIR}/kubeconfig"
```

将 `jwt-private-key.pem` 的原文作为 `JWT_PRIVATE_KEY`，将以下命令的单行输出作为 `KUBECONFIG_B64`：

```bash
base64 --wrap=0 "${OPS_SECRET_DIR}/kubeconfig"
```

设置 GitHub Secrets 后删除这个临时目录。不要在 Bosun 仓库目录生成这些文件。

生产 workflow 需要 `[self-hosted, linux, bosun-deploy]` runner。学生项目可直接把 runner 安装在新加坡 control plane 的独立非 root 账号下，按 GitHub `Settings → Actions → Runners` 页面当前显示的命令注册，并添加 `bosun-deploy` label。首次部署时以前台 `./run.sh` 运行即可。

## 6. 创建数据库 Secret

Helm 部署前在 control plane 执行：

```bash
sudo k3s kubectl create namespace bosun-platform \
  --dry-run=client -o yaml |
  sudo k3s kubectl apply -f -

umask 077
DATABASE_SECRET_DIR="$(mktemp -d)"
DATABASE_PASSWORD="$(openssl rand -hex 24)"
printf '%s' "${DATABASE_PASSWORD}" \
  > "${DATABASE_SECRET_DIR}/password"
printf 'postgres://bosun:%s@bosun-postgresql:5432/bosun?sslmode=disable' \
  "${DATABASE_PASSWORD}" > "${DATABASE_SECRET_DIR}/url"
sudo k3s kubectl -n bosun-platform create secret generic bosun-database \
  --from-file="password=${DATABASE_SECRET_DIR}/password" \
  --from-file="url=${DATABASE_SECRET_DIR}/url"
unset DATABASE_PASSWORD
```

确认 Secret 存在后删除临时目录。其余 namespace、CRD、RBAC 与 workload 均由生产 Helm chart 创建，不需要预先手工应用清单。

## 7. 首次发布与部署

1. 确认目标 commit 已在 `main` 且 `CI / ci-gate` 通过。
2. 手动运行 GitHub Actions `Publish images`，输入完整 40 位 commit SHA。
3. 在 Docker Hub 确认六个镜像都出现相同的七位 SHA tag。
4. 确认 `bosun-deploy` runner 在线，然后手动运行 `Deploy production`，输入同一个完整 SHA。

workflow 会完成以下初始化：

- 仅允许 `role=edge` 节点运行 ServiceLB；
- 将 Traefik 调度到 edge；
- 安装 cert-manager；
- 写入 JWT 和 provider Secret；
- 安装 CRD、RBAC、PostgreSQL 与全部 Bosun workload；
- 申请 Let's Encrypt 证书并验证 HTTP 到 HTTPS 跳转。

Bosun 自建的六个镜像只从 Docker Hub 推拉，不配置 registry mirror。k3s 内置系统组件和 cert-manager 仍使用它们的官方上游 registry；其中 cert-manager 的官方 chart 和镜像位于 Quay。为了让第三方组件也只经过 Docker Hub 而自行维护镜像副本会增加不必要的供应链成本，本项目不采用该方案。

## 8. 最终验收

在 control plane 执行：

```bash
sudo k3s kubectl get nodes --show-labels
sudo k3s kubectl get pods -A -o wide
sudo k3s kubectl get pvc -A
sudo k3s kubectl -n bosun-platform get certificate bosun-tls
sudo k3s kubectl get nodes \
  -l svccontroller.k3s.cattle.io/enablelb=true
curl -I http://bosun.amsors.com/healthz
curl -fsS https://bosun.amsors.com/healthz
```

验收标准：

- 节点全部 `Ready`；
- ServiceLB label 只出现在 `node-hk-edge`；
- Traefik 和 frontend 在 edge；
- PostgreSQL、API、gateway、operator 和 cert-manager 在 core；
- 新建 `AgentSession` 的 Pod 只在 `node-hk-worker`；
- Certificate 为 `Ready=True`；
- HTTP 返回指向对应 HTTPS URL 的永久重定向（`301` 或 `308`），HTTPS `/healthz` 返回成功。

如果节点加入失败，先检查 `tailscale ping <peer>`、control plane 的 `6443/tcp` 可达性以及 `journalctl -u k3s-agent`。如果 Pod 跨节点不通，检查 k3s Node `INTERNAL-IP` 是否为 Tailscale IP、`tailscale0` 是否存在，以及主机防火墙是否允许 Tailscale 内部流量。如果 cert-manager 报外部 HTTPS 证书过期，但节点上直接 `curl` 正常，检查 Pod `/etc/resolv.conf` 是否再次出现 `localdomain`，以及 `getent` 是否返回了 `<external-host>.localdomain`。

## 参考

- [K3s Quick-Start Guide](https://docs.k3s.io/quick-start)
- [K3s Requirements](https://docs.k3s.io/installation/requirements)
- [K3s Uninstalling](https://docs.k3s.io/installation/uninstall)
- [K3s Distributed hybrid or multicloud cluster](https://docs.k3s.io/networking/distributed-multicloud)
- [cert-manager Helm installation](https://cert-manager.io/docs/installation/helm/)
