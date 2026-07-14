# Local Development

```bash
cd ../yijie-infra
make dev-up

cd ../yijie-api
make migrate-status
make dev
```

默认连接本地 PostgreSQL：

```text
postgres://yijie:yijie@localhost:5432/yijie_api?sslmode=disable
```

可通过环境变量 `YIJIE_API_POSTGRES_DSN` 覆盖。真实凭据不得写入仓库。
