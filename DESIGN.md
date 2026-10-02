# Design

## Source of truth
- Status: Active
- Last refreshed: 2026-10-02
- Approved update: MyAnimeList genres/themes, community recommendations ranked with local feedback, and minimal thumbnail cards with image-only playback controls, one muted visible YouTube trailer autoplaying at a time, and independent explicit pause/resume/restart gestures.
- Primary product surfaces: SERIAL native desktop application, Apple Silicon macOS.
- Evidence reviewed: desktop/frontend/src/App.tsx, styles.css, icons.tsx, types.ts; internal/desktop state, catalog, player, downloads; desktop/README.md; current HiAnime browse and detail responses.

## Brand
- Product name: SERIAL, uppercase in the sidebar, window, About panel, and macOS app bundle. Retain upstream GoAnime attribution and existing profile identity.
- Personality: quiet, useful, personal.
- Trust signals: real provider titles, visible loading/errors, local preferences, clear recommendation reasons.
- Avoid: decorative heroes, marketing slogans, oversized headings, repeated badges, dense ornaments.

## Product goals
- Goals: find an anime, choose an episode by its real name, watch/resume with optional MetalFX, save it, and discover new titles using explicit likes/dislikes.
- Non-goals: accounts, cloud profiles, social features, AI model infrastructure.
- Success signals: Explore has real titles before feedback; liking changes recommendation order/pool; dislikes disappear; feedback survives restart and can be undone.

## Personas and jobs
- Primary personas: a viewer using their own Mac.
- User jobs: continue a series; search a known title; discover something similar to favorite shows; hide unwanted recommendations.
- Key contexts of use: desktop keyboard/mouse; variable provider availability.

## Information architecture
- Primary navigation: Home, Explore, Search, Library, Downloads; Settings at the bottom.
- Discovery update: Explore gets compact selectable genre/theme chips; details show full tags and a trailer action. MAL catalog entries resolve to a verified streaming title before episode playback.
- Core routes/screens: Home shows resume and small Explore preview; Explore is the full personalized feed; Search preserves provider/language controls; Library has Saved/Liked/Disliked views for feedback management.
- Content hierarchy: page title and one sentence, real cards/actions, short contextual notices.

## Design principles
- Content first: artwork and titles carry the screen, not decorative illustration.
- Explicit control: Like and Dislike are mutually exclusive toggles, independent of saving; selected state and undo are visible.
- Tradeoffs: keep existing dark theme/coral accent; make structure simpler rather than changing the familiar watching workflow.

## Visual language
- Color: neutral dark backgrounds, muted grey text, coral for active/primary actions; restrained green/red feedback states with labels/icons.
- Typography: system font; 26-28px page titles, 16-18px section headings, readable 12-14px body.
- Spacing/layout rhythm: 8px base; compact 176-192px sidebar; main content 28-32px gutters.
- Shape/radius/elevation: thin borders, 6-8px corners, no large gradients or ambient shadows.
- Motion: short state transitions; obey reduced motion. One fully visible trailer starts automatically without hover. Advance to another eligible visible card after it ends, fails, is paused by the viewer, or leaves the viewport. Never automatically resume a viewer-paused card. Explicit playback takes priority and suspends automatic selection while manual players run. Several manually started trailers remain independent. Reduced motion disables automatic previews.
- Imagery/iconography: real cover art and existing SVG icons; no hero illustration.

