import test from 'node:test'
import assert from 'node:assert/strict'
import { actualEpisodeTitle, animeKey, availableSources, currentRendererAvailable, episodeLabel, feedbackFor, formatTime, hasUsableCatalogDetails, initialState, acknowledgedNumberDraft, acknowledgedSeekDraft, nextQueuedPlayerCommand, positiveVolumeMemory, relativeSeekPosition, nextTrailerToken, normalizeState, normalizedAnimeTitle, normalizedTagIds, percentage, playbackResolutionLabel, preserveUsableCatalogDetails, reconcileFeedbackState, rendererLabel, RequestGate, SerializedAsyncGate, chooseTrailerAutoplayCandidate, hasActiveManualTrailer, manualTrailerGesture, shouldApplyTrailerCommand, sameAnime, tagFilterKey, toggleTagId, trailerPreviewBounds, unreviewedExplore, visibleTags, withFeedback } from './lib.ts'
import type { Anime, State } from './types.ts'

test('restored nullable backend lists render as empty lists', () => {
  const restored = normalizeState({ ...initialState, saved: null, feedback: null, history: null, downloads: null } as unknown as State)
  assert.deepEqual(restored.saved, [])
  assert.deepEqual(restored.feedback, [])
  assert.deepEqual(restored.history, [])
  assert.deepEqual(restored.downloads, [])
})

test('legacy settings and player state get honest playback defaults', () => {
  const restored = normalizeState({ ...initialState, settings: { quality: '720p', mode: 'dub', downloadDir: '/tmp/anime', mpvPath: '' }, player: { active: true, paused: false, position: 0, duration: 0, volume: 90, error: '' } } as unknown as State)
  assert.equal(restored.settings.upscaler, 'off')
  assert.equal(restored.player.renderer, 'mpv')
  assert.equal(restored.player.speed, 1)
  assert.equal(restored.player.upscaling, false)
  assert.equal(restored.metalFXAvailable, false)
  assert.equal(restored.metalFXDevice, '')
  assert.equal(restored.trailerPreviewAvailable, false)
})

const anime: Anime = { id: 'source-one', title: 'Naruto: Shippuden', url: 'https://one.example/naruto', imageUrl: '', source: 'one', language: 'EN', description: '', genres: [], score: 0, episodeCount: 0 }

test('feedback follows equivalent titles across provider IDs without dropping non-Latin characters', () => {
  const otherSource = { ...anime, id: 'source-two', url: 'https://two.example/naruto', title: ' NARUTO — Shippuden ' }
  assert.equal(sameAnime(anime, otherSource), true)
  assert.equal(feedbackFor(withFeedback([], anime, 'dislike'), otherSource), 'dislike')
  assert.equal(animeKey({ ...anime, title: '進撃の巨人' }), '進撃の巨人')
  assert.equal(sameAnime(anime, { ...otherSource, title: 'Naruto' }), false)
})

test('MAL identity links catalog and stream provider copies before title fallback', () => {
  const mal = { ...anime, id: 'mal-20', source: 'mal', malId: 20, url: 'https://myanimelist.net/anime/20' }
  const provider = { ...anime, id: 'hianime-123', source: 'hianime', malId: 20, title: 'Naruto' }
  assert.equal(animeKey(mal), 'mal:20')
  assert.equal(sameAnime(mal, provider), true)
  const remake = { ...provider, id: 'mal-21', source: 'mal', malId: 21, title: mal.title, url: mal.url }
  assert.equal(sameAnime(mal, remake), false)
  assert.equal(feedbackFor(withFeedback([], mal, 'dislike'), remake), '')
  assert.equal(feedbackFor(withFeedback([], mal, 'dislike'), provider), 'dislike')
})

