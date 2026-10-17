# 从零运行

一份公开代码仓库足够。下面创建的是你自己机器上的私有运行目录，不需要公开 shared 仓库，也不接任何真实模型账号。

## 1. 准备程序与数据

按 [README 的本地运行命令](../README.md#本地运行) 完成编译、复制模板、初始化资料目录、发放 owner/my-agent 两把 key，再前台启动服务。

- 系统：macOS 或 Linux；依赖 Go 1.25.9+、Git。
- 访问地址：http://127.0.0.1:8787/d/
- owner key 用于本人网页登录；my-agent key 用于 Agent HTTP 请求。不要互换或复用。
- 服务启动时加载 key 配置，运行中增加/撤销身份后需要重启。
- 如果 Go 提示标准库与编译器版本不一致，检查 `go version`、`go env GOROOT`，确保使用同一套工具链。

`var/` 已被忽略，但请始终检查 Git diff，避免通过其他文件名提交资料或凭据。不要在已有的 `var/memory` 上再次覆盖复制模板。

## 2. 在网页建群

1. 用 owner key 登录。
2. 打开“群组”，选择“新建群组”。
3. 事项选 `welcome`，成员选 `my-agent`。
4. 填一个具体问题，例如“根据这个事项的目标，给我下一步建议”，并 @ `my-agent`。

群成员不等于模型进程：创建身份、登记角色不会自动启动一个 AI。你需要让实际 Agent 执行 [领取任务 → 读取上下文 → 提交回复](agent-onboarding.md) 的流程。

## 3. 检查闭环

- Agent 的 `/v1/whoami` 返回 `my-agent`。
- `/v1/wakeups` 能看到刚才的任务。
- claim 后的 finish 返回成功，网页群里出现回复。
- finish 已发送回复，不要再单独 post 同一条消息。

## 4. 写入自己的资料

编辑 `var/memory/items/welcome/CONTEXT.md`，或者新增事项目录。角色/名册读取已提交版本，因此编辑后需要提交本地资料仓：

```sh
git -C var/memory add .
git -C var/memory -c user.name=Owner -c user.email=owner@example.invalid commit -m "Update private context"
```

这里的 `git add` 仅作用于你的私有资料目录，不要在公开源码仓执行 `git add var -f`。模板只是结构示例，不含模型回复、个人档案或在线演示数据。

## 5. 停止与升级

前台按 Ctrl-C 停止。程序不注册开机自启。升级前分别备份私有 Git 资料仓和 SQLite，再停进程、替换二进制、重新启动；网页 session 会失效，需要重新登录。

跨设备访问、TLS、Git 私有远端和备份见 [部署与安全](deployment.md)。
