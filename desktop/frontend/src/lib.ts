import type { Anime, AnimeTag, Episode, ExploreItem, Feedback, FeedbackValue, PlayerState, Source, State } from './types.ts'

export const initialState: State = {
  settings: { quality: 'best', mode: 'sub', downloadDir: '', mpvPath: '', upscaler: 'off' },
  sources: [], history: [], saved: [], feedback: [], feedbackRevision: 0, downloads: [],
  player: { active: false, paused: false, position: 0, duration: 0, volume: 100, speed: 1, renderer: 'mpv', upscaling: false, videoWidth: 0, videoHeight: 0, outputWidth: 0, outputHeight: 0, error: '' },
  mpvAvailable: false,
  metalFXAvailable: false,
  metalFXDevice: '',
  metalFXReason: '',
  trailerPreviewAvailable: false,
}

export function normalizeState(value: State): State {
  return {
    ...initialState, ...value,
    settings: { ...initialState.settings, ...value.settings },
    player: { ...initialState.player, ...value.player },
    sources: value.sources ?? [], history: value.history ?? [],
    saved: value.saved ?? [], feedback: value.feedback ?? [], downloads: value.downloads ?? [],
    feedbackRevision: value.feedbackRevision ?? 0,
  }
}

export function reconcileFeedbackState(incoming: State, confirmed: State): State {
  const next = normalizeState(incoming)
  return next.feedbackRevision < confirmed.feedbackRevision
    ? { ...next, feedback: confirmed.feedback, feedbackRevision: confirmed.feedbackRevision }
    : next
}

export function normalizedAnimeTitle(title: string): string {
  return title.trim().replace(/\s*\(TV\)$/i, '').toLowerCase().replace(/[^\p{L}\p{N}]+/gu, ' ').trim()
}

export function animeKey(anime: Anime): string {
  if (anime.malId && anime.malId > 0) return `mal:${anime.malId}`
  return normalizedAnimeTitle(anime.title) || anime.id
}

function titleCandidates(anime: Anime): string[] {
  return [anime.title, ...(anime.alternateTitles ?? [])].map(normalizedAnimeTitle).filter(Boolean)
}

function compatibleEpisodeCount(left: Anime, right: Anime): boolean {
  return !(left.episodeCount > 0 && right.episodeCount > 0 && left.episodeCount !== right.episodeCount)
}

export function sameAnime(left: Anime, right: Anime): boolean {
  if (left.malId && right.malId) return left.malId === right.malId
  if ((!!left.id && left.id === right.id) || (!!left.url && left.url === right.url)) return true
  if (!compatibleEpisodeCount(left, right)) return false
  const rightTitles = new Set(titleCandidates(right))
  return titleCandidates(left).some(title => rightTitles.has(title))
}

export function feedbackFor(feedback: Feedback[], anime: Anime): FeedbackValue | '' {
  return feedback.find(item => sameAnime(item.anime, anime))?.value ?? ''
}

export function withFeedback(feedback: Feedback[], anime: Anime, value: FeedbackValue | ''): Feedback[] {
  const remaining = feedback.filter(item => !sameAnime(item.anime, anime))
  return value ? [{ anime, value, updatedAt: new Date().toISOString() }, ...remaining] : remaining
}

export function unreviewedExplore(items: ExploreItem[], state: State): ExploreItem[] {
  const known = [...state.feedback.map(item => item.anime), ...state.saved, ...state.history.map(item => item.anime)]
  return items.filter(item => !known.some(anime => sameAnime(anime, item.anime)))
}

// Every feedback change invalidates in-flight recommendation responses.
export class RequestGate {
  private revision = 0
  begin(): number { return ++this.revision }
  invalidate(): void { this.revision++ }
  accepts(revision: number): boolean { return revision === this.revision }
}

export function availableSources(sources: Source[], language: string): Source[] {
  return language === 'all' ? sources : sources.filter(source => source.language === language)
}

