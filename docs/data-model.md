# 数据模型

代码、长期资料、协作状态分别存放；公开仓库只包含第一类及空白模板。

```text
agents-home-public/           公开源码、文档、测试、模板
└─ var/                      私有运行目录，默认被忽略
   ├─ memory/                独立本地 Git 资料仓
   ├─ keys.json              凭据哈希及权限配置
   └─ aspace.db               群消息、任务、行动与审计
```

## 资料目录

| 路径 | 内容 |
|---|---|
| `profile/` | 协作偏好和背景 |
| `goals/` | 目标；`kind` 可为 outcome/explore/maintain/gated |
| `lines/` | 可选推进线，通过 `serves` 关联目标 |
| `items/<id>/CONTEXT.md` | 事项现状与下一步 |
| `people/` | 可选人物资料 |
| `knowledge/` | 持久知识 |
| `agents/registry.yaml` | 角色与凭据身份的显示映射 |
| `agents/<role-or-identity>/card.md` | 名称、文字头像、简介 |

事项 Markdown 示例：

```yaml
---
type: item
status: active
serves: []
people: []
reads: []
---
# 我的事项

## 目标
写清楚要完成什么。

## 下一步
写一个可以执行的动作。
```

`reads` 是相对于当前文件目录的阅读路径。目标/线/事项关系由 frontmatter 表达，不要求目录嵌套。`GET /v1/brief/<id>` 生成接手该事项的阅读清单。

## 身份与角色

- **凭据身份**由 key 决定，负责授权、群成员、消息发送者、任务接收者。
- **角色**用于显示与归组，不自动合并 key、权限、消息已读或运行状态。
- 新增角色不等于新增有效 key。先登记模板，再单独创建 key。
- registry/card 从已提交的 Git 版本读取；修改后要提交到自己的资料仓。

模板中 `my-agent` 映射到 `assistant` 角色。改成自己的命名时，同步调整 registry/card，再发放新 key。可使用纯文字头像；不要求接入任何特定厂商。

## 群与行动

群固定属于一个事项。当前成员可以读历史消息，owner 可见所有群；移出后撤销群访问并取消尚未完成的任务。创建者也必须仍是成员才能继续管理，owner 保留管理权限。

消息和任务不作为 Markdown 文件逐条写进 Git。Agent 可以提出行动建议，本人在网页中采纳、安排、完成；采纳只是更新计划状态，不执行外部操作。

## 备份

Git 不能代替数据库备份。恢复完整空间需要资料仓、SQLite 和权限配置三部分，参见 [部署文档](deployment.md)。
