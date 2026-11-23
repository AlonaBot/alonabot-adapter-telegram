# alonabot-adapter-telegram 0.3.0 - 2026-11-23

### 新增

- 添加或编辑账号时，可在 WebUI「账号设置」中粘贴 Bot Token 并用 `getMe` 验证，确认机器人名称和用户名后再回填到配置；代理按 `none`、`core`、`custom` 设置生效。见 [PR #10](https://github.com/AlonaBot/alonabot-adapter-telegram/pull/10)。
- 支持 Core v0.3.0 的 Adapter 操作目录与 `alonabot adapter instance` 调试命令。

### 发行与升级

- SDK 与打包器固定到 Core 提交 `25a0f851099f`（`v0.2.1-0.20261123105642-25a0f851099f`）。
- 安装或更新本版前先升级 Core v0.3.0；「账号设置」需要 WebUI 0.3.0。