export function visibleTags(anime: Anime, limit = 3): AnimeTag[] {
  return (anime.tags ?? []).filter(tag => tag.id > 0 && tag.name.trim()).slice(0, limit)
}


export function hasUsableCatalogDetails(anime: Anime): boolean {
  const hasTrailer = Boolean(anime.trailer?.youtubeId)
  const hasTags = visibleTags(anime, Number.MAX_SAFE_INTEGER).length > 0
  const hasDescription = Boolean(anime.description?.trim())
  const hasAliases = (anime.alternateTitles ?? []).some(title => title.trim())
  return hasTrailer || (hasTags && hasDescription && hasAliases)
}


export function preserveUsableCatalogDetails(current: Anime | undefined, incoming: Anime): Anime {
  return current && hasUsableCatalogDetails(current) && !hasUsableCatalogDetails(incoming) ? current : incoming
}

export function normalizedTagIds(tagIds: number[]): number[] {
  return [...new Set(tagIds.filter(id => Number.isInteger(id) && id > 0))].sort((a, b) => a - b)
}

export function toggleTagId(tagIds: number[], tagId: number, limit = 3): number[] {
  const normalized = normalizedTagIds(tagIds)
  if (normalized.includes(tagId)) return normalized.filter(id => id !== tagId)
  return normalized.length >= limit ? normalized : normalizedTagIds([...normalized, tagId])
}

export function tagFilterKey(tagIds: number[]): string {
  return normalizedTagIds(tagIds).join(',')
}


export function acknowledgedNumberDraft(draft: number | null, confirmed: number, tolerance = 0.001): number | null {
  return draft !== null && Math.abs(draft - confirmed) <= tolerance ? null : draft
}

export function positiveVolumeMemory(previous: number, value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : previous
}

export function relativeSeekPosition(delta: number, position: number, duration: number, draft: number | null = null): number {
  const base = draft ?? position
  const ceiling = duration > 0 && Number.isFinite(duration) ? duration : Number.MAX_SAFE_INTEGER
  return Math.max(0, Math.min(ceiling, base + delta))
}

export function acknowledgedSeekDraft(draft: number | null, position: number, paused: boolean, speed = 1): number | null {
  if (draft === null || !Number.isFinite(position)) return draft
  const delta = position - draft
  if (Math.abs(delta) <= (paused ? 0.25 : 0.35)) return null
  const playbackWindow = Math.max(1, Number.isFinite(speed) && speed > 0 ? speed : 1) * 1.25
  return !paused && delta >= 0 && delta <= playbackWindow ? null : draft
}

export interface QueuedPlayerCommand { value: number; session: string }

export function nextQueuedPlayerCommand(queue: Map<string, QueuedPlayerCommand>, name: string, currentSession: string): number | null {
  const current = queue.get(name)
  if (!current) return null
  queue.delete(name)
  return current.session === currentSession ? current.value : null
}

export function nextTrailerToken(previous: number, now = Date.now()): number {
  return Math.max(previous + 1, now, 1)
}

export type ManualTrailerPhase = 'idle' | 'starting' | 'playing' | 'paused' | 'hidden' | 'unavailable' | 'failed'
export type TrailerPlaybackMode = 'manual' | 'auto'
export type ManualTrailerAction = 'start' | 'pause' | 'resume' | 'restart'

export function manualTrailerGesture(phase: ManualTrailerPhase | undefined, clickCount: number): ManualTrailerAction {
  if (clickCount > 1) return phase && phase !== 'idle' && phase !== 'unavailable' && phase !== 'failed' ? 'restart' : 'start'
  if (phase === 'playing' || phase === 'starting') return 'pause'
  if (phase === 'paused' || phase === 'hidden') return 'resume'
  return 'start'
}



export function shouldApplyTrailerCommand(currentToken: number, currentRevision: number, expectedToken: number, expectedRevision: number): boolean {
  return currentToken === expectedToken && currentRevision === expectedRevision
}

