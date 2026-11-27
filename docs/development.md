# Development

[简体中文](development.zh-CN.md)

Run these commands from this repository root. Use the Go version declared in `go.mod` (managed with asdf locally); the SDK stays pinned there. Build the CLI with the same component toolchain:

```fish
go mod download github.com/AlonaBot/AlonaBot
env GOTOOLCHAIN=(go env GOVERSION) go -C (go list -m -f '{{.Dir}}' github.com/AlonaBot/AlonaBot) install ./cmd/alonabot
```

Keep the Go bin directory on PATH. For cross-repository development, a local `go.work` may select the corresponding Core checkout; verify releases using the locked SDK.

The existing GORM dependency uses upstream releases pinned in `go.mod`.

## Verify a change

Select checks by the affected behavior. CI runs the following Go checks for Go, dependency, and CI gate changes; component resources and checking workflows also trigger `alonabot check .`.

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

This repository has no `go:generate` entry; edit the owned resources directly.

For documentation and GitHub metadata, check the changed links, YAML and scripts, plus `git diff --check`. In CI, Repository compares the event base SHA with HEAD; docs-only changes skip Go checks. Record actual commands, platform, failures, and unrun checks in the PR.

## Outbound descriptions and read-only checks

`internal/telegram/formats.go` augments `sdk/message.StandardFormats()` with status, sources, limits, and combination rules. `Descriptor.Formats` and `Descriptor.Combinations` reach Core through registration. This repository currently declares no outbound message extensions; platform actions and extension events retain their own entry points.

Instances implement the optional `adapter.MessageChecker`. `planTopology` in `plan.go` owns target, quote, keyboard placement, and consecutive media grouping, shared by `CheckMessage` and `Send`. Text, caption, source, and label validation is also shared. Checking reads the original Core-authorized `MediaRef`/`Incoming` with trusted MIME, accepts unsigned choice buttons, and avoids upload and final button rendering. Core converts generic Markdown to text entities before calling the adapter.

For local cross-repository builds, keep a dedicated `go.work` under ignored `tmp/` and explicitly set an absolute `GOWORK` for every command, selecting this repository and the corresponding Core checkout. Workspace files and local paths stay local; remote validation uses the exact SDK pinned in `go.mod`. The development candidate is `0.4.0-dev.0`, with bilingual notes under `changelog/0.4.0-dev.0/` headed Unreleased. Existing release history is preserved.

## Source map

- `telegram.go`: public construction entry
- `internal/telegram/`: component implementation
- `plugin.yaml`: component identity and packaging
- `action/`, `event/`, `perm/`, `telegramtest/`: typed platform API, permissions, and local Bot API simulator

[Core development](https://github.com/AlonaBot/AlonaBot/blob/main/docs/development.md) · [Component developer docs](https://alonabot.github.io/en/docs/tutorial/project)
