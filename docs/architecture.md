# Architecture

`yijie-api` 使用模块化单体。模块内部推荐分为：

- `domain/`
- `application/`
- `infrastructure/`
- `interfaces/`

当前持久化模块包括：

- Public HTTP 接口负责创建和查询任务；
- Application 层负责输入约束和用例编排；
- PostgreSQL Repository 在同一事务中写入任务和审计日志；
- `identity` 以外部 `(issuer, subject)` 解析活动内部用户；
- `tenancy` 逐次验证 user、tenant 与 membership 均为 active，并返回
  `authorization_revision`；
- `authorization` 只接受批准的 7 个 capability，在 tenant-scoped RBAC 中 deny-by-default；
- `internal/platform/authn` 只验证固定 audience 的 RS256 access JWT 和受限 JWKS，不签发
  yijie session。

FEAT-125 S3 只建立上述基础能力，尚未把它们接入 Public HTTP handler。生产 IdP、endpoint
和 feature activation 由后续 gate 管理。

共享设施：

- `pgxpool` 由进程统一创建和关闭；
- `goose` Migration 由独立命令执行，API 启动时只验证 Schema。
