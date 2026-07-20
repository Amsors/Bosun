# Bosun P0 E2E

本目录保存跨 backend、operator、frontend 与 deploy 的真实 k3s 验收脚本。

运行前设置 `KUBECONFIG` 和 `BOSUN_BASE_URL`；测试密码仅通过 `BOSUN_E2E_PASSWORD` 环境变量注入。脚本创建唯一测试用户，结束后按管理标签检查孤儿资源。测试用户的业务记录由后端 soft delete 语义保留，不包含真实身份信息。

```bash
KUBECONFIG=/path/to/kubeconfig \
BOSUN_BASE_URL=http://127.0.0.1:18080 \
BOSUN_E2E_PASSWORD='<test-only-password>' \
./e2e/smoke-a.sh
```

`smoke-b.sh` 接收 smoke A 输出的 access token 创建 small session，等待进入 `Running` 后删除，并检查会话 Pod、PVC 与 CR 均已清理。
