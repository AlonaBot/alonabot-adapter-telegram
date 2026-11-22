# Unreleased

## Added

- Voice, GIF animation, static sticker and video note mappings, streamed media-reference uploads, and URL buttons.
- A separate `file_base_url` and chat titles on messages, callbacks, membership updates and extension events.
- Fourteen typed platform actions, typing and private draft streaming helpers, and ordered batched deletion.
- Seven service-message events with payload schemas, plus service filtering and membership notification deduplication.
- Automatic/manual command menu configuration reporting and per-chat scope/language set, get and delete operations that retain platform errors and the last successful menu.

## Fixed

- GIF reference and inline animation uploads now include a `.gif` filename and `image/gif` content type; other uploads retain the source MIME and original filename.

---

# alonabot-adapter-telegram 0.1.0

## Added

- First standalone official component release for AlonaBot.
- Signed packages for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64.

## Changed

- Group implementation under `internal/`; retain the public construction entry.
- Use the upstream GORM dependency where applicable.
