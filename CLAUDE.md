# Bosun — coding agent 托管与算力调度平台

Bosun 是一个基于 k3s 的 coding agent（Claude Code 等）托管平台：用户在网页上创建会话，平台在跨区集群中调度出一个隔离的 agent 容器，通过 web 终端交互。

**当前项目为学生课程项目，不是生产级项目**，因此：

- 无需施加过于严格以至于极大增加实现成本的安全约束
- 允许开发过程中的用户数据丢失

## 技术栈速览

- 后端 / Operator：Go + Gin + kubebuilder（`AgentSession` CRD）
- 前端：Vue 3 + Vite + TypeScript，纯静态 SPA，部署于edge节点
- 数据层：PostgreSQL 单实例（新加坡 core 节点）
- Agent 运行时：固定版本 Claude Code CLI +  Anthropic-compatible endpoint
- 集群：k3s over Tailscale；业务镜像发布到 Docker Hub

## 仓库结构

```
Bosun/
├── CLAUDE.md
├── AGENTS.md
├── docs/                 # 面向最终用户的产品文档
├── backend/              # Go 平台后端（API、LLM 网关、计量）
├── operator/             # kubebuilder 工程，AgentSession CRD 与控制器
├── frontend/             # Vue 3 SPA
├── images/               # agent 运行时等 Dockerfile
├── e2e/                  # 跨组件真实 k3s smoke 与 E2E
└── deploy/               # Helm chart 与集群配置
```

## 常用命令

本地完整联调使用 Docker 中的 k3d 三角色集群；真实 provider 配置按 `deploy/local/README.md` 从 shell 环境变量注入：

```bash
make dev-up
make dev-build COMPONENT=frontend
make dev-deploy
make dev-forward
BOSUN_E2E_PASSWORD='<test-only-password>' make dev-smoke
make dev-reset
make dev-down
```

模块自身的 lint/test/build 命令见各模块 README 或 Makefile；本地集群的依赖、Secret 注入与调度验证见 `deploy/local/README.md`。

## 行为约束摘要

- 提交信息用 Conventional Commits；Commmit Message使用英文编写。
- 任何密钥、token、kubeconfig 一律不得写入仓库。
- 文档一律中文，技术名词保留英文。


## agent 调试指南

如果需要实际连接到集群进行调试，请使用项目根目录下的 `tmp_kubeconfig` 文件作为临时 kubeconfig 文件，请在命令中引用其路径，但不要直接读取这个文件，调试后也不需要删除这个文件，此文件由用户管理。

如果没有发现 `tmp_kubeconfig` 文件，则无法也不需实际连接到集群进行调试

## CI检查指南

当完成某个任务的完整开发后，告知用户开发完成，并提醒用户在commit之前运行  `./development/ci.sh` 中的 CI 测试。提醒用户，但不要主动执行 CI 测试。

