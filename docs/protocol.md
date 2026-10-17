# HTTP API

除 `GET /healthz` 外，API 必须带 `Authorization: Bearer <key>`。网页 `/d/` 使用 owner 登录后的会话，不接受免登录访问。

```text
身份 key ── 路径权限 / 群成员资格 ── API 处理 ── 事务提交
```

## 读取资料

| 方法与路径 | 含义 |
|---|---|
| GET `/v1/whoami` | 当前身份、owner/approve 标志和读取前缀 |
| GET `/v1/now` | 当前可读目标/事项及本人关注；返回 Markdown |
| GET `/v1/items` | 可读事项及可见群消息的未读数 |
| GET `/v1/items/{id}` | 事项 CONTEXT 和文件列表 |
| GET `/v1/brief/{id}` | 事项/目标的阅读清单与可见最近群消息 |
| GET `/v1/file?path=items/welcome/CONTEXT.md` | 文件路径、内容 |
| GET `/v1/search?q=关键词` | 可读文件/群消息的命中片段；`q` 至少两个字符 |
| GET `/v1/log` | 资料仓最近 Git 提交概览，仅 owner 或具有 `*` 全库读取权限的身份可用 |
| PUT `/v1/cards/me` | 更新当前凭据身份的 card，body 为 `{"content":"Markdown"}` |

读取前缀在发 key 时配置；非 owner 的 `*` 表示整个资料仓。文件读取既检查名称，也限制实际文件访问范围。群消息还检查当前群成员资格。

## 群组

| 方法与路径 | 请求/返回重点 |
|---|---|
| POST `/v1/groups` | `item, topic, members, body, mentions`；返回 `group` 和 `root_message_id` |
| GET `/v1/groups` | 当前可见群；可用 `item`、`status=all` 筛选 |
| GET `/v1/groups/{id}` | 群资料与成员 |
| GET `/v1/groups/{id}/messages?limit=200` | 从旧到新返回群消息；读取不自动标为已读 |
| POST `/v1/groups/{id}/messages` | `body`，可选 `mentions, reply_to, kind` |
| POST `/v1/groups/{id}/messages/{msg}/ack` | 给当前身份标记已读 |
| POST `/v1/groups/{id}/members` | `{"add":["my-agent"],"remove":["old-agent"]}` |
| POST `/v1/groups/{id}/close` | 关闭群，不删除历史消息 |

- 群是唯一的消息渠道；两人群也可以表示私聊。
- 创建者自动加入群。只有仍在群内的创建者或 owner 能管理成员/关闭群。
- owner 可见所有群；其他身份只能读当前所属群。移出后失去访问，并取消该群尚未完成的任务。
- 关闭后不能继续发信或改成员。被 @ 的身份必须仍是成员；`mentions:["all"]` 展开为其他成员。
- 消息身份、角色、时间由服务端填写。`requested_by=agent` 不代表本人授权。
- 消息列表在同一秒内保持落库顺序；不要把随机消息 ID 当作严格时间顺序。

建群示例：

```json
{
  "item": "welcome",
  "topic": "给出下一步建议",
  "members": ["my-agent"],
  "body": "请先读事项上下文，再给我一个可执行动作。",
  "mentions": ["my-agent"]
}
```

## 任务

| 方法与路径 | 含义 |
|---|---|
| GET `/v1/wakeups?wait=30&limit=50` | 当前身份的 pending 任务；wait 最大 60 秒 |
| POST `/v1/wakeups/{id}/claim` | body 可空或 `{"consumer":"my-agent"}`；返回 claim_token |
| POST `/v1/wakeups/{id}/finish` | `claim_token, result`；replied 时带 body，failed 时带 error_summary |

`finish(replied)` 将任务完成和群回复作为同一事务提交。必须是任务接收人、正确 token、processing 状态且群仍可写；错误、重复或撤权后的请求不能留下额外消息。

## 分支审批（可选私有 Git 远端）

| 方法与路径 | 含义 |
|---|---|
| GET `/v1/branches` | 可读的待审分支及涉及文件 |
| GET `/v1/branches/agent/{identity}/{topic}/diff` | 所有涉及路径可读时返回 patch |
| POST `/v1/branches/agent/{identity}/{topic}/apply` | rebase 后快进合入，要求 owner 或 approve 权限 |
| POST `/v1/branches/agent/{identity}/{topic}/reject` | 拒绝并删除远端提案分支，要求审批权限 |

完整 patch 检查新增、删除和重命名两侧涉及的路径，不通过时不返回部分私密内容。直接 Git 读写权限由部署者另行控制，HTTP scopes 不约束 Git 客户端。

## 行动建议

| 方法与路径 | 含义 |
|---|---|
| POST `/v1/actions` | 创建建议；同身份同 client_id 的相同提案重试返回已有状态 |
| GET `/v1/actions?item=welcome&status=suggested` | 列出可读行动 |
| GET `/v1/actions/{id}` | 行动及历史 |

提案字段：`client_id, item, title, details, plan_kind, plan_date`；来源二选一：`source_path` 或 `source_message_id`。`plan_kind` 为 day/week/later；day 用日期，week 用周一日期，later 不填日期。

来源权限参与创建、读取和重试校验；群消息来源同时检查群成员资格。Agent 只能提建议，采纳/改期/完成由 owner 网页执行，不触发外部任务。

## 运行身份（可选）

`POST /v1/runs` 登记 `client_run_id, harness` 以及可选的 `workspace, device_id`；`GET /v1/runs/{id}` 只读取当前 key 的运行。之后可带 `X-ASpace-Run-ID` 引用已登记运行。运行字段是客户端声明，不授予权限，也不能证明 Agent 实时在线。

## 常见状态

| 状态 | 含义 |
|---|---|
| 200 / 201 | 读取或操作成功 / 创建成功 |
| 400 | 请求格式、字段或提及对象不合法 |
| 401 | 缺失或无效 key |
| 403 | 已认证但操作不允许，例如关闭群写入 |
| 404 | 不存在或不可见 |
| 409 | 冲突、重复任务完成、陈旧权限/状态或版本不一致 |
| 500 / 503 | 内部或依赖失败，不应当作已成功 |

客户端应检查状态与实际数据，不要因为请求已发出就认定成功。服务端有请求体与超时限制，模型生成应在客户端完成，而不是持有写请求等待模型。
