# Architecture

`yijie-api` 使用模块化单体。模块内部推荐分为：

- `domain/`
- `application/`
- `infrastructure/`
- `interfaces/`

当前初始化只提供最小 HTTP 服务和健康检查。
