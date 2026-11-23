# Telegram 使用与配置

[English](usage.md)

使用 Core v0.2.0 或更高版本。安装后在 Core 添加 Telegram 账号，实例配置由 [adapter_config_schema.json](../adapter_config_schema.json) 声明。

## 实例配置

配置 `token` 后即可使用 long polling 收发消息；`api_base_url` 缺省为 Telegram 公共服务地址。

| 配置 | 缺省值 | 用途 |
| --- | --- | --- |
| `token` | 必填 | Bot Token，按 secret 保存 |
| `api_base_url` | `https://api.telegram.org` | API 服务根地址，适配器追加 `/bot<TOKEN>/<method>` |
| `file_base_url` | 空 | 独立文件服务根地址，追加 `/file/bot<TOKEN>/<file_path>`；留空时沿用 `api_base_url` |
| `proxy_mode` | `none` | `none` 直连、`core` 使用 Core 代理环境、`custom` 使用实例代理 |
| `proxy` | 空 | `custom` 模式的 HTTP、HTTPS 或 SOCKS5 代理地址 |
| `auto_sync_commands` | `true` | Core 自动维护按聊天发布的命令菜单 |
| `clear_commands_on_disable` | `true` | 从自动模式切到手动模式时清理受管菜单 |

API 与文件下载共用实例代理；文件根地址与 API 根地址均支持路径前缀。

## 媒体、消息动作与事件

图片、视频、文件和语音支持 `https_url`、`platform_file`、`inline_base64` 与 `media_ref`。引用上传边读取 Core 的媒体块边写 multipart，取消与读取错误会保留到发送回执。语音单独发送；可信 MIME 为 `image/gif` 的图片引用和 `GIF8` 开头的内联图片使用动画发送，均切断连续相册，上传文件名带 `.gif`，类型为 `image/gif`。其他上传沿用来源 MIME 和文件原名，未知 MIME 使用 `application/octet-stream`。HTTPS 图片 URL 原样交给 Telegram。

入站 voice 映射为语音，audio 保持文件，animation/video_note 映射为视频，静态 sticker 映射为图片。动画和视频 sticker 保留在原始平台载荷中。群与频道标题、私聊姓名直接填入 `chat_title`。

使用 `message.URLButton("Open", "https://example.com")` 添加链接按钮；回调按钮继续使用 Core 签发的 token。支持文本编辑和撤回，撤回按输入顺序每批最多 100 个 ID；失败或结果未知后保留后续未尝试回执。

公开 `action` 包提供以下 14 个 v1、chat scope 的带类型入口：

- `BanChatMember`、`UnbanChatMember`、`RestrictChatMember`
- `PinChatMessage`、`UnpinChatMessage`、`SetMessageReaction`
- `GetChat`、`GetChatMember`、`GetChatMemberCount`、`GetChatAdministrators`
- `SetChatTitle`、`LeaveChat`、`SendChatAction`、`SendMessageDraft`

例如，在消息 Handler 中置顶当前消息：

```go
_, err := action.PinChatMessage(c, c.Event().Ref(), action.PinChatMessageParams{})
```

调用者为 `sdk/action.Caller`，支持 Handler 的 `*sdk.Context`、`*sdk.AdapterClient` 与后台 `sdk/action.NewClient(ctx)`。聊天和话题取自目标会话；消息操作使用消息句柄，参数中的数字 ID 使用十进制字符串。`action.Call` 返回完整 Bot API `result` JSON 与错误；带类型结果将 ID 读为字符串。成员管理成功会作废管理员缓存。

`action.Typing(ctx, to, opts...)` 立即发送输入状态并每 4 秒刷新，随 ctx 结束；`action.StreamDraft(ctx, to, draftID, updates, opts...)` 随 `DraftUpdate` 更新私聊草稿，关闭输入后经 `sdk.Send` 正式发送最后内容。`draftID` 为非零十进制字符串；临时草稿使用平台动作，正式消息执行出站 Hook。`SendMessageDraftParams` 同步支持 `can_stop` 与 `keep_on_stop`。

公开 `event` 包提供 7 个载荷与事件名常量：`ForumTopicCreated`、`ForumTopicEdited`、`ForumTopicClosed`、`ForumTopicReopened`、`MessagePinned`、`ChatTitleChanged`、`ChatMigrated`。事件完整名以 `telegram.` 开头，版本为 1，payload 沿用 Bot API 字段名，数字 ID 为字符串；话题位置取事件会话。成员服务消息只推进游标，成员事件统一来自 `chat_member`/`my_chat_member`；群升级只在新超级群生成迁移事件。其他官方服务字段在 `internal/telegram/service.go` 逐项列出并过滤，位置、投票等普通消息保留空 Chain 与原始载荷。

## 命令菜单

自动模式只展示用 `CommandBuilder.Menu()` 标记的命令。Core 负责可见性、权限、Profile、前缀、语言、所有权和生命周期；新聊天首次消息及入群后异步发布，论坛话题共用聊天菜单。全局 `default` 菜单保持为空。

手动配置 `auto_sync_commands=false`，订阅 `sdk.CommandMenuReady`，就绪后调用 `sdk/command.Set`、`Get`、`Delete`、`Status`；`Sync` 可重新处理同步或模式切换错误。位置由账号、scope 与语言组成：

- `chat`：填写 `ChatID`。
- `chat_administrators`：填写 `ChatID`。
- `chat_member`：填写 `ChatID` 与 `UserID`。

`LanguageCode` 为空表示缺省语言，非空使用两位小写 ISO 639-1。每份菜单最多 100 项，命令名使用 1～32 个小写字母、数字或下划线，说明为 1～256 个字符；超限和无效输入返回错误，保留上次成功菜单。Set 整份覆盖指定位置，Delete 仅删除指定位置；成功覆盖会转移整份菜单的插件所有权。

自动转手动时，Core 排空在途写入，按 `clear_commands_on_disable` 清理后通知 ready；清理失败保留错误，显式 Sync 可重处理。`clear=false` 保留菜单至插件接管，首次手动启动及手动重启保留菜单。机器人离群停用目标，重入后重建。通过外部工具写过的历史 scope 需要明确指定位置后清理。

分域命令菜单需要 Core v0.2.0；SDK 与打包器固定到 `go.mod` 中已审查的 Core 精确提交。


Adapter 实现 `adapter.CommandMenuController`；`SetCommandMenu`、`GetCommandMenu`、`DeleteCommandMenu` 执行平台请求，Core 管理模式、所有权和串行写入。协议与公开包入口见[开发指南](development.zh-CN.md)。
