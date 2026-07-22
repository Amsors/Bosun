# Bosun operator

operator 使用 kubebuilder 和 controller-runtime 管理两个 CRD：

- `UserEnvironment`：创建用户 namespace、配额、默认资源限制、NetworkPolicy 和 backend RBAC；
- `AgentSession`：创建并维护 workspace PVC、ServiceAccount 和隔离的 coding agent Pod，处理休眠、恢复、超时与删除。

Agent Pod 必须调度到 `role=worker` 节点。平台 namespace、Agent 镜像、gateway、egress proxy 和 storage class 由启动参数注入；生产参数由根目录 Helm chart 管理。

## 开发命令

```bash
make test
make lint
make build
make manifests generate
```

修改 `api/v1alpha1/*_types.go` 或 kubebuilder RBAC marker 后，必须运行 `make manifests generate`，并提交生成的 CRD、RBAC 和 DeepCopy 结果。根目录 CI 会检查这些生成文件是否同步。

## CR 示例

先安装 CRD 并运行 operator，再创建用户环境：

```bash
kubectl apply -f config/samples/bosun_v1alpha1_userenvironment.yaml
kubectl wait userenvironment/userenvironment-sample \
  --for=jsonpath='{.status.phase}'=Ready \
  --timeout=2m
kubectl apply -f config/samples/bosun_v1alpha1_agentsession.yaml
```

AgentSession 示例位于 operator 创建的用户 namespace 中，因此不能在 UserEnvironment 变为 `Ready` 前应用。

## 部署方式

生产环境只使用根目录 `deploy/chart` 和 `.github/workflows/deploy.yml`，不要单独运行 `make deploy` 覆盖生产 release。operator Makefile 中的 `make install`、`make deploy`、`make undeploy` 和 `make test-e2e` 保留给隔离的开发或 Kind 集群使用。

本地完整联调请在仓库根目录执行 `make dev-up` 和 `make dev-smoke`，详细说明见 `deploy/local/README.md`。
