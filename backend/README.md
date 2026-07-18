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
