# alonabot-adapter-telegram 0.3.0 - 2026-11-23

### Added

- When adding or editing an account, paste a Bot Token in WebUI's account settings page and verify it with `getMe`; the bot's name and username are shown before the token is filled into the configuration. The `none`, `core` and `custom` proxy modes apply. See [PR #10](https://github.com/AlonaBot/alonabot-adapter-telegram/pull/10).
- Support Core v0.3.0's Adapter action catalog and `alonabot adapter instance` debugging commands.

### Distribution and upgrade

- The SDK and packager are pinned to Core commit `25a0f851099f` (`v0.2.1-0.20261123105642-25a0f851099f`).
- Upgrade Core to v0.3.0 before installing or updating this release; the account settings page requires WebUI 0.3.0.