## Components
- Existing components to reuse: Poster, Button, AnimeCard, episode dialog, player, history/download rows.
- Playback controls: persistent controls inside the native episode window, below the video and subtitles: Play/Pause, ten-second rewind/forward, seek with elapsed/duration, 0.5×–2× speed, mute/volume, and fullscreen. Clicking the video toggles pause. Space and arrows control playback when no slider or menu has focus. Changing speed while paused retains pause and the selected resume speed. The main app footer mirrors transport, speed, and volume at its minimum width. Keep the Off/MetalFX 2× selector, real episode names, and confirmed source/output dimensions.
- Trailer component: native WKWebView preview occupies the thumbnail area at a minimum 200×200 viewport, with unobscured YouTube controls/branding. A single thumbnail click starts a trailer, or pauses an active trailer and shows its cover; another single click resumes the retained position. Double-click seeks to the beginning and plays. Each physical card owns its own player, position, token, and status. Defer single-click handling briefly so a double click produces only the restart action. YouTube controls and branding remain unobscured; the central image area forwards click gestures. Scrolling out pauses and hides that player. Automatic selection can choose eligible visible cards, but cards explicitly paused by the viewer retain their covers until clicked. One automatic preview at most; dispose or hide the prior automatic player before starting a replacement. Changing page, opening details, starting an episode, or unmounting disposes all players. Titles and a labeled Episodes button open details independently. Missing or restricted videos keep the poster; reveal a quiet error/fallback within the cover, without adding a card status panel. Card footers contain Like, Dislike, and Episodes only; image clicks own playback. Remove Stop buttons, playback timestamps, duplicate Trailer buttons, and persistent Play trailer/Continue text on covers. Show a small play/resume cue only on hover or keyboard focus. Keep title, reason, compact tags, and actions aligned with no overflowing buttons.
- New/changed components: Like/Dislike toggles on cards and details, Explore cards with short reasons, compact feedback filter tabs, quiet page navigation.
- Variants and states: neutral/liked/disliked; idle/loading/updating/empty/error; saved remains independent.
- Token/component ownership: repo-native CSS variables and existing React components; no new UI dependency.

## Accessibility
- Target standard: WCAG 2.2 AA where applicable.
- Keyboard/focus behavior: labeled buttons, aria-pressed feedback toggles, visible focus, existing dialog trap; Cmd/Ctrl-K search.
- Contrast/readability: maintain readable neutral text; feedback selection never relies on color alone.
- Screen-reader semantics: descriptive card actions, polite feed status, errors announced.
- Reduced motion and sensory considerations: muted autoplay, no simultaneous automatic previews, and automatic previews disabled for reduced motion. Explicit click controls remain available.

## Responsive behavior
- Supported breakpoints/devices: native minimum 980x660; wider desktops; browser preview smaller layouts.
- Layout adaptations: auto-fill card grid, compact navigation at narrow widths, dialog fits viewport.
- Touch/hover differences: feedback controls remain visible; no hover-only essential actions.

## Interaction states
- Loading: skeleton/spinner plus concise label; no fabricated sample titles.
- Empty: invite search/likes; no misleading personal ranking before feedback.
- Error: retain existing usable feed when refresh fails; retry visible.
- Success: immediate selected feedback state, dislikes filtered locally, async feed refresh.
- Disabled: individual pending action disabled; playback controls retain existing safety.
- Offline/slow network: cached real candidates can be ranked using new feedback; explain any provider failure.

## Content voice
- Tone: concise and direct.
- Terminology: Explore, Like, Dislike, Saved, For you, Popular.
- Microcopy rules: show honest reasons such as Because you liked Naruto; no claim of AI or guaranteed availability.

## Implementation constraints
- Framework/styling system: Wails v2, React/TypeScript, Go services, plain CSS; separate Swift/AVFoundation/MetalFX player built with Apple system frameworks.
- Design-token constraints: reuse existing dark/coral palette, reduce surface variety.
- Metadata constraints: Jikan public read-only data with shared rate limiter, bounded requests, cache/coalescing, honest outage notices; details only hydrate visible cards through a bounded queue with shared coalescing; no eager requests for the entire feed. Likes/dislikes remain local. No MAL login required for this update.
- Performance constraints: cache browse/related pages, bounded concurrent requests, no network requests directly on render; stable card identities survive metadata enrichment; independent players must not replace one another.
- Compatibility constraints: existing state files must load with empty feedback; preserve saved/history/downloads/settings and playback; existing profiles default to upscaling Off. Never replace a real provider title or invent a missing episode name.
- Test/screenshot expectations: verify native pause/resume, seek, rewind/forward, paused speed changes, volume/mute restoration, resize/fullscreen, and main-app synchronization; regression tests for feedback persistence/ranking/exclusion/undo/stale results; native screenshot at desktop size; live browse plus playback smoke; verify automatic visible playback without hover/click, single automatic selection, manual pause suppression, preserved pause/resume position, restart at zero, gesture cancellation, and navigation/viewport cleanup; inspect clean card layouts at native desktop width.

## Open questions
- No blocking questions. Assumptions: one local viewer, dark theme retained, English recommendations from the currently working HiAnime source.
