# AGENTS.md

## 仓库职责

`yijie-api` 是易界 AI 的核心业务后端。它管理租户、用户、RBAC、店铺授权、任务、审批、审计、插件注册、知识库版本和计费等业务主状态。

## 禁止事项

- 不直接实现外部平台 API 调用细节；
- 不维护 Codex Runtime 内部状态；
- 不编写 Skill prompt；
- 不实现 RAG chunking、embedding 或检索；
- 不在未更新 `yijie-contracts` 的情况下修改 API。

## 技术栈

Go、PostgreSQL、Redis。当前骨架只做占位，不连接真实数据库。

## 开发命令

```bash
make dev
make test
make lint
```

## 安全要求

token、PII、订单、买家信息和商家经营数据必须脱敏。高风险操作必须走审批和审计。
