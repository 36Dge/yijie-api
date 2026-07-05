# yijie-api

Go 业务后端，MVP 阶段采用模块化单体。

## 仓库职责

- 用户认证、租户、RBAC；
- 店铺授权业务；
- 会话、任务和 Agent session 元数据；
- 审批策略、审计日志、Webhook；
- 插件和知识库版本注册；
- 与 `yijie-agent-host`、`yijie-connectors`、`yijie-knowledge` 做服务间调用。

## 技术栈

- Go；
- PostgreSQL，占位未接入；
- Redis，占位未接入；
- 标准库 HTTP 最小服务。

## 本地开发

```bash
make dev
curl http://localhost:8080/healthz
```

## 测试

```bash
make test
```

## 安全要求

不得在日志、fixtures 或配置中提交真实 token、PII、订单和商家经营敏感数据。