test('one-sided MAL identity still matches provider titles and aliases safely', () => {
  const mal = { ...anime, id: 'mal-20', source: 'mal', malId: 20, title: 'Kimi no Na wa.', alternateTitles: ['Your Name.', '君の名は。'], episodeCount: 1 }
  const provider = { ...anime, id: 'hianime-your-name', source: 'hianime', malId: undefined, title: 'Your Name', alternateTitles: [], episodeCount: 1 }
  const japaneseProvider = { ...provider, id: 'jp', title: '君の名は' }
  const remake = { ...provider, id: 'remake', url: 'https://stream.example/your-name-remake', title: 'Your Name.', episodeCount: 12 }
  assert.equal(normalizedAnimeTitle('Your Name.'), 'your name')
  assert.equal(sameAnime(mal, provider), true)
  assert.equal(sameAnime(mal, japaneseProvider), true)
  assert.equal(sameAnime(mal, remake), false)
})

test('feedback changes are exclusive and undo removes equivalent provider copies', () => {
  const otherSource = { ...anime, id: 'source-two', url: 'https://two.example/naruto' }
  const liked = withFeedback([], anime, 'like')
  const disliked = withFeedback(liked, otherSource, 'dislike')
  assert.equal(disliked.length, 1)
  assert.equal(feedbackFor(disliked, anime), 'dislike')
  assert.deepEqual(withFeedback(disliked, anime, ''), [])
  assert.equal(feedbackFor(liked, anime), 'like', 'optimistic updates must not mutate the confirmed snapshot')
})

test('an old preferences response cannot erase a confirmed like', () => {
  const confirmed = { ...initialState, feedback: withFeedback([], anime, 'like'), feedbackRevision: 1 }
  const oldPreferencesResponse = { ...initialState, settings: { ...initialState.settings, quality: '720p' } }
  const reconciled = reconcileFeedbackState(oldPreferencesResponse, confirmed)
  assert.equal(feedbackFor(reconciled.feedback, anime), 'like')
  assert.equal(reconciled.feedbackRevision, 1)
  assert.equal(reconciled.settings.quality, '720p', 'unrelated state must still update')
})

test('an old full state response cannot restore feedback that was cleared', () => {
  const beforeClear = { ...initialState, feedback: withFeedback([], anime, 'dislike'), feedbackRevision: 1 }
  const confirmedClear = { ...initialState, feedback: [], feedbackRevision: 2 }
  const reconciled = reconcileFeedbackState(beforeClear, confirmedClear)
  assert.deepEqual(reconciled.feedback, [])
  assert.equal(reconciled.feedbackRevision, 2)
  const newerLike = { ...initialState, feedback: withFeedback([], anime, 'like'), feedbackRevision: 3 }
  assert.equal(feedbackFor(reconcileFeedbackState(newerLike, reconciled).feedback, anime), 'like')
})

test('older Explore responses cannot replace a newer request or feedback revision', () => {
  const gate = new RequestGate()
  const initialBrowse = gate.begin()
  gate.invalidate() // A dislike arrives while browse is loading.
  assert.equal(gate.accepts(initialBrowse), false)
  const refreshed = gate.begin()
  const newest = gate.begin()
  assert.equal(gate.accepts(refreshed), false)
  assert.equal(gate.accepts(newest), true)
})



test('television format labels share feedback while season identity is retained', () => {
  const mal = { ...anime, id: 'mal:40748', url: 'https://myanimelist.net/anime/40748', malId: 40748, title: 'Jujutsu Kaisen' }
  assert.equal(sameAnime(mal, { ...anime, title: 'Jujutsu Kaisen (TV)' }), true)
  assert.equal(sameAnime(mal, { ...anime, title: 'Jujutsu Kaisen Season 2' }), false)
})

test('latest open gate rejects stale modal detail responses after close', async () => {
  const gate = new RequestGate()
  const firstOpen = gate.begin()
  gate.invalidate()
  await Promise.resolve()
  assert.equal(gate.accepts(firstOpen), false)
  const secondOpen = gate.begin()
  assert.equal(gate.accepts(secondOpen), true)
})

test('manual trailer gestures map single and double clicks to stable actions', () => {
  assert.equal(manualTrailerGesture(undefined, 1), 'start')
  assert.equal(manualTrailerGesture('playing', 1), 'pause')
  assert.equal(manualTrailerGesture('starting', 1), 'pause')
  assert.equal(manualTrailerGesture('paused', 1), 'resume')
  assert.equal(manualTrailerGesture('hidden', 1), 'resume')
  assert.equal(manualTrailerGesture('playing', 2), 'restart')
  assert.equal(manualTrailerGesture('failed', 2), 'start')
  assert.equal(manualTrailerGesture('unavailable', 2), 'start')
})



