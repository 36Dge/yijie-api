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
- PostgreSQL 16；
- `pgx/v5` 连接池；
- `goose` SQL Migration；
- Redis 7，目前只配置本地依赖，尚未进入业务主链路。

## 本地开发

先启动 `yijie-infra` 中的本地依赖：

```bash
cd ../yijie-infra
make dev-up
```

然后启动 API。`make dev` 会先执行待应用的 Migration，再启动服务：

```bash
cd ../yijie-api
make dev
```

验证服务和数据库：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

创建并查询任务：

```bash
curl -i -X POST http://localhost:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -H 'X-Request-ID: local-example-1' \
  -d '{
    "tenant_id": "9d1e1888-7ecf-42f6-8cbf-4d24101a801c",
    "task_type": "listing_diagnosis",
    "title": "Amazon Listing diagnosis",
    "input": {"url": "https://example.com/product"}
  }'

curl http://localhost:8080/v1/tasks/<task_id>
```

## 数据库命令

```bash
make migrate-up
make migrate-status
```

API 启动时只校验 Schema，不会自动修改数据库。生产环境应由独立发布任务执行 Migration。

## 测试

```bash
make test
make test-integration
make test-all
```

集成测试会在 PostgreSQL 中创建隔离的临时 Schema，测试结束后删除，不会清空本地开发数据。

## 安全要求

不得在日志、fixtures 或配置中提交真实 token、PII、订单和商家经营敏感数据。真实数据库 DSN 只能通过 Secret Manager 或运行环境注入。
