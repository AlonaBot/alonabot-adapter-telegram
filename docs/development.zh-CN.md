# 源码开发

[English](development.md)

在本仓根目录执行。使用 `go.mod` 声明的 Go 版本，本地由 asdf 管理；SDK 以该文件固定版本为准。CLI 沿用组件工具链构建：

```fish
go mod download github.com/AlonaBot/AlonaBot
env GOTOOLCHAIN=(go env GOVERSION) go -C (go list -m -f '{{.Dir}}' github.com/AlonaBot/AlonaBot) install ./cmd/alonabot
```

将 Go bin 目录加入 PATH。跨仓开发可用本地 `go.work` 指向对应 Core checkout；发行验证使用锁定的 SDK。

现有 GORM 依赖使用 `go.mod` 固定的上游正式版本。

## 验证改动

按影响选择检查。Go、依赖和 CI 门禁变更触发以下 Go 检查；组件资源和检查工作流变更也触发 `alonabot check .`。

```fish
gofmt -l (git ls-files '*.go')
go vet ./...
go test -race ./...
for target in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64
    set -l parts (string split / $target)
    env GOOS=$parts[1] GOARCH=$parts[2] CGO_ENABLED=0 go build ./...
end
alonabot check .
```

本仓当前没有 `go:generate` 入口，直接维护对应资源。

文档和 GitHub 元数据检查改动链接、YAML、脚本及 `git diff --check`。CI 的 Repository 比较事件 base SHA 与 HEAD；纯文档改动跳过 Go 检查。PR 记录实际命令、平台、失败与未运行项。

## 出站描述与只读预检

`internal/telegram/formats.go` 从 `sdk/message.StandardFormats()` 补充状态、来源、限制与组合规则，`Descriptor.Formats` 和 `Descriptor.Combinations` 随注册提供给 Core。本仓当前没有出站消息扩展声明；平台动作和扩展事件维持各自入口。

实例实现可选的 `adapter.MessageChecker`。`plan.go` 的 `planTopology` 负责目标、引用、键盘位置与连续媒体分组，`CheckMessage` 与 `Send` 共用；文本、caption、来源和标签校验同样复用。预检读取 Core 已授权的原始 `MediaRef`/`Incoming` 及可信 MIME，接受未签发的选项按钮，不进入上传或最终按钮渲染。Core 在调用前将通用 Markdown 转为文本实体。

本地跨仓联编把专用 `go.work` 放在已忽略的 `tmp/`，每条命令显式设置绝对 `GOWORK`，指向本仓及对应 Core checkout。工作区文件和本地路径留在本机，远端验证使用 `go.mod` 固定的精确 SDK。开发候选为 `0.4.0-dev.0`，双语日志位于 `changelog/0.4.0-dev.0/`，标题标注 Unreleased；正式版本历史保留。

## 源码导航

- `telegram.go`: 公开构造入口
- `internal/telegram/`: 组件实现
- `plugin.yaml`: 组件身份与打包声明
- `action/`、`event/`、`perm/`、`telegramtest/`：类型化平台 API、权限与本地 Bot API 模拟端

[Core 源码开发](https://github.com/AlonaBot/AlonaBot/blob/main/docs/development.zh-CN.md) · [组件开发文档](https://alonabot.github.io/docs/tutorial/project)
