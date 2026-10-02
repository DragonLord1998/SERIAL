# SERIAL

A local desktop GUI built with Wails v2.15.0, React, TypeScript, and GoAnime's existing provider code. The first packaged build targets Apple Silicon macOS 14 or later.

## Run

Open `desktop/build/bin/SERIAL.app`. The macOS package includes mpv, so playback does not require a separate installation. Video opens in a separate player window. MetalFX playback controls are visible inside that window and in SERIAL. Off uses bundled mpv; MetalFX 2× uses the bundled native Apple Silicon player.

Version 0.5.3 adds visible episode playback controls and retains the simplified thumbnail cards and muted visible trailer autoplay. Click an image to pause/resume its trailer, or double-click to restart. The dark interface includes Home, Explore, Search, Library, Downloads, and Settings, with MyAnimeList tags and community recommendations, real episode names, and optional MetalFX 2× playback. Home keeps Continue Watching and a small recommendation preview. The app includes source/language search, quality and sub/dub preferences, saved anime, durable resume, download cancellation/retry, and local playback of completed downloads. Empty states contain no sample titles. A standalone browser preview clearly indicates that it is disconnected from the desktop backend.

SERIAL keeps the existing profile identity. Local preferences, likes/dislikes, saved anime, history, and download records are stored in `~/Library/Application Support/GoAnimeGUI/state.json`. Downloads default to `~/Downloads/GoAnime`. `GOANIME_GUI_DATA_DIR` can point to a separate state directory for tests. Existing profiles load with empty feedback and retain their library, history, downloads, and preferences.

## Explore and feedback

Explore combines real popular MyAnimeList entries with available HiAnime shows. Choose up to three genre, theme, or demographic tags to narrow the catalogue; matching all selected tags is required. More tags reveals the complete list. Like or Dislike a title from a card or its episode details; selecting the same rating again clears it. Library has Saved, Liked, and Disliked views for reviewing and undoing feedback. Saving is independent of rating.

Likes seed MyAnimeList community recommendations and boost shared tags and genres. Dislikes hide rated shows and lower similar candidates. Saved, watched, and rated titles are excluded. MAL IDs also prevent duplicate ratings for English/Japanese title aliases. Each card explains its recommendation. Ratings stay on this Mac; the app reads public metadata without connecting to a MyAnimeList account.