test('autoplay scheduler chooses the first eligible visible trailer and keeps an active eligible player', () => {
  const candidates = [
    { cardKey: 'later', order: 3, hasTrailer: true, visible: true },
    { cardKey: 'blocked', order: 1, hasTrailer: true, visible: true, blocked: true },
    { cardKey: 'missing', order: 0, hasTrailer: false, visible: true },
    { cardKey: 'first', order: 2, hasTrailer: true, visible: true },
  ]
  assert.equal(chooseTrailerAutoplayCandidate(candidates), 'first')
  assert.equal(chooseTrailerAutoplayCandidate(candidates, 'later'), 'later')
  assert.equal(chooseTrailerAutoplayCandidate(candidates, 'blocked'), 'first')

  assert.equal(chooseTrailerAutoplayCandidate(candidates.map(candidate => candidate.cardKey === 'first' ? { ...candidate, suppressed: true } : candidate)), 'later')
  assert.equal(chooseTrailerAutoplayCandidate(candidates.map(candidate => candidate.hasTrailer ? { ...candidate, suppressed: true } : candidate)), '')
  assert.equal(chooseTrailerAutoplayCandidate(candidates.map(candidate => ({ ...candidate, visible: false }))), '')
})

test('autoplay suspends only for actively playing manual trailers', () => {
  assert.equal(hasActiveManualTrailer([{ mode: 'manual', phase: 'playing', cover: false }]), true)
  assert.equal(hasActiveManualTrailer([{ mode: 'manual', phase: 'starting', cover: false }]), true)
  assert.equal(hasActiveManualTrailer([{ mode: 'manual', phase: 'starting', cover: true }]), true)
  assert.equal(hasActiveManualTrailer([{ mode: 'manual', phase: 'playing', cover: true }]), false)
  assert.equal(hasActiveManualTrailer([{ mode: 'manual', phase: 'paused', cover: true }]), false)
  assert.equal(hasActiveManualTrailer([{ mode: 'auto', phase: 'playing', cover: false }]), false)
})


test('serialized async gate queues scheduler reruns and rejects stale awaited work', async () => {
  const gate = new SerializedAsyncGate()
  const first = gate.begin()
  assert.equal(first.run, true)
  const overlapped = gate.begin()
  assert.equal(overlapped.run, false)
  gate.invalidate()
  await Promise.resolve()
  assert.equal(gate.accepts(first.generation), false)
  assert.equal(gate.finish(), true)
  const second = gate.begin()
  assert.equal(second.run, true)
  assert.equal(gate.accepts(second.generation), true)
  assert.equal(gate.finish(), false)
})

test('trailer async command guard requires the same token and revision', () => {
  assert.equal(shouldApplyTrailerCommand(7, 3, 7, 3), true)
  assert.equal(shouldApplyTrailerCommand(8, 3, 7, 3), false)
  assert.equal(shouldApplyTrailerCommand(7, 4, 7, 3), false)
})

test('Explore immediately excludes reviewed, saved and watched provider copies', () => {
  const liked = { ...anime, id: 'liked', title: 'Liked anime', url: 'https://example/liked' }
  const disliked = { ...anime, id: 'disliked', title: 'Disliked anime', url: 'https://example/disliked' }
  const saved = { ...anime, id: 'saved', title: 'Saved anime', url: 'https://example/saved' }
  const watched = { ...anime, id: 'watched', title: 'Watched anime', url: 'https://example/watched' }
  const known = { ...initialState, saved: [saved], feedback: [...withFeedback([], liked, 'like'), ...withFeedback([], disliked, 'dislike')], history: [{ anime: watched, episode: { number: '1', title: '', url: '' }, position: 20, duration: 100, updatedAt: '' }] }
  const items = [liked, disliked, saved, watched, anime].map(item => ({ anime: { ...item, id: `other-${item.id}`, url: `${item.url}-copy` }, reason: 'Popular' }))
  assert.deepEqual(unreviewedExplore(items, known).map(item => item.anime.title), [anime.title])
  const mal = { ...anime, id: 'mal-20', source: 'mal', malId: 20, url: 'https://myanimelist.net/anime/20' }
  const provider = { ...anime, id: 'hianime-20', source: 'hianime', malId: 20, url: 'https://stream.example/naruto' }
  assert.deepEqual(unreviewedExplore([{ anime: provider, reason: 'MAL' }], { ...initialState, saved: [mal] }), [])
})

