# Bosun — coding agent 托管与算力调度平台

Bosun 是一个基于 k3s 的 coding agent（Claude Code 等）托管平台：用户在网页上创建会话，平台在跨区集群（新加坡/中国/香港）中调度出一个隔离的 agent 容器，通过 web 终端交互，按资源与 token 用量计量。

> 根目录的 `AGENTS.md` 是指向本文件（`CLAUDE.md`）的**符号链接、内容完全一致**——只因 Claude Code 默认读取 `CLAUDE.md`，而部分其他 coding agent 读取 `AGENTS.md`。编辑任意一个另一个即同步；若被编辑器断链，用 `ln -sf CLAUDE.md AGENTS.md` 重新建立。
>
> **开发文档在 `dev_docs/`**——一个独立 git 仓库（`git@github.com:Amsors/Bosun-dev-docs.git`），以 submodule 形式挂载。`docs/` 则是面向**最终用户**的产品文档，随主仓库走。

## 每次开发前必须做的事（硬性要求）

1. **通读 `dev_docs/spec/` 下所有编号文档**（00–05）。spec 是硬性规范，任何代码和文档产出都必须遵守；与 spec 冲突的做法一律不允许，除非先修改 spec（见 spec/00 的变更流程）。
2. **读 `dev_docs/techspec.md` 中与本次任务相关的章节**，技术方案选择必须遵循其中的「技术决策表」；表中每项已标注首选方案，未经决策变更流程不得擅自换用备选方案。
3. **检查 `dev_docs/guide/` 是否有与本次任务相关的、状态为 `active` 的指导文档**。有则必须遵守。
4. 如果开发者要求为一个新功能的开发撰写文档，则按 `dev_docs/guide/_template.md` 创建。如果开发者只要求创建文档而未要求开发，则不要自行开始开发。

## 每次开发完成后必须做的事（硬性要求）

1. 按 `dev_docs/record/_template.md` 在 `dev_docs/record/` 写一份开发记录，文件名 `YYYY-MM-DD-<slug>.md`。
2. 若本次任务有对应的 guide，**归档进 record**（见 dev_docs/spec/00 §3–§4）：把其 frontmatter 的 `status` 改为 `done`、回填 `related-record`，再将该 guide 移动到 `dev_docs/record/` 并改名 `YYYY-MM-DD-<slug>.guide.md`，**不在 `dev_docs/guide/` 保留**。
3. 若开发中做出了新的技术选型或推翻了旧决策，更新 `dev_docs/techspec.md` 的决策表，并在 record 中说明原因。
4. 若改动了 `dev_docs/` 内容：先在 `dev_docs/` 提交并推送，再回主仓库提交更新后的 submodule 指针。

## 文档地图

开发文档在 `dev_docs/` submodule 内；面向用户的产品文档在主仓库 `docs/`。

| 路径 | 性质 | 说明 |
|---|---|---|
| `dev_docs/spec/` | 硬性规范，长期有效 | 每次开发必读必遵守，变更需评审 |
| `dev_docs/guide/` | 临时指导，功能级 | 仅存当前 `active` 的方案；开发完归档进 record |
| `dev_docs/record/` | 归档记录，只增不改 | 开发记录 + 归档的 guide，供追溯 |
| `dev_docs/deploy_record/` | 部署实录，持续更新 | 保存实际部署指导；由 `status.md` 引用当前与已完成配置 |
| `dev_docs/PRD.md` | 产品需求 | 做什么、为谁做、优先级 |
| `dev_docs/techspec.md` | 技术方案 | 架构、技术决策表（含首选标注）、数据模型、关键流程 |
| `dev_docs/plan.md` | 开发计划 | 里程碑、任务表                                     |
| `docs/` | 用户文档 | 面向最终用户的产品文档，不属于开发文档三层体系 |

## 技术栈速览（详见 techspec 决策表）

- 后端 / Operator：Go + Gin + kubebuilder（`AgentSession` CRD）
- 前端：Vue 3 + Vite + TypeScript，纯静态 SPA，部署于香港节点
- 数据层：PostgreSQL 单实例（新加坡 core 节点）+ 阿里云 OSS（P1）；Redis 延后到 P2
- Agent 运行时：固定版本 Claude Code CLI +  Anthropic-compatible endpoint
- 集群：k3s over Tailscale；使用中国可访问的镜像源

## 仓库结构（目标形态）

```
Bosun/
├── CLAUDE.md             # ← AGENTS.md 符号链接指向它，内容一致
├── AGENTS.md
├── .gitmodules
├── docs/                 # 面向最终用户的产品文档
├── dev_docs/             # submodule → Bosun-dev-docs（含 spec/guide/record/deploy_record 等）
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



## 行为约束摘要（完整版见 spec）

- 提交信息用 Conventional Commits；Commmit Message使用英文编写。
- 完成开发后默认不进行提交操作，需要询问开发者的意见。
- 任何密钥、token、kubeconfig 一律不得写入仓库（见 spec/05）。
- 所有 k8s 工作负载必须声明 requests/limits；镜像禁止使用 `:latest`（见 spec/04）。
- 文档一律中文，技术名词保留英文；凡列出多个可选方案，必须选定一个标注「首选」。
