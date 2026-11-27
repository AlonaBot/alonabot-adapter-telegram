# 0.4.0-dev.0 — Unreleased

## Added

- Declare standard rich message formats, media sources, UTF-16 limits, and album, quote, and keyboard rules, with a complete album → text → buttons example.
- Add read-only message checks sharing real send topology, accepting unsigned choice buttons and trusted MIME; missing MIME that determines topology returns unknown.

## Development

- This candidate requires Core/SDK with outbound descriptions and checks. Local cross-repository builds use a dedicated workspace; integration pins the exact remote SDK version.
