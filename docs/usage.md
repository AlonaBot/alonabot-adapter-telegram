# Using Telegram

[简体中文](usage.zh-CN.md)

Use Core v0.2.0 or later. After installation, add a Telegram account in Core. [adapter_config_schema.json](../adapter_config_schema.json) defines instance settings.

## Instance configuration

Set `token` to send and receive messages through long polling. `api_base_url` defaults to the public Telegram service.
When adding an account in WebUI, **Account settings** verifies the Bot Token and shows the bot's name and username before you save.

| Setting | Default | Purpose |
| --- | --- | --- |
| `token` | required | Bot token, stored as a secret |
| `api_base_url` | `https://api.telegram.org` | API root; the adapter appends `/bot<TOKEN>/<method>` |
| `file_base_url` | empty | Separate file root, with `/file/bot<TOKEN>/<file_path>` appended; uses `api_base_url` when empty |
| `proxy_mode` | `none` | `none` for direct access, `core` for the Core proxy environment, `custom` for an instance proxy |
| `proxy` | empty | HTTP, HTTPS, or SOCKS5 proxy URL for `custom` mode |
| `auto_sync_commands` | `true` | Let Core maintain per-chat command menus |
| `clear_commands_on_disable` | `true` | Clear managed menus when switching from automatic to manual mode |

API calls and file downloads share the instance proxy. Both API and file roots support path prefixes.

## Media and message operations

Images, videos, files, and voice accept `https_url`, `platform_file`, `inline_base64`, and `media_ref`. Referenced uploads read Core media chunks directly into multipart requests; cancellation and read errors remain in send receipts. Voice is sent separately. Image references with trusted MIME `image/gif`, and inline images starting with `GIF8`, are sent as animations. Both break continuous albums, use a `.gif` upload name and MIME `image/gif`. Other uploads retain their source MIME and filename; unknown MIME uses `application/octet-stream`. HTTPS image URLs pass directly to Telegram.

Inbound voice becomes a voice segment, audio remains a file, animation/video_note become video, and static stickers become images. Animated and video stickers remain in the raw platform payload. Group/channel titles and private-chat names populate `chat_title`.

Use `message.URLButton("Open", "https://example.com")` for URL buttons. Callback buttons use Core-issued tokens. Text editing and deletion are supported. Deletion follows input order in batches of at most 100 IDs; after a failure or unknown outcome, later receipts remain unattempted.

## Typed actions

The public `action` package exposes 14 version-1, chat-scope actions:

- `BanChatMember`, `UnbanChatMember`, `RestrictChatMember`
- `PinChatMessage`, `UnpinChatMessage`, `SetMessageReaction`
- `GetChat`, `GetChatMember`, `GetChatMemberCount`, `GetChatAdministrators`
- `SetChatTitle`, `LeaveChat`, `SendChatAction`, `SendMessageDraft`

For example, pin the current message in a handler:

```go
_, err := action.PinChatMessage(c, c.Event().Ref(), action.PinChatMessageParams{})
```

The caller implements `sdk/action.Caller`: handler `*sdk.Context`, `*sdk.AdapterClient`, or a background `sdk/action.NewClient(ctx)`. Chat and topic come from the target conversation. Message operations use message handles; numeric IDs in parameters are decimal strings. `action.Call` returns the complete Bot API `result` JSON and error; typed results decode IDs as strings. Successful member management invalidates the administrator cache.

`action.Typing(ctx, to, opts...)` sends typing immediately, refreshes every 4 seconds, and ends with ctx. `action.StreamDraft(ctx, to, draftID, updates, opts...)` updates a private-chat draft from `DraftUpdate` values. When updates closes, it sends the final content through `sdk.Send`. `draftID` is a nonzero decimal string. Temporary drafts use platform actions; the final message runs outbound hooks. `SendMessageDraftParams` also supports `can_stop` and `keep_on_stop`.

## Typed events

The public `event` package provides payloads and event-name constants for `ForumTopicCreated`, `ForumTopicEdited`, `ForumTopicClosed`, `ForumTopicReopened`, `MessagePinned`, `ChatTitleChanged`, and `ChatMigrated`. Full names start with `telegram.`, version is 1, payloads use Bot API field names, and numeric IDs are strings. Topic location comes from the event conversation.

Member service messages only advance the polling cursor; member events come from `chat_member`/`my_chat_member`. Group migration emits an event only in the new supergroup. Other official service fields are enumerated and filtered in `internal/telegram/service.go`. Ordinary messages such as locations and polls retain an empty Chain and their raw payload.

## Command menus

The adapter implements `adapter.CommandMenuController`. `SetCommandMenu`, `GetCommandMenu`, and `DeleteCommandMenu` perform platform requests; Core owns mode, ownership, and serialized writes.

Automatic mode displays only commands explicitly marked with `CommandBuilder.Menu()`. Core manages visibility, permissions, Profile, prefixes, language, ownership, and lifecycle. The first message in a new chat and joining a group trigger asynchronous publication. Forum topics share the chat menu. The global `default` menu stays empty.

For manual operation, set `auto_sync_commands=false`, subscribe to `sdk.CommandMenuReady`, then call `sdk/command.Set`, `Get`, `Delete`, or `Status` after readiness. `Sync` reprocesses synchronization or mode-switch errors. Each position consists of account, scope, and language:

- `chat`: provide `ChatID`.
- `chat_administrators`: provide `ChatID`.
- `chat_member`: provide `ChatID` and `UserID`.

Empty `LanguageCode` means the default language; otherwise use a two-letter lowercase ISO 639-1 code. Each menu accepts up to 100 entries. Command names contain 1–32 lowercase letters, digits, or underscores; descriptions contain 1–256 characters. Invalid or excessive input returns an error and preserves the last successful menu. Set replaces the complete menu at that position; Delete removes only that position. Successful replacement transfers ownership of the whole menu to the plugin.

When switching from automatic to manual, Core drains in-flight writes, clears according to `clear_commands_on_disable`, then announces readiness. Cleanup failures retain their errors and can be reprocessed with explicit Sync. `clear=false` preserves menus until a plugin takes over. First manual startup and manual restarts preserve menus. Leaving a chat deactivates its target; rejoining recreates it. Historical scopes written by external tools need explicit positions for cleanup.

Scoped menus require Core v0.2.0. The SDK and packager stay pinned to the reviewed Core commit in `go.mod`. See [development](development.md) for public package and protocol entry points.