[Jikan](https://docs.api.jikan.moe/) supplies public MyAnimeList metadata, with read-only public MyAnimeList pages as a backup during outages. Metadata requests share the episode-title rate limiter, are bounded, coalesced, and cached privately beside the profile. Up to three recent likes and two dislikes fetch missing details; all stored tag and genre feedback contributes to ranking. Refresh requests updated discovery. Streaming discovery retains its existing session cache and provider warnings. Catalogue entries require one exact title/alias and episode-count match before opening streaming episodes; ambiguous or unavailable shows offer Search instead.

## Thumbnail trailers

A genuine trailer starts muted as soon as an eligible poster is fully visible. Automatic previews advance through visible titles when a trailer finishes, fails, is paused, or leaves the viewport. Only one preview plays automatically at a time, following [YouTube's autoplay requirement](https://developers.google.com/youtube/terms/required-minimum-functionality#autoplay-and-scripted-playbacks). Reduced motion and a background document suspend automatic previews.

Click the playing thumbnail to pause and display its cover; click the cover to resume the retained position. Double-click either state to restart. Pausing a thumbnail explicitly prevents it from automatically restarting. Explicit playback takes priority over the automatic preview, and several manually started players can still run independently. YouTube controls and branding stay visible; image gestures apply to the central video area. Titles and Episodes open episode details.

Cards keep playback status and timestamps out of their footers. They show Like, Dislike, and Episodes; a small play cue appears on cover hover or keyboard focus. Missing or restricted trailers keep the cover and a quiet fallback rather than an extra status panel. Automatic playback never opens an external browser.

Scrolling a playing poster out of view stops an automatic preview or pauses and hides a manually started player. Each automatic card gets one attempt per trailer until navigation, card removal, changed trailer metadata, or an explicit image click resets it; an ended or failed trailer does not repeatedly reload. Navigation, removing a card, opening details, starting an episode, and closing the app dispose their previews. Players use the official YouTube iframe in a native WKWebView with the app bundle identity as its web origin. No placeholder videos or opening/ending songs are substituted for trailers. Inline previews currently target macOS; other platforms retain explicit browser trailer links. Episode playback, subtitles, and MetalFX use the separate native episode player.

## Episode names and MetalFX

Episode lists, Continue Watching, downloads, and the player show real names when metadata is available. Existing provider names take priority. Generic labels are enriched through [Jikan](https://docs.api.jikan.moe/): HiAnime supplies a MyAnimeList series ID through its supported server mapping; other sources use a strict title and episode-count match. Ambiguous matches retain the episode number. Requests are bounded and rate limited; complete results cache for 24 hours during the app session, partial results for five minutes. Canceled requests do not poison the cache. Exact Naruto (220 episodes) and Naruto: Shippuden (500 episodes) identities have a bounded Wikipedia backup during Jikan outages. Metadata failures preserve playable episode URLs and numbers.

Choose **MetalFX 2×** in Settings or the episode dialog. The app checks the bundled player and the Mac's actual MetalFX support. The native player decodes video with AVFoundation and runs each decoded frame through Apple's [MetalFX spatial scaler](https://developer.apple.com/documentation/metalfx/mtlfxspatialscaler) on the GPU. Output is twice the source width and height, then fitted to the window; source quality still determines the available detail. This is spatial scaling of SDR video. Playback reports source and output dimensions only after a completed GPU frame.

Online English WebVTT subtitles and embedded HLS captions appear over the scaled video. The native episode window has Play/Pause, ten-second rewind/forward, a seek bar with elapsed/duration, 0.5×–2× playback speed, mute/volume, and fullscreen. Clicking the video toggles pause. Space toggles pause and Left/Right seek ten seconds when a slider or menu is not focused. Speed changes retain pause and apply when resumed. The main SERIAL footer also offers transport, speed, and volume; Stop closes the player and saves the position. Changing the upscaler applies to the next playback session. Existing profiles keep Off until MetalFX is selected. MetalFX is available only in the Apple Silicon macOS package; Off retains mpv playback.

## Providers and current limits

- HiAnime: English catalog with subtitled/dubbed ZokoAnime streams, English subtitles for online playback, and available HLS quality selection. This GUI adapter uses the current theme API; it does not launch a terminal or browser.
- AniDB: English catalog; sub/dub selection follows upstream language support. The upstream resolver can fall back when a requested resolution is unavailable.
- AnimeFire: Portuguese catalog; dubbed titles are separate Dublado results. JSON quality lists are resolved without terminal prompts.
- Goyabu, SuperFlix, and Blogger embeds are excluded from this first GUI because their current paths require terminal choices or additional extraction work.

The 0.1.1 repair adds HiAnime because the original two sources are unavailable on this connection. On October 2, 2026, AniDB served an explicit maintenance page (HTTP 503), and AnimeFire blocked requests (HTTP 403). Search keeps results from responding sources and reports failed or timed-out sources separately. A source outage no longer discards a successful search.

Live checks returned Naruto, Bleach, and One Piece results and Naruto's 220 episodes. The bundled mpv decoded real video and audio in both Naruto subtitled and dubbed modes; seek, stop, and resume checkpoints passed. Availability still depends on each title's supported server. External subtitle files are used for online playback; the current downloader exports video/audio without saving those external subtitle files.

Direct-file downloads and supported MPEG-TS HLS streams use the existing Go network/download code with stricter completion checks. Native HLS rejects encryption, fragmented media, live playlists, or separate audio. If a separate `ffmpeg` executable is installed, the downloader uses it for HLS export. The mpv bundle includes FFmpeg libraries, not a standalone ffmpeg executable. Failed or canceled partial files are cleaned up; files are marked completed only after successful finalization. The real ffmpeg path remains unverified.

Windows and Linux source paths are present, but builds on those platforms are unverified. Integrated in-window video, Anime4K controls, and Discord integration are not included.

## Develop and build

Prerequisites: Go 1.27.1 or newer, Node.js/npm, and the platform's Wails native build prerequisites. No Go or Node installation is needed to run the packaged macOS app.

From the repository root:

```sh
go test -race ./internal/desktop
go vet ./internal/desktop ./desktop
cd desktop/frontend
npm ci
npm test
npm run build
```

From `desktop`, use `wails dev` for native development. The frontend is also viewable with `npm run dev`, with an explicit desktop-connection notice.

`desktop/scripts/build-macos.sh` builds the Apple Silicon macOS app, retains or downloads the pinned official mpv bundle, compiles the Swift MetalFX helper using Apple system frameworks, copies its source/third-party notice, and signs the package for local use. This is a local development build; it has not been notarized or published.

## Architecture

- `desktop/main.go` and `app.go`: native Wails window, dialogs, typed Go bindings, state events, lifecycle.
- `desktop/frontend/src`: React interface; no subprocess or provider logic in the frontend.
- `internal/desktop/catalog.go`: provider adapter calls, concurrent source results/warnings, source validation, stream headers, audio choice, metadata mapping.
- `internal/desktop/hianime.go`: bounded HiAnime theme API requests, episode membership checks, supported embed decoding, HLS quality selection, and English subtitle metadata.
- `internal/desktop/hianime_browse.go` and `recommendations.go`: real discovery/detail parsing, recommendation scoring and explanations, bounded requests, deduplication, and an in-memory provider cache.
- `internal/desktop/episode_titles.go`: bounded, cached, identity-checked episode enrichment.
- `internal/desktop/mal_metadata.go` and `discovery_service.go`: public tags/community recommendations, durable metadata cache, feedback ranking, and verified catalogue-to-source matching.
- `desktop/trailer_darwin.*`: independent muted native YouTube players, pause/resume/restart, measured positions, bundle web identity, geometry checks, click forwarding, and lifecycle cleanup.
- `internal/desktop/player.go` and `metalfx.go`: owned player processes, private configuration and JSON IPC with deadlines, playback monitoring.
- `desktop/native/main.swift`: AVFoundation decoding, real MetalFX spatial scaling, subtitle overlay, and the native player window.
- `internal/desktop/downloads.go`: one-job FIFO queue, cancellation, honest progress, strict download completion.
- `internal/desktop/store.go` and `service.go`: private atomic state files, settings/library/history/feedback and asynchronous state coordination. A persisted feedback revision prevents old unrelated responses from replacing confirmed ratings in the interface.

The terminal entry point and existing provider implementation remain available. This work adds a desktop entry point and services; it does not automate the TUI.

## Verification

Version 0.5.3 adds visible episode controls in the native MetalFX window and main app. The 28 frontend tests, TypeScript production build, fresh Go desktop tests with the race detector, Go static analysis, native compiler, packaged Apple Silicon build, and strict signatures passed. A focused review found no remaining issues after rapid-seek and command-state fixes. Actual AVPlayer checks confirmed paused speed changes do not resume playback, selected speed survives resume, and the native mute handler restores 40% and 100% volume. The MetalFX GPU self-test completed on Apple M4 with a 64×36 → 128×72 frame and nonzero checksum.

Live UI checks used an isolated profile and a real Fullmetal Alchemist: Brotherhood stream with English subtitles at 1920×1080 → 3840×2160. Native Play/Pause, image-click pause, Space resume, ten-second rewind/forward, seeking, paused 1.5× selection, mute/restore at 40%, fullscreen, and a 900×400 player layout passed. Main-app controls changed speed while paused, adjusted volume to 35%, restored it after mute, accumulated two rapid forward clicks into twenty seconds, resumed at 1.5×, and stopped the owned player. Native controls mirrored those changes. All main controls remained visible at 980px width. Existing user profile storage and bundle identifiers are retained.

Version 0.5.2 renames the desktop app to SERIAL. The 27 frontend tests, TypeScript production build, Go static analysis, native package build, and strict signature verification passed. Native UI checks verified SERIAL in the window, sidebar, macOS menu, and Settings About card; bundled mpv and MetalFX remained available. Existing bundle identifiers and profile storage were retained, and the user's library, preferences, and watch history were preserved.

Version 0.5.1 passed 27 frontend tests, the TypeScript production build, Go static analysis, the Apple Silicon package build, strict signature verification, and a focused lifecycle review. Tests cover serialized automatic starts, cancellation of stale asynchronous work, manual playback priority, hydrated trailer metadata surviving thinner card data, and suppression of finished or failed cards after native player disposal. Native UI checks verified genuine muted autoplay without hovering or clicking a poster, image-click pause restoring the cover, the next visible title starting automatically, explicit resume taking priority, and double-click restart. A sole eligible trailer completed and stayed on its cover through subsequent scrolling. Explore scrolling removed an offscreen automatic player, navigation cleared previews, and card footers contained only feedback and Episodes. Gesture tests used a separate profile. In the installed app, scrolling partially clipped Home cards fully into view started the genuine Frieren trailer automatically. The original user profile remained unchanged.

Version 0.5.0 passed 23 frontend tests, the TypeScript production build, fresh desktop service tests with the race detector, Go static analysis, native compiler/input/lifecycle checks, the Apple Silicon package build, and strict signature verification. Native UI checks played genuine Jujutsu Kaisen and Chainsaw Man trailers simultaneously; pausing one restored its cover while the other continued. A resume checkpoint advanced from 0:06 to 0:18; double-click restart was verified from both a cover and a playing native view. Completed trailers restored their covers. Removing a playing card through Library filters and navigating away removed its native player. Testing used a separate profile; the user's history and preferences remained unchanged.

Version 0.4.1 passed 23 frontend tests, the TypeScript production build, native macOS packaging, and strict signature verification. Native UI checks verified that a muted hover preview keeps playing while the cursor is elsewhere in the window, with Stop visible and restoring the poster. An independent lifecycle review confirmed the existing navigation, detail, episode, and viewport stop paths remain in place.

Version 0.4.0 passed the desktop service suite with the race detector, Go static analysis, 23 frontend helper tests, the TypeScript production build, the native Apple Silicon package build, and strict signature verification. Fixtures cover public metadata mapping, tag filters, community ranking/exclusions, exact source matching, title aliases, stale-response cancellation, and JSON/HTML cache recovery across restarts.

`GOANIME_LIVE_MAL=1 go test ./internal/desktop -run TestMALMetadataLivePublicPages -v` verified public tags, 48 Action-filtered titles, 48 Naruto recommendations, and genuine Chainsaw Man trailer metadata. Native UI checks verified automatic muted hover and explicit inline trailer playback, Stop restoring the poster, rating-driven MyAnimeList recommendation reasons, tag disclosure/filtering, and catalogue resolution to Fullmetal Alchemist: Brotherhood's 64 named episodes. Playback from that catalogue title produced a persisted resume checkpoint. A separate test profile kept test ratings and history out of the user's profile. The bundled MetalFX GPU self-test passed on Apple M4 with a completed 64×36 → 128×72 frame and a nonzero checksum.

The 0.3.0 checks include the desktop service suite with the race detector, fourteen frontend helper tests, TypeScript production build, Go static analysis, native macOS package build, and strict signature verification. Metadata fixtures cover mapping, ambiguous matches, pagination, cancellation, cache recovery, and provider failures. `GOANIME_LIVE_JIKAN=1 go test ./internal/desktop -run TestEpisodeTitleEnricherLiveJikanNarutoAndShippuden -v` checks real first/last episode names for both series. The native `--self-test` submits an actual MetalFX GPU command and validates a nonzero output checksum on Apple M4. Native playback on the M4 rendered Naruto with English subtitles at 1056×800 → 2112×1600. Pause, seek, volume, Stop, and resumed playback passed using an isolated profile; Stop persisted the new position and episode name. Live metadata checks verified Naruto episodes 1, 2, and 220 and Shippuden episodes 1, 2, and 500, including backup recovery for missing late-page metadata.


The 0.2.0 checks passed: TypeScript production build, ten frontend helper tests, the desktop service suite with the race detector, Go static analysis, native macOS package build, and strict package signature verification. Feedback tests cover restart, exclusive ratings, undo, legacy profiles, failed-save rollback, and service-level ranking/exclusions. Recommendation tests cover chronological seed selection, cache expiry/outage fallback, cancellation, request coalescing, and concurrency bounds. Frontend tests guard against stale recommendation and full-state responses.

The bundled mpv was exercised with a generated local H.264 video served by a local HTTP test server. The test verified playback, source-specific User-Agent/Referer/Origin headers, seek, stop, state persistence, and resumed playback after creating a new service. Download tests use local fixtures and cover exact completion, cancellation, queued cancellation, truncated/error responses, incomplete HLS rejection, extensionless/root-relative HLS, and unsupported HLS formats.

The opt-in `TestDesktopLiveCatalogAndPlayback` verifies live catalog and decoded sub/dub playback. `GOANIME_TEST_EXPLORE_LIVE=1 go test ./internal/desktop -run TestHiAnimeExploreLive -v` verifies real discovery and personalization. The live Explore check returned 48 popular/airing titles and a changed personalized feed, with no source warnings. Ordinary tests use fixtures and remain independent of provider uptime.

Native 0.2.0 UI checks verified the minimal layout, real Explore feed, immediate Like/Dislike exclusions, personalized reasons, Library feedback views, restart persistence, and undo restoring the cold feed. An isolated temporary profile kept test ratings separate from normal user state. Prior native checks verified search/settings, source warnings, and saved preferences.

See `THIRD_PARTY.md` for the pinned mpv source and licenses.
