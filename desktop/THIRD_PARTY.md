# Third-party components

GoAnime upstream source is by alvarorichard and contributors, under the repository's MIT license. SERIAL is a local GUI extension of upstream commit 85258f7.

The desktop framework is Wails v2.15.0 (MIT), with React 19.1.1 (MIT) and TypeScript 5.9.2 (Apache-2.0).

This local macOS build includes a separate mpv executable and its bundled libraries from the official mpv-player CI release:

- Version: v0.41.0-dev-ga1f50f2c3, built September 29, 2026.
- Asset: mpv-v0.41.0-dev-ga1f50f2c3-36640285359-macos-14-arm.zip.
- SHA-256: b4bb538d7b765b49b9104b4fe2eff5e84efdc55e9aeb7810cb75f04ea7a05211 (official release asset digest).
- Binary source: https://github.com/mpv-player/mpv/releases/tag/git-release
- mpv source at the build revision: https://github.com/mpv-player/mpv/tree/a1f50f2c3
- mpv license information and dependency notices: https://github.com/mpv-player/mpv/blob/a1f50f2c3/Copyright

mpv and its bundled libraries retain their upstream licenses. They run in a separate process; the GUI communicates using mpv's JSON IPC protocol. This package is a local development build, not an upstream GoAnime release.

The bundled GoAnimeMetalFX helper, displayed as SERIAL MetalFX Player, is built from `desktop/native/main.swift` under this repository's MIT license. It uses Apple system frameworks (AVFoundation, Metal, MetalKit, MetalFX, CoreVideo, Cocoa); no Apple framework is redistributed. Episode names are retrieved from Jikan/MyAnimeList, with a limited Wikipedia fallback for the exact Naruto series identities.

Discovery reads public MyAnimeList metadata and community recommendations through [Jikan](https://docs.api.jikan.moe/), with read-only [MyAnimeList](https://myanimelist.net/) pages as an outage backup. Anime titles, cover images, tags, and promotional trailers retain their respective owners' rights. Trailer previews use the official [YouTube iframe player](https://developers.google.com/youtube/iframe_api_reference) in Apple's system WebKit; videos are streamed from YouTube, not bundled or downloaded. One visible preview can start automatically; multiple players require individual user actions. The official controls and branding remain visible, following YouTube's embedded player requirements.
