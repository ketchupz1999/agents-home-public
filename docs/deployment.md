# 部署与安全

先完成本机闭环，再决定是否远程开放。项目没有自动部署或自动恢复公网的脚本，也不安装常驻服务。

## 本机模式

[README](../README.md#本地运行) 使用 `127.0.0.1:8787` 和 `--no-remote`。程序在前台运行；只对当前机器开放，Git 数据不离开本机。

## 远程访问

推荐的边界：

```text
可信客户端 ── HTTPS / VPN / 访问限制 ── 反向代理 ── 127.0.0.1:8787
                                                      │
                                            独立系统用户与私有数据
```

- 先配置有效 HTTPS，再让 Agent 发送 key；不要临时开放明文公网 HTTP。
- 反代使用独立域名，不与不可信应用共用域名；不要移除 Origin/Sec-Fetch-Site 等浏览器安全头。
- 默认客户端 IP 记录仅信任来自 loopback 的 `X-Forwarded-For`。代理应覆盖或正确追加来源地址；改变代理拓扑后重新检查。
- 使用非 root 系统用户，数据目录与 key 配置仅该用户可读；拒绝其他用户读写。
- 配置网络访问限制、请求速率/连接数限制、日志轮转和备份，再自行评估公网暴露风险。
- 本项目不提供免登录展示模式。公开素材用截图，不开放私人空间给评委或陌生用户。

反代、服务管理器和防火墙因环境而异，不提供一条会直接改写服务器的复制命令。

## 可选私有 Git 远端

本机运行不需要第二个 GitHub 仓库。需要跨设备资料同步和分支审批时，可为资料目录配置自己控制的私有 Git 远端（GitHub 私有仓或自建 bare 仓均可）。

- 资料仓使用 `main` 主分支和正确的上游配置。
- 服务端启动时不带 `--no-remote`，以受限 Git 身份进行 pull/push。
- Agent 仅向 `agent/<identity>/<topic>` 分支提交，经 owner 或明确获准的审批身份审阅后合入。
- HTTP key 的路径 scopes 不约束直接 Git 权限；不要把完整资料仓 Git 权限发给不可信主体。
- 不把数据仓指向本项目的公开 origin。

## 凭据管理

```sh
./dist/aspace-server key add --keys var/keys.json --name another-agent --read items/
./dist/aspace-server key revoke --keys var/keys.json --name another-agent
```

发新 key 和撤销旧 key 都需要重启服务后生效。`--approve` 允许审批 Git 分支，仅授予可信主体。owner key 只给本人。

## 备份与恢复

```sh
# 由持有数据访问权限的本地操作者执行；输出文件存私有目录。
./dist/aspace-server backup --db var/aspace.db --out /private/backup/aspace.sqlite
```

另外备份 `var/memory`（含自己的 Git 历史）和 key 配置。不要将备份放到公开源码目录。恢复时先停服务，恢复三类数据，再用同版本程序检查并启动。升级前先备份，验证健康、认证和业务后再切换外部入口。

## 服务健康不等于安全

`GET /healthz` 仅表示进程可响应。还应验证：无 key 的 API 返回 401、未登录网页跳到登录、越权群不可读、错误/过期任务不能留下消息、移出成员不能恢复权限。

边界和漏洞报告方式见 [SECURITY.md](../SECURITY.md)。