export interface TrailerAutoplayCandidate { cardKey: string; order: number; hasTrailer: boolean; visible: boolean; blocked?: boolean; suppressed?: boolean }

export function chooseTrailerAutoplayCandidate(candidates: TrailerAutoplayCandidate[], activeCardKey = ''): string {
  const eligible = candidates
    .filter(candidate => candidate.visible && candidate.hasTrailer && !candidate.blocked && !candidate.suppressed)
    .sort((left, right) => left.order - right.order)
  if (activeCardKey && eligible.some(candidate => candidate.cardKey === activeCardKey)) return activeCardKey
  return eligible[0]?.cardKey ?? ''
}

export function hasActiveManualTrailer(players: { mode?: TrailerPlaybackMode; phase: ManualTrailerPhase; cover: boolean }[]): boolean {
  return players.some(player => player.mode === 'manual' && (player.phase === 'starting' || (!player.cover && player.phase === 'playing')))
}

export class SerializedAsyncGate {
  private generation = 0
  private running = false
  private queued = false

  invalidate(): number { return ++this.generation }

  begin(): { run: boolean; generation: number } {
    if (this.running) { this.queued = true; return { run: false, generation: this.generation } }
    this.running = true
    this.queued = false
    return { run: true, generation: ++this.generation }
  }

  accepts(generation: number): boolean { return generation === this.generation }

  finish(): boolean {
    this.running = false
    const queued = this.queued
    this.queued = false
    return queued
  }
}

export interface PreviewRect { x: number; y: number; width: number; height: number }

export function trailerPreviewBounds(rect: PreviewRect, viewport: PreviewRect, minimum = 200): PreviewRect | null {
  const width = Math.round(rect.width)
  const height = Math.round(rect.height)
  const x = Math.round(rect.x)
  const y = Math.round(rect.y)
  if (width < minimum || height < minimum) return null
  if (x < viewport.x || y < viewport.y) return null
  if (x + width > viewport.x + viewport.width) return null
  if (y + height > viewport.y + viewport.height) return null
  return { x, y, width, height }
}

export function currentRendererAvailable(state: State): boolean {
  return state.settings.upscaler === 'metalfx' ? state.metalFXAvailable : state.mpvAvailable
}

export function rendererLabel(renderer: PlayerState['renderer']): string {
  return renderer === 'metalfx' ? 'MetalFX' : 'mpv'
}

export function actualEpisodeTitle(episode: Episode | undefined): string {
  const title = episode?.title.trim() ?? ''
  if (!title) return ''
  const number = episode?.number.trim()
  if (number && new RegExp(`^episode\\s*${escapeRegExp(number)}$`, 'i').test(title)) return ''
  return title
}

export function episodeLabel(episode: Episode | undefined): string {
  const number = episode?.number.trim()
  return number ? `Episode ${number}` : 'Episode'
}

export function playbackResolutionLabel(player: PlayerState): string {
  return player.upscaling && player.videoWidth > 0 && player.videoHeight > 0 && player.outputWidth > 0 && player.outputHeight > 0
    ? `${player.videoWidth}x${player.videoHeight} -> ${player.outputWidth}x${player.outputHeight}`
    : ''
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

export function percentage(position: number, duration: number): number {
  return duration > 0 && Number.isFinite(position) && Number.isFinite(duration)
    ? Math.min(100, Math.max(0, position / duration * 100)) : 0
}

export function formatTime(seconds: number): string {
  const safe = Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0
  const hours = Math.floor(safe / 3600)
  const minutes = Math.floor(safe / 60) % 60
  const rest = String(safe % 60).padStart(2, '0')
  return hours ? `${hours}:${String(minutes).padStart(2, '0')}:${rest}` : `${minutes}:${rest}`
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  return typeof error === 'string' ? error : 'Something went wrong. Please try again.'
}
