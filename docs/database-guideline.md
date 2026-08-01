# Database Guideline

## 技术基线

- PostgreSQL 16 是业务主数据的唯一事实来源；
- Go 使用 `pgx/v5` 和 `pgxpool`；
- Schema 只能通过 `goose` SQL Migration 修改；
- Redis 只用于缓存、限流、幂等和短期协调，不保存业务唯一状态。

## Migration

- Migration 位于 `internal/platform/migrations/sql/`；
- 本地通过 `make migrate-up` 执行；
- API 进程不会在启动时自动迁移；
- CI 必须从空 Schema 执行 Migration 和真实 PostgreSQL 集成测试；
- Migration 不得包含真实商家数据。
- FEAT-125 migration v2 是 expand-only：新增 identity/tenant/RBAC 表并把审计 locator 从
  task-only 扩展为 UUID 或 text key；应用回退时保留 schema 并 roll-forward，禁止 destructive down。

## 事务边界

创建任务和写入对应审计日志必须在同一数据库事务中完成。任一写入失败时，整个事务回滚。
后续 membership、role、permission 状态写入同样必须与 `authorization_revision` 原子递增和
append-only audit 位于同一事务；S3 仅提供读取基础与 schema，不提供这些写入口。

## 审计约束

`audit_logs` 是只追加记录。数据库触发器禁止 `UPDATE` 和 `DELETE`；未来如需实现合规删除，必须通过独立 ADR 和受控 Migration 调整。

## 服务边界

Desktop 和 Admin Web 不得连接数据库。只有 `yijie-api` 及独立 Migration Runner 可以访问业务数据库。
