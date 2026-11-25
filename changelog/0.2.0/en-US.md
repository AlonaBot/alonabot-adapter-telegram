# alonabot-adapter-telegram 0.2.0 - 2026-11-22

### Added

- Support voice, GIF animation, static stickers, video notes, streaming media-reference uploads and URL buttons.
- Add `file_base_url` and chat titles, fourteen typed actions, typing and private draft-streaming helpers, and ordered batched deletion.
- Add seven service-message events and payload schemas, service filtering and deduplicated membership notifications.
- Support automatic/manual chat-scoped command menus with language-specific set, get and delete operations, retaining platform errors and the last successful menu. Scoped menu control requires Core v0.2.0.

### Fixed

- Include `.gif` filenames and `image/gif` content types for GIF reference and inline uploads; retain the source MIME and original filename for other uploads.

This release adopts the single-archive format and the MIT License. Installing it requires the archive installer introduced in Core v0.2.0.
