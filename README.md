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
缺少 G3-NP-LOCAL 验证、后续 G5 生产配置和发布批准时不得把这些模块视为生产启用。

`YIJIE_API_SERVICE_PROFILE=feat-125-local-lab` 是仅允许 `local` 或 `nonproduction`
环境使用的封闭集成 profile。该 profile 不注册 legacy `POST /v1/tasks` 和
`GET /v1/tasks/{task_id}` handler，并强制 API 仅监听 `127.0.0.1:<YIJIE_API_PORT>`，监听
host 不能由环境变量放宽；默认 profile 保持既有 `:<port>` 监听行为和 Tasks wire contract
不变。

`YIJIE_API_SERVICE_PROFILE=feat-126-s10-local-lab` 是 FEAT-126 S10 专用的封闭本地运行
profile，只能由 `yijie-infra` 的版本化 runtime authority 启动，不能作为日常开发或生产开关。
它要求 `YIJIE_ENV=nonproduction`、permission projection 与 secure Tasks 均精确为 `true`，
并把 PostgreSQL 锁定到带非空凭据的
`127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable`。issuer、JWKS、local CA path/pin、
canonical TCP port、IPv4 loopback bind 和 legacy `/v1/tasks` 隔离同样 fail closed；任一缺失、
漂移、通用/未知 profile 或额外 DSN query 都会在数据库连接前被拒绝。该 profile 不改变
`feat-125-local-lab`、默认配置或 production 行为。

`feat-125-local-lab` 开启 permission projection 时，还会锁死 issuer 与 JWKS 为本地 Keycloak 的精确 HTTPS
地址，并要求同时设置 `YIJIE_API_LOCAL_CA_PEM_PATH`（绝对路径）与
`YIJIE_API_LOCAL_CA_SHA256`（小写 SHA-256）。API 只为这一条 JWKS 出站链路创建独立、禁用代理与
redirect 的 TLS client；CA 必须是 owner-only `0400`/`0600`、不超过 64 KiB 的单一 public CA
certificate。默认 profile、production 环境或未开启 projection 时出现这两个 local-only 变量都会
fail closed；它们不会修改系统或用户 Keychain，也不能关闭 TLS 校验。

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

### FEAT-125 synthetic nonproduction bootstrap

仓库提供一个只允许 `YIJIE_ENV=nonproduction` 的事务化 bootstrap。它要求 manifest issuer
与 `YIJIE_API_ACCESS_ISSUER` 精确相等，并创建或核验一个
synthetic user identity、tenant、active membership、`tenant_owner`/`tenant_member` 两个
system roles、精确权限集合与指定角色 assignment；重复执行不会重复业务状态。通过 CLI 的环境、
manifest、issuer、migration 与数据库连接前置校验并进入事务化 Runner 后，每次执行都会写
append-only audit；前置校验失败不会被表述为已有数据库审计。授权状态发生变化时，同一事务内将 tenant
`authorization_revision` 原子递增一次，并在 audit 中记录新 revision 和有界授权 diff。
状态漂移、角色切换、生产环境、issuer 不匹配、真实命名或未声明 `synthetic_only` 时 fail
closed，issuer/subject 不进入输出或 audit metadata；failure audit 写入失败会作为独立稳定
错误分类返回，不会被静默忽略。若数据库可能已提交但客户端丢失 commit 确认，Runner 会按精确
request ID 和预期审计内容对账：确认 success audit 后返回成功；无法确认时返回稳定
`outcome unknown` 且不追加可能与成功结果矛盾的 failure audit。此时必须先按输出的 request ID
人工对账，禁止直接重试。

先复制示例为被 Git 忽略的 local manifest，并替换为真实非生产 IdP 中创建的 synthetic
用户 subject 和批准的固定 UUID：

```bash
install -m 600 config/nonproduction/feat-125-bootstrap.example.json \
  config/nonproduction/feat-125-bootstrap.local.json
YIJIE_ENV=nonproduction \
YIJIE_API_ACCESS_ISSUER='https://localhost:8443/realms/yijie-local' \
YIJIE_API_POSTGRES_DSN='<Secret Manager injected DSN>' \
make bootstrap-nonprod-authz INPUT=config/nonproduction/feat-125-bootstrap.local.json
```

提交的 example 使用 G3-NP-LOCAL 固定的 loopback issuer；执行时必须使用与本地 IdP online
preflight 完全相同的 issuer，并登记 success audit request ID、authorization revision 和 diff。
该证据不能替代未来 G5/G6 的生产环境验证。

G3-NP-LOCAL 必须显式传入 `BOOTSTRAP_PROFILE=feat-125-local-lab`。该 profile 在读取 manifest、
检查 migration 或建立数据库连接之前，锁死以下边界：

- issuer 精确为 `https://localhost:8443/realms/yijie-local`；
- PostgreSQL URL 精确使用 `postgres` scheme、`127.0.0.1:5432`、数据库
  `yijie_api_feat125_local`，且唯一 query 为 `sslmode=disable`；
- DSN 必须包含运行时注入的非空用户名和密码，但任何错误和输出都不会回显 DSN；
- manifest 内容只能是仓库中固定的两个 synthetic subject × 两个 synthetic tenant 的四个
  精确角色 tuple，任意用户、租户、名称、角色或 actor 漂移都会在数据库访问前拒绝；
- 未传 profile 时保留上述通用 nonproduction bootstrap；任何未知或拼写错误的显式 profile
  都会 fail closed。

固定的两用户 × 两租户合成矩阵位于
`config/nonproduction/feat-125-local-lab/`，四份 manifest 均为受测试约束的仓库文件，不得复制
为真实用户或真实租户数据。准备好独立本地数据库并显式执行 migration 后，逐份执行：

```bash
export YIJIE_ENV=nonproduction
export YIJIE_API_ACCESS_ISSUER='https://localhost:8443/realms/yijie-local'
export YIJIE_API_POSTGRES_DSN="$FEAT125_LOCAL_API_DSN"
for manifest in config/nonproduction/feat-125-local-lab/*.json; do
  make bootstrap-nonprod-authz \
    BOOTSTRAP_PROFILE=feat-125-local-lab \
    INPUT="$manifest" || exit 1
done
```

`FEAT125_LOCAL_API_DSN` 必须由本机 ignored secrets 注入并满足上述精确结构；禁止把 credential
写入仓库、命令输出或日志。该 CLI 只核验 migration version，不会自动执行 migration。

## 测试

```bash
make test
make test-integration
make test-all
```

集成测试会在 PostgreSQL 中创建隔离的临时 Schema，测试结束后删除，不会清空本地开发数据。

## 安全要求

不得在日志、fixtures 或配置中提交真实 token、PII、订单和商家经营敏感数据。真实数据库 DSN 只能通过 Secret Manager 或运行环境注入。
