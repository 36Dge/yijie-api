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

## FEAT-125 权威权限投影候选

当前代码包含未生产激活的身份、租户与授权候选：

- contracts `9ec34abd6e7dfb5a23b0154d467694167224ebbb` exact pin 和生成漂移检查；
- 固定 audience `https://api.yijie.ai` 的 RS256 access-JWT/JWKS verifier；
- `(issuer, subject)` 到内部用户、活动租户 membership 和 PostgreSQL RBAC；
- goose v2 expand migration，不创建 API session，也不写真实用户/租户。
- 默认关闭的 `GET /v1/me/tenants` 与 `GET /v1/me/capabilities`；
- 逐请求 bearer、内部用户、active membership/tenant 校验，以及一次一致性读取形成的
  allow-only capability projection；
- canonical contract fixture producer conformance、稳定错误矩阵和有界标签的进程内
  projection metrics recorder。Exporter、dashboard 与告警仍属于 G5。

`YIJIE_API_PERMISSION_PROJECTION_ENABLED` 默认是 `false`，因此服务不会注册两个 access
endpoint。只有显式开启时才要求同时提供 `YIJIE_API_ACCESS_ISSUER` 与
`YIJIE_API_ACCESS_JWKS_URL`；仓库没有任何生产 IdP、issuer、client ID 或 JWKS 值。
缺少后续 G3/G5 配置、staging 验证和发布批准时不得把这些模块视为生产启用。

从固定 contracts checkout 重生成并校验：

```bash
make generate-check CONTRACTS_DIR=../yijie-contracts
```

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
