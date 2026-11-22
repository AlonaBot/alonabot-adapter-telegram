# alonabot-adapter-telegram 0.2.0 - 2026-11-22

### Added

- Support voice, GIF animation, static stickers, video notes, streaming media-reference uploads and URL buttons.
- Add `file_base_url` and chat titles, fourteen typed actions, typing and private draft-streaming helpers, and ordered batched deletion.
- Add seven service-message events and payload schemas, service filtering and deduplicated membership notifications.
- Support automatic/manual chat-scoped command menus with language-specific set, get and delete operations, retaining platform errors and the last successful menu. Scoped menu control requires Core v0.2.0.

### Fixed

- Include `.gif` filenames and `image/gif` content types for GIF reference and inline uploads; retain the source MIME and original filename for other uploads.

### Distribution and upgrade

- Publish five `.tar.gz` platform archives, including Windows amd64; GitHub supplies source archives. Standalone `release.json`, signatures and public-key assets are retired.
- Use the SDK and packager from reviewed Core commit `2dbbded7184f` (`v0.1.2-0.20261122021903-2dbbded7184f`), which contains the v0.2.0 archive installation flow. The component uses the MIT License.
- Upgrade Core to v0.2.0 before installing or updating this release. Stop the component and back up its data before replacement; retain the same component ID, source and data directory. Historical sidecar cleanup follows verification of Core and WebUI.
