# 贡献指南

需要 Go 1.25.9+ 和 Git。测试创建临时资料仓与 SQLite，不需要生产凭据、数据库或第三方模型账号。

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
python3 scripts/smoke.py
```

修改权限、任务状态或数据写入时，先加失败回归，再修实现。验证错误请求的数据库副作用，而不是只断言 HTTP 状态码；并发撤权和重复提交也属于验收范围。

## 代码位置

| 目录 | 职责 |
|---|---|
| `cmd/aspace-server` | 服务启动与本地 key 管理 |
| `internal/server` | HTTP API、身份和业务授权 |
| `internal/web` | 服务端 HTML 与网页交互 |
| `internal/db` | SQLite、事务、协作状态 |
| `internal/store` | 资料目录与 Git 操作 |
| `internal/model` | Markdown 内容模型 |
| `internal/agents` | 角色名册与身份映射 |
| `templates/memory` | 不含个人数据的初始化模板 |

不要提交 key、Cookie、运行数据库、私人资料、备份或个人服务配置。安全问题不要在公开 issue 中附带可用凭据或真实用户数据；遵循 [SECURITY.md](SECURITY.md)。