test('unknown playback duration never implies completed watching', () => {
  assert.equal(percentage(120, 0), 0)
  assert.equal(percentage(120, Number.NaN), 0)
  assert.equal(percentage(300, 200), 100)
  assert.equal(percentage(-1, 200), 0)
})

test('provider filtering respects actual source language', () => {
  const sources = [{ id: 'anidb', name: 'AniDB', language: 'EN' }, { id: 'animefire', name: 'AnimeFire', language: 'PT-BR' }]
  assert.equal(availableSources(sources, 'EN')[0].id, 'anidb')
  assert.equal(availableSources(sources, 'PT-BR').length, 1)
  assert.equal(availableSources(sources, 'all').length, 2)
})


test('catalog metadata source alone does not count as fetched details', () => {
  const thin = { ...anime, metadataSource: 'myanimelist', tags: [], trailer: null, alternateTitles: [], description: '' }
  assert.equal(hasUsableCatalogDetails(thin), false)
  assert.equal(hasUsableCatalogDetails({ ...thin, trailer: { youtubeId: 'abc123', url: 'https://youtu.be/abc123' } }), true)
  assert.equal(hasUsableCatalogDetails({ ...thin, tags: [{ id: 1, name: 'Romance', kind: 'genre' }], description: 'A story.', alternateTitles: ['English title'] }), true)
})


test('visible-card cache writes keep hydrated trailer details over thin base entries', () => {
  const thin = { ...anime, id: 'mal-thin', source: 'mal', metadataSource: 'myanimelist', trailer: null, tags: [], description: '', alternateTitles: [] }
  const hydrated = { ...thin, trailer: { youtubeId: 'abc123', url: 'https://youtu.be/abc123' }, description: 'Hydrated details' }
  assert.equal(preserveUsableCatalogDetails(hydrated, thin).trailer?.youtubeId, 'abc123')
  assert.equal(preserveUsableCatalogDetails(thin, hydrated).trailer?.youtubeId, 'abc123')
})

test('tag filters are stable, toggleable request keys', () => {
  assert.deepEqual(normalizedTagIds([9, 2, 9, -1, 0, 2]), [2, 9])
  assert.deepEqual(toggleTagId([9, 2], 9), [2])
  assert.deepEqual(toggleTagId([9, 2], 4), [2, 4, 9])
  assert.deepEqual(toggleTagId([1, 2, 3], 4), [1, 2, 3])
  assert.equal(tagFilterKey([9, 2, 9]), '2,9')
  assert.deepEqual(visibleTags({ ...anime, tags: [{ id: 1, name: 'Action', kind: 'genre' }, { id: 0, name: 'Bad', kind: 'genre' }, { id: 2, name: 'Shounen', kind: 'theme' }, { id: 3, name: 'Ninja', kind: 'theme' }] }), [{ id: 1, name: 'Action', kind: 'genre' }, { id: 2, name: 'Shounen', kind: 'theme' }, { id: 3, name: 'Ninja', kind: 'theme' }])
})


