# AGENTS.md

## 适用范围

本文件适用于 `yijie-api` 整个仓库。目录中若出现更具体的 `AGENTS.md`，修改对应目录时以更具体的规则为准。

## 仓库职责与当前状态

`yijie-api` 是易界 AI 的核心业务后端，管理租户、用户、RBAC、店铺授权元数据、任务、审批、审计、插件注册、知识库版本和计费等业务主状态。

当前已落地的最小闭环包括：

- Go module `github.com/36Dge/yijie-api`；
- pgx v5/pgxpool PostgreSQL 连接；
- goose migration 入口及 `tasks`、`audit_logs` 首个迁移；
- tasks 模块的 domain、application、PostgreSQL infrastructure 和 HTTP interface 分层；
- task 与 audit log 的事务写入及 audit append-only 约束；
- 从相邻 `yijie-contracts` 生成的公共 API 类型；
- Redis 连接配置占位，但尚未用于业务流程。

`cmd/admin-server`、`cmd/scheduler`、`cmd/webhook-server`、`cmd/worker` 和部分模块目录目前仍是占位，Codex 不得假定其能力已经实现。

## 仓库边界

- 不直接实现 Amazon、Temu、Shopee、TikTok Shop 等平台 API 调用细节，该职责属于 `yijie-connectors`；
- 不维护 Codex Runtime 内部状态，该职责属于 `yijie-codex` 和 `yijie-agent-host`；
- 不编写 Skill prompt，不实现 RAG chunking、embedding 或检索引擎；
- 不把数据库模型或传输 DTO 当作领域模型到处复用；
- 不在未更新 `yijie-contracts` 的情况下改变对外 API；
- 不把 Redis 用作业务主状态或审计真相源。

## 模块与依赖方向

采用模块化单体优先。业务能力位于 `internal/modules/<module>/`，并遵循以下依赖方向：

```text
interfaces -> application -> domain
                  ^
                  |
            infrastructure
```

- `domain/`：实体、值对象、领域错误和端口，不依赖 HTTP、pgx、Redis 或生成 DTO；
- `application/`：用例、事务边界和端口编排，不包含 SQL 或 HTTP 细节；
- `infrastructure/`：实现 repository、外部客户端和持久化端口；
- `interfaces/`：HTTP/worker 输入输出、认证上下文映射和 DTO 转换；
- `internal/platform/`：数据库、migration、日志等共享技术设施；
- `internal/app/`：组装依赖和进程生命周期，不承载领域规则；
- `cmd/`：保持轻量，只负责配置、启动和退出。

跨模块调用优先通过明确的 application 端口，不直接访问另一个模块的表或 infrastructure 实现。新增共享抽象前先确认它确实属于平台层，而不是某个业务模块。

## Contract First

- 对外 API 的源文件位于相邻 `yijie-contracts`，先更新并评审契约，再执行 `make generate`；
- `internal/interfaces/publicapi/types.gen.go` 是生成文件，禁止手工编辑；
- 生成后检查 git diff，确保实现、测试和文档与契约一致；
- 新增 required 字段、删除字段、收窄类型或改变错误语义前，必须确认兼容策略和版本方案；
- handler 负责传输 DTO 与应用输入输出之间的显式转换，不让生成 DTO 渗透到 domain。

## PostgreSQL 与 Migration

- PostgreSQL 是业务主状态来源，所有 schema 变化必须使用 goose migration；
- 活跃 migration 路径是 `internal/platform/migrations/sql/`，不要把迁移写入仓库顶层占位目录；
- 应用二进制不在启动时隐式修改 schema；本地 `make dev` 通过 `make migrate-up` 显式迁移后启动；
- migration 必须有稳定编号、可在空库执行，并考虑线上滚动发布时的前后版本兼容；
- 删除列、重写大表、数据回填、不可逆迁移或生产数据修复必须先取得用户确认和独立执行方案；
- 多表一致性和 audit 写入使用显式事务，失败时整体回滚；
- audit log 保持 append-only，不提供无审计的更新或删除捷径；
- SQL 和 pgx 细节仅存在于 infrastructure/platform 层，测试覆盖成功、冲突、回滚和租户隔离。

Redis 当前仅有配置占位。引入缓存、锁、队列、限流或幂等键之前，必须先确认具体用途、失效策略、容灾语义和 PostgreSQL 真相源关系。

## 身份、租户与安全

- 租户、用户和角色最终必须来自受信任的认证上下文，不能长期依赖客户端任意提交的 `tenant_id`；
- 当前本地接口仍处于基础闭环阶段，不得据此自行发明认证、会话、角色或多租户方案；
- repository 查询必须显式带租户边界，避免跨租户读取、更新或审计串线；
- 高风险操作需要服务端授权、审批、幂等和 append-only 审计；
- token、DSN、cookie、PII、订单、买家信息和完整商家数据不得出现在日志、错误响应、测试夹具或提交记录中；
- 对外错误使用稳定错误码和安全文案，内部错误保留可观测上下文但不泄露秘密。

## 必须先确认的决策

以下事项信息不足时必须停止并询问用户，不自行选择：

- 数据库 schema、字段语义、索引、保留期、迁移和生产回填方案；
- 认证、会话、租户识别、RBAC、审批和审计策略；
- Redis 的具体用途及一致性、失效和故障降级语义；
- 新增数据库、中间件、基础设施、外部依赖或跨模块共享抽象；
- API 破坏性变化、版本策略和跨仓库发布顺序；
- 连接器、Runtime、Knowledge 或其他服务的同步/异步调用边界。

## 开发与验证

```bash
make lint             # gofmt 检查和 go vet
make test             # 单元测试、race 和覆盖率
make migrate-status   # 查看本地迁移状态
make migrate-up       # 显式应用本地迁移
make test-integration # 需要测试 PostgreSQL
make test-all         # 单元测试和 PostgreSQL 集成测试
make generate         # 需要相邻 yijie-contracts
make dev              # 迁移后启动本地 API
```

- 纯领域或应用逻辑至少执行 `make lint && make test`；
- repository、事务、SQL 或 migration 改动必须执行 `make test-all`，并验证空库迁移；
- 契约改动必须执行 `make generate`，并在相关仓库执行契约兼容性检查；
- 配置、认证、安全或进程生命周期改动需要补充失败路径测试；
- 测试依赖只能连接明确的本地/CI 测试数据库，禁止对未知 DSN 执行 migration 或集成测试。

## 完成标准

- 模块边界和依赖方向正确，domain 不依赖传输或持久化细节；
- 对外行为由契约驱动，生成文件未被手工修改；
- schema 变化有 migration、事务策略、兼容性说明和集成测试；
- 租户隔离、授权、审批、幂等和审计要求得到满足或明确标记为未实现；
- lint、race 单元测试及与改动相关的集成测试通过；
- 未执行的数据库、契约、认证或跨仓库验证在交付说明中如实列出。
