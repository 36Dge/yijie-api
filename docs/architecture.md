# Architecture

`yijie-api` 使用模块化单体。模块内部推荐分为：

- `domain/`
- `application/`
- `infrastructure/`
- `interfaces/`

当前首个持久化垂直切片位于任务模块：

- Public HTTP 接口负责创建和查询任务；
- Application 层负责输入约束和用例编排；
- PostgreSQL Repository 在同一事务中写入任务和审计日志；
- `pgxpool` 由进程统一创建和关闭；
- `goose` Migration 由独立命令执行，API 启动时只验证 Schema。