test('player control helpers keep drafts until acknowledgement and drop stale queued sessions', () => {
  assert.equal(acknowledgedNumberDraft(50, 49), 50)
  assert.equal(acknowledgedNumberDraft(50, 50), null)
  assert.equal(positiveVolumeMemory(70, 0), 70)
  assert.equal(positiveVolumeMemory(70, 35), 35)
  assert.equal(relativeSeekPosition(10, 20, 100, null), 30)
  assert.equal(relativeSeekPosition(10, 20, 100, 30), 40)
  assert.equal(relativeSeekPosition(-50, 20, 100, 10), 0)
  assert.equal(relativeSeekPosition(100, 20, 60, 30), 60)
  assert.equal(acknowledgedSeekDraft(50, 40, false, 1), 50)
  assert.equal(acknowledgedSeekDraft(50, 50, true, 1), null)
  assert.equal(acknowledgedSeekDraft(50, 51, false, 1), null)
  assert.equal(acknowledgedSeekDraft(50, 60, false, 1), 50)
  const queue = new Map([['volume', { value: 0, session: 'old' }]])
  assert.equal(nextQueuedPlayerCommand(queue, 'volume', 'new'), null)
  queue.set('volume', { value: 80, session: 'new' })
  assert.equal(nextQueuedPlayerCommand(queue, 'volume', 'new'), 80)
})

test('trailer preview tokens and bounds require an unclipped poster-sized frame', () => {
  assert.equal(nextTrailerToken(50, 40), 51)
  assert.equal(nextTrailerToken(0, 123), 123)
  assert.deepEqual(trailerPreviewBounds({ x: 10, y: 100, width: 220, height: 260 }, { x: 0, y: 64, width: 800, height: 500 }), { x: 10, y: 100, width: 220, height: 260 })
  assert.equal(trailerPreviewBounds({ x: 10, y: 100, width: 180, height: 260 }, { x: 0, y: 64, width: 800, height: 500 }), null)
  assert.equal(trailerPreviewBounds({ x: 10, y: 60, width: 220, height: 260 }, { x: 0, y: 64, width: 800, height: 500 }), null)
  assert.equal(trailerPreviewBounds({ x: 10, y: 400, width: 220, height: 260 }, { x: 0, y: 64, width: 800, height: 500 }), null)
})

test('selected playback renderer controls whether watch actions are available', () => {
  assert.equal(currentRendererAvailable({ ...initialState, mpvAvailable: true }), true)
  assert.equal(currentRendererAvailable({ ...initialState, mpvAvailable: false, metalFXAvailable: true }), false)
  assert.equal(currentRendererAvailable({ ...initialState, settings: { ...initialState.settings, upscaler: 'metalfx' }, mpvAvailable: false, metalFXAvailable: true }), true)
  assert.equal(currentRendererAvailable({ ...initialState, settings: { ...initialState.settings, upscaler: 'metalfx' }, mpvAvailable: true, metalFXAvailable: false }), false)
})

test('player labels show the native renderer and only real upscale resolutions', () => {
  assert.equal(rendererLabel('metalfx'), 'MetalFX')
  assert.equal(rendererLabel('mpv'), 'mpv')
  assert.equal(playbackResolutionLabel({ ...initialState.player, upscaling: false, videoWidth: 1280, videoHeight: 720, outputWidth: 2560, outputHeight: 1440 }), '')
  assert.equal(playbackResolutionLabel({ ...initialState.player, upscaling: true, videoWidth: 1280, videoHeight: 720, outputWidth: 2560, outputHeight: 1440 }), '1280x720 -> 2560x1440')
  assert.equal(playbackResolutionLabel({ ...initialState.player, upscaling: true, videoWidth: 0, videoHeight: 720, outputWidth: 2560, outputHeight: 1440 }), '')
})

test('episode names use supplied titles without inventing generic names', () => {
  assert.equal(episodeLabel({ number: '4', title: '', url: '' }), 'Episode 4')
  assert.equal(actualEpisodeTitle({ number: '4', title: 'The Red Thread', url: '' }), 'The Red Thread')
  assert.equal(actualEpisodeTitle({ number: '4', title: 'Episode 4', url: '' }), '')
  assert.equal(actualEpisodeTitle({ number: '4', title: '  ', url: '' }), '')
})

test('playback times are stable for uninitialized, long and negative values', () => {
  assert.equal(formatTime(Number.NaN), '0:00')
  assert.equal(formatTime(-3), '0:00')
  assert.equal(formatTime(3661), '1:01:01')
})
