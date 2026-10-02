import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Icon, type IconName } from './icons'
import { actualEpisodeTitle, animeKey, availableSources, currentRendererAvailable, episodeLabel, errorMessage, feedbackFor, formatBytes, formatTime, initialState, nextTrailerToken, percentage, playbackResolutionLabel, reconcileFeedbackState, rendererLabel, RequestGate, sameAnime, tagFilterKey, toggleTagId, trailerPreviewBounds, unreviewedExplore, visibleTags, withFeedback, hasUsableCatalogDetails, preserveUsableCatalogDetails, acknowledgedNumberDraft, acknowledgedSeekDraft, nextQueuedPlayerCommand, positiveVolumeMemory, relativeSeekPosition, manualTrailerGesture, shouldApplyTrailerCommand, chooseTrailerAutoplayCandidate, hasActiveManualTrailer, SerializedAsyncGate } from './lib'
import type { Anime, AnimeTag, Download, Episode, ExploreResult, FeedbackValue, HistoryEntry, SearchWarning, Settings, State, TrailerPreviewStatus } from './types'
import type { ManualTrailerPhase, TrailerPlaybackMode } from './lib'

type Page = 'home' | 'explore' | 'search' | 'library' | 'downloads' | 'settings'
type Connection = 'loading' | 'connected' | 'preview' | 'failed'
const playbackSpeeds = [0.5, 0.75, 1, 1.25, 1.5, 2]
const navigation: { id: Page; label: string; icon: IconName }[] = [
  { id: 'home', label: 'Home', icon: 'home' },
  { id: 'explore', label: 'Explore', icon: 'explore' },
  { id: 'search', label: 'Search', icon: 'search' },
  { id: 'library', label: 'Library', icon: 'library' },
  { id: 'downloads', label: 'Downloads', icon: 'download' },
]
type TrailerPlayer = { token: number; cardKey: string; youtubeId: string; element: HTMLElement; phase: ManualTrailerPhase; message: string; position: number; cover: boolean; visible: boolean; commandAt: number; revision: number; mode: TrailerPlaybackMode; viewerPaused: boolean }

function Button({ children, icon, variant = 'secondary', className = '', ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon?: IconName; variant?: 'primary' | 'secondary' | 'ghost' | 'icon' }) {
  return <button className={`button button-${variant} ${className}`} {...props}>{icon && <Icon name={icon} size={17} />}{children}</button>
}

function Poster({ anime, compact = false }: { anime: Anime; compact?: boolean }) {
  const [failed, setFailed] = useState(false)
  useEffect(() => setFailed(false), [anime.imageUrl])
  return <div className={`poster ${compact ? 'poster-compact' : ''}`}>
    {anime.imageUrl && !failed ? <img src={anime.imageUrl} alt={`Cover of ${anime.title}`} loading="lazy" onError={() => setFailed(true)} referrerPolicy="no-referrer" /> : <div className="poster-placeholder"><Icon name="film" size={compact ? 20 : 35} /><span>{compact ? '' : 'Artwork unavailable'}</span></div>}
  </div>
}

function EmptyState({ icon, title, children, action }: { icon: IconName; title: string; children: ReactNode; action?: ReactNode }) {
  return <div className="empty-state"><div className="empty-icon"><Icon name={icon} size={28} /></div><h3>{title}</h3><p>{children}</p>{action}</div>
}

function FeedbackButtons({ anime, value, onChange, disabled }: { anime: Anime; value: FeedbackValue | ''; onChange: (value: FeedbackValue) => void; disabled: boolean }) {
  return <div className="feedback-actions" role="group" aria-label={`Recommendations for ${anime.title}`}>
    {(['like', 'dislike'] as const).map(action => <button key={action} className={`feedback-button ${value === action ? `selected-${action}` : ''}`} aria-pressed={value === action} aria-label={`${action === 'like' ? 'Like' : 'Dislike'} ${anime.title}${value === action ? ', click to undo' : ''}`} title={value === action ? `Undo ${action}` : action === 'like' ? 'Recommend more like this' : 'Hide from Explore'} onClick={() => onChange(action)} disabled={disabled}><Icon name={action} size={15} /><span>{action === 'like' ? 'Like' : 'Dislike'}</span></button>)}
  </div>
}

function TagDisclosure({ anime, initialLimit = 3, className = 'card-tags' }: { anime: Anime; initialLimit?: number; className?: string }) {
  const tags = visibleTags(anime, Number.MAX_SAFE_INTEGER)
  const [expanded, setExpanded] = useState(false)
  useEffect(() => setExpanded(false), [animeKey(anime)])
  if (tags.length === 0) return null
  const shown = expanded ? tags : tags.slice(0, initialLimit)
  const hidden = tags.length - shown.length
  return <div className={className}>{shown.map(tag => <span key={tag.id}>{tag.name}</span>)}{hidden > 0 && <button type="button" aria-label={`Show ${hidden} more tags`} onClick={() => setExpanded(true)}>{className === 'card-tags' ? `+${hidden}` : `More tags +${hidden}`}</button>}{expanded && tags.length > initialLimit && <button type="button" onClick={() => setExpanded(false)}>Show less</button>}</div>
}

function AnimeCard({ cardKey, order, anime, trailer, onOpen, onSave, onFeedback, onVisible, onVisibility, onUnmountCard, onTrailerClick, onStopTrailer, onOpenTrailerFallback, saved, value, reason, disabled, feedbackBusy }: { cardKey: string; order: number; anime: Anime; trailer?: TrailerPlayer; onOpen: () => void; onSave: () => void; onFeedback: (value: FeedbackValue) => void; onVisible: (anime: Anime, cardKey: string) => void; onVisibility: (anime: Anime, cardKey: string, element: HTMLElement, visible: boolean, order: number) => void; onUnmountCard: (cardKey: string) => void; onTrailerClick: (anime: Anime, cardKey: string, element: HTMLElement, clickCount: number) => void; onStopTrailer: () => void; onOpenTrailerFallback: (anime: Anime) => void; saved: boolean; value: FeedbackValue | ''; reason?: string; disabled: boolean; feedbackBusy: boolean }) {
  const posterRef = useRef<HTMLDivElement>(null)
  const stopOnUnmount = useRef(onStopTrailer)
  const latestAnime = useRef(anime)
  const latestOnVisible = useRef(onVisible)
  const latestOnVisibility = useRef(onVisibility)
  const latestOnUnmountCard = useRef(onUnmountCard)
  useEffect(() => { stopOnUnmount.current = onStopTrailer }, [onStopTrailer])
  useEffect(() => { latestAnime.current = anime; latestOnVisible.current = onVisible; latestOnVisibility.current = onVisibility; latestOnUnmountCard.current = onUnmountCard }, [anime, onVisible, onVisibility, onUnmountCard])
  useEffect(() => () => { stopOnUnmount.current(); latestOnUnmountCard.current(cardKey) }, [cardKey])
  useEffect(() => {
    const node = posterRef.current
    if (!node) return
    let seen = false
    const observer = new IntersectionObserver(entries => {
      const visible = entries.some(entry => entry.isIntersecting)
      latestOnVisibility.current(latestAnime.current, cardKey, node, visible, order)
      if (!seen && visible) {
        seen = true
        latestOnVisible.current(latestAnime.current, cardKey)
      }
    }, { threshold: 0.35 })
    observer.observe(node)
    latestOnVisibility.current(latestAnime.current, cardKey, node, false, order)
    return () => {
      observer.disconnect()
      latestOnVisibility.current(latestAnime.current, cardKey, node, false, order)
    }
  }, [cardKey, order])
  const errorTrailer = trailer && (trailer.phase === 'unavailable' || trailer.phase === 'failed')
  const showCover = !trailer || trailer.cover || trailer.phase === 'paused' || trailer.phase === 'hidden' || errorTrailer
  const actionLabel = errorTrailer && trailer.youtubeId ? 'Open trailer on YouTube' : trailer?.phase === 'playing' || trailer?.phase === 'starting' ? 'Pause trailer' : trailer?.phase === 'paused' || trailer?.phase === 'hidden' ? 'Resume trailer' : 'Play trailer'
  return <article className="anime-card">
    <div ref={posterRef} className="poster-frame">
      <button className={`anime-card-open trailer-cover ${errorTrailer ? 'trailer-cover-error' : ''}`} onClick={event => { event.preventDefault(); if (!posterRef.current) return; if (errorTrailer && trailer?.youtubeId) onOpenTrailerFallback(anime); else onTrailerClick(anime, cardKey, posterRef.current, event.detail || 1) }} aria-label={`${actionLabel} for ${anime.title}`}>
        {showCover && <><Poster anime={anime} /><div className="poster-shade"><span className="poster-action"><Icon name={trailer?.phase === 'paused' || trailer?.phase === 'hidden' ? 'play' : 'play'} size={19} /><span className="visually-hidden">{actionLabel}</span></span>{errorTrailer && <span className="trailer-cover-message">{trailer.message || 'Trailer unavailable'}{trailer.youtubeId ? ' · Open on YouTube' : ''}</span>}</div></>}
      </button>
    </div>
    <div className="anime-card-info"><div className="card-title-row"><button className="text-button card-title" onClick={onOpen}>{anime.title}</button><button className={`save-card ${saved ? 'is-saved' : ''}`} title={saved ? 'Remove from saved' : 'Save for later'} aria-pressed={saved} aria-label={`${saved ? 'Remove' : 'Save'} ${anime.title} ${saved ? 'from saved' : 'for later'}`} onClick={onSave} disabled={disabled}><Icon name={saved ? 'check' : 'plus'} size={16} /></button></div>{reason ? <p className="card-reason">{reason}</p> : <div className="card-meta"><span>{anime.source === 'mal' ? 'Catalogue' : anime.language || 'Language unknown'}</span><span aria-hidden="true">·</span><span>{anime.source === 'mal' ? 'MyAnimeList' : anime.source}</span></div>}<TagDisclosure anime={anime} /><div className="card-actions"><FeedbackButtons anime={anime} value={value} onChange={onFeedback} disabled={feedbackBusy} /><Button variant="ghost" icon="arrow" disabled={disabled} onClick={onOpen}>Episodes</Button></div></div>
  </article>
}

export default function App() {
  const [state, setState] = useState<State>(initialState)
  const [connection, setConnection] = useState<Connection>('loading')
  const [connectionError, setConnectionError] = useState('')
  const [page, setPage] = useState<Page>('home')
  const [query, setQuery] = useState('')
  const [source, setSource] = useState('')
  const [language, setLanguage] = useState('all')
  const [results, setResults] = useState<Anime[]>([])
  const [searched, setSearched] = useState('')
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState('')
  const [searchWarnings, setSearchWarnings] = useState<SearchWarning[]>([])
  const [selected, setSelected] = useState<Anime | null>(null)
  const [episodes, setEpisodes] = useState<Episode[]>([])
  const [episodesLoading, setEpisodesLoading] = useState(false)
  const [episodesError, setEpisodesError] = useState('')
  const [episodeFilter, setEpisodeFilter] = useState('')
  const [busy, setBusy] = useState<string[]>([])
  const [toast, setToast] = useState<{ message: string; error: boolean } | null>(null)
  const [settingsDraft, setSettingsDraft] = useState<Settings>(initialState.settings)
  const [settingsDirty, setSettingsDirty] = useState(false)
  const [showAllHistory, setShowAllHistory] = useState(false)
  const [libraryView, setLibraryView] = useState<'saved' | 'like' | 'dislike'>('saved')
  const [explore, setExplore] = useState<ExploreResult>({ items: [], personalized: false, message: '', warnings: [] })
  const [exploring, setExploring] = useState(false)
  const [exploreError, setExploreError] = useState('')
  const [exploreFilterKey, setExploreFilterKey] = useState('')
  const [tags, setTags] = useState<AnimeTag[]>([])
  const [tagsLoading, setTagsLoading] = useState(false)
  const [tagsError, setTagsError] = useState('')
  const [selectedTagIds, setSelectedTagIds] = useState<number[]>([])
  const [showAllFilterTags, setShowAllFilterTags] = useState(false)
  const [metadataCache, setMetadataCache] = useState<Record<string, Anime>>({})
  const [trailerPlayers, setTrailerPlayers] = useState<Record<string, TrailerPlayer>>({})
  const [reducedMotion, setReducedMotion] = useState(false)
  const [seekDraft, setSeekDraft] = useState<number | null>(null)
  const [volumeDraft, setVolumeDraft] = useState<number | null>(null)
  const [speedDraft, setSpeedDraft] = useState<number | null>(null)
  const searchVersion = useRef(0)
  const episodeVersion = useRef(0)
  const locks = useRef(new Set<string>())
  const pendingPlayerCommands = useRef(new Map<string, { value: number; session: string }>())
  const playerCommandGeneration = useRef(0)
  const playerSessionRef = useRef('')
  const lastVolume = useRef(100)
  const searchInput = useRef<HTMLInputElement>(null)
  const dialogRef = useRef<HTMLElement>(null)
  const exploreGate = useRef(new RequestGate())
  const authoritativeState = useRef<State>(initialState)
  const pendingFeedback = useRef(new Map<string, { anime: Anime; value: FeedbackValue | '' }>())
  const feedbackQueue = useRef<Promise<void>>(Promise.resolve())
  const metadataCacheRef = useRef(new Map<string, Anime>())
  const metadataInflight = useRef(new Map<string, Promise<Anime>>())
  const fetchedDetails = useRef(new Set<string>())
  const openVersion = useRef(0)
  const trailerToken = useRef(0)
  const trailerRevision = useRef(0)
  const trailerPlayersRef = useRef(new Map<string, TrailerPlayer>())
  const clickTimers = useRef(new Map<string, number>())
  const metadataQueue = useRef<string[]>([])
  const metadataQueued = useRef(new Set<string>())
  const metadataActive = useRef(0)
  const visibleCards = useRef(new Map<string, { anime: Anime; element: HTMLElement; visible: boolean; order: number }>())
  const autoTrailerKey = useRef('')
  const autoSuppressedCards = useRef(new Map<string, string>())
  const autoScheduler = useRef<number | null>(null)
  const autoGate = useRef(new SerializedAsyncGate())
  const autoSuppressed = useRef(false)
  const stopAllGeneration = useRef(0)
  const appMounted = useRef(true)
  const ready = connection === 'connected'
  playerSessionRef.current = state.player.active ? `${playerCommandGeneration.current}:${state.player.renderer}:${state.player.anime?.id ?? ''}:${state.player.episode?.url ?? state.player.episode?.number ?? ''}` : ''
  const trailerRuntime = useRef({ ready: false, trailerPreviewAvailable: false, reducedMotion: false, selected: false, playerActive: false })
  const bridge = () => {
    const app = window.go?.main?.App
    if (!app) throw new Error('Open SERIAL as a desktop app to use this feature.')
    return app
  }

  const acceptState = useCallback((next: State) => {
    const restored = reconcileFeedbackState(next, authoritativeState.current)
    authoritativeState.current = restored
    const feedback = [...pendingFeedback.current.values()].reduce((items, change) => withFeedback(items, change.anime, change.value), restored.feedback)
    setState({ ...restored, feedback })
  }, [])
  const loadExplore = useCallback(async (refresh = false, requestedTags = selectedTagIds) => {
    const app = window.go?.main?.App
    if (!app) return
    const revision = exploreGate.current.begin()
    const filterKey = tagFilterKey(requestedTags)
    setExploring(true); setExploreError('')
    try {
      const result = await app.GetExploreWithTags(requestedTags, refresh)
      if (exploreGate.current.accepts(revision)) { setExplore({ ...result, items: result.items ?? [], warnings: result.warnings ?? [] }); setExploreFilterKey(filterKey) }
    } catch (error) { if (exploreGate.current.accepts(revision)) setExploreError(errorMessage(error)) }
    finally { if (exploreGate.current.accepts(revision)) setExploring(false) }
  }, [selectedTagIds])
  const bootstrap = useCallback(async () => {
    const app = window.go?.main?.App
    if (!app) { setConnection('preview'); return }
    setConnection('loading')
    try { acceptState(await app.Bootstrap()); setConnection('connected'); setConnectionError('') }
    catch (error) { setConnection('failed'); setConnectionError(errorMessage(error)) }
  }, [acceptState])

  trailerRuntime.current = { ready, trailerPreviewAvailable: state.trailerPreviewAvailable, reducedMotion, selected: Boolean(selected), playerActive: state.player.active }

  useEffect(() => {
    void bootstrap()
    const unsubscribe = window.runtime?.EventsOn('app:state', acceptState)
    return () => { unsubscribe?.(); searchVersion.current++; openVersion.current++; episodeVersion.current++; exploreGate.current.invalidate() }
  }, [bootstrap, acceptState])
  useEffect(() => { if (ready) void loadExplore(false, selectedTagIds) }, [ready, selectedTagIds, loadExplore])
  useEffect(() => {
    if (!ready) return
    let cancelled = false
    setTagsLoading(true); setTagsError('')
    bridge().GetAnimeTags().then(items => { if (!cancelled) setTags(items ?? []) }).catch(error => { if (!cancelled) setTagsError(errorMessage(error)) }).finally(() => { if (!cancelled) setTagsLoading(false) })
    return () => { cancelled = true }
  }, [ready])

  useEffect(() => {
    const media = window.matchMedia?.('(prefers-reduced-motion: reduce)')
    if (!media) return
    const update = () => setReducedMotion(media.matches)
    update()
    media.addEventListener?.('change', update)
    return () => media.removeEventListener?.('change', update)
  }, [])
  useEffect(() => {
    const visibility = () => {
      if (document.hidden) pauseAutomaticTrailer()
      scheduleAutoplay()
    }
    document.addEventListener('visibilitychange', visibility)
    return () => document.removeEventListener('visibilitychange', visibility)
  }, [])
  useEffect(() => {
    if (!settingsDirty) setSettingsDraft(state.settings)
  }, [state.settings, settingsDirty])
  useEffect(() => {
    lastVolume.current = positiveVolumeMemory(lastVolume.current, volumeDraft ?? state.player.volume)
    setVolumeDraft(value => acknowledgedNumberDraft(value, state.player.volume))
  }, [state.player.volume, volumeDraft])
  useEffect(() => {
    setSpeedDraft(value => acknowledgedNumberDraft(value, state.player.speed))
  }, [state.player.speed])
  useEffect(() => {
    setSeekDraft(value => acknowledgedSeekDraft(value, state.player.position, state.player.paused, state.player.speed))
  }, [state.player.position, state.player.paused, state.player.speed])
  function invalidatePlayerCommands() {
    playerCommandGeneration.current++
    pendingPlayerCommands.current.clear()
    setSeekDraft(null)
    setVolumeDraft(null)
    setSpeedDraft(null)
  }
  useEffect(() => {
    invalidatePlayerCommands()
  }, [state.player.active, state.player.renderer, state.player.anime?.id, state.player.episode?.url, state.player.episode?.number])
  useEffect(() => { window.scrollTo(0, 0) }, [page])
  useEffect(() => {
    if (!toast) return
    const timer = setTimeout(() => setToast(null), toast.error ? 8000 : 3500)
    return () => clearTimeout(timer)
  }, [toast])
  useEffect(() => {
    function keyboard(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault(); setPage('search')
        requestAnimationFrame(() => searchInput.current?.focus())
      }
      if (event.key === 'Escape') closeAnimeDetails()
    }
    window.addEventListener('keydown', keyboard)
    return () => window.removeEventListener('keydown', keyboard)
  }, [])
  useEffect(() => {
    if (!selected) return
    const previous = document.activeElement as HTMLElement | null
    const panel = dialogRef.current
    panel?.querySelector<HTMLButtonElement>('button')?.focus()
    function trap(event: KeyboardEvent) {
      if (event.key !== 'Tab' || !panel) return
      const elements = [...panel.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]')]
      const first = elements[0], last = elements[elements.length - 1]
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
      if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
    }
    window.addEventListener('keydown', trap)
    return () => { window.removeEventListener('keydown', trap); previous?.focus() }
  }, [selected])
  useEffect(() => { void stopAllTrailers() }, [page, selected, state.player.active])
  useEffect(() => () => { appMounted.current = false; visibleCards.current.clear(); void stopAllTrailers() }, [])
  useEffect(() => {
    function syncAll() {
      trailerPlayersRef.current.forEach(player => syncTrailerPlayer(player.cardKey))
      scheduleAutoplay(0)
    }
    const poll = window.setInterval(() => {
      trailerPlayersRef.current.forEach(player => {
        syncTrailerPlayer(player.cardKey)
        if (!player.token) return
        void window.go?.main?.App?.GetTrailerPreviewStatus(player.token).then((status: TrailerPreviewStatus) => {
          const current = trailerPlayersRef.current.get(player.cardKey)
          if (!current || !shouldApplyTrailerCommand(current.token, current.revision, status.token, player.revision)) return
          if (status.phase === 'stopped') { if (current.mode === 'auto') suppressAutomaticCard(player.cardKey); void stopTrailer(player.cardKey); return }
          if (status.phase === 'unavailable' || status.phase === 'failed') {
            const errorPhase = status.phase as ManualTrailerPhase
            if (current.mode === 'auto') suppressAutomaticCard(player.cardKey)
            if (current.phase !== 'unavailable' && current.phase !== 'failed') void window.go?.main?.App?.PauseTrailerPreview(status.token)
            updateTrailerPlayer(player.cardKey, latest => latest && shouldApplyTrailerCommand(latest.token, latest.revision, status.token, player.revision) ? { ...latest, phase: errorPhase, message: status.message || status.phase, cover: true, commandAt: Date.now(), position: Number.isFinite(status.position) ? status.position : latest.position } : latest)
            return
          }
          if (status.phase === 'paused' && !current.cover && Date.now() - current.commandAt >= 500) {
            if (current.mode === 'auto') suppressAutomaticCard(player.cardKey)
            void window.go?.main?.App?.PauseTrailerPreview(status.token)
            updateTrailerPlayer(player.cardKey, latest => latest && shouldApplyTrailerCommand(latest.token, latest.revision, status.token, player.revision) ? { ...latest, phase: 'paused', message: status.message, cover: true, commandAt: Date.now(), position: Number.isFinite(status.position) ? status.position : latest.position } : latest)
            return
          }
          updateTrailerPlayer(player.cardKey, latest => {
            if (!latest || !shouldApplyTrailerCommand(latest.token, latest.revision, status.token, player.revision)) return latest
            const freshCommand = Date.now() - latest.commandAt < 500
            const phase = freshCommand ? latest.phase : normalizeTrailerPhase(status.phase, latest.phase)
            return { ...latest, phase, message: status.message, position: Number.isFinite(status.position) ? status.position : latest.position }
          })
        }).catch(error => {
          updateTrailerPlayer(player.cardKey, current => current && shouldApplyTrailerCommand(current.token, current.revision, player.token, player.revision) ? { ...current, phase: 'failed', message: errorMessage(error), cover: true, commandAt: Date.now() } : current)
        })
      })
    }, 900)
    window.addEventListener('scroll', syncAll, true)
    window.addEventListener('resize', syncAll)
    return () => { window.clearInterval(poll); window.removeEventListener('scroll', syncAll, true); window.removeEventListener('resize', syncAll) }
  }, [])
  useEffect(() => {
    function nativeClick(event: Event) {
      const detail = (event as CustomEvent<{ token: number; clickCount: number }>).detail
      const token = Number(detail?.token ?? 0)
      const clickCount = Number(detail?.clickCount ?? 1)
      const player = [...trailerPlayersRef.current.values()].find(item => item.token === token)
      if (player) queueTrailerClick(player.cardKey, clickCount)
    }
    window.addEventListener('native-trailer-click', nativeClick)
    return () => window.removeEventListener('native-trailer-click', nativeClick)
  }, [])



  async function operation(key: string, action: () => Promise<void>, success?: string) {
    if (locks.current.has(key)) return
    locks.current.add(key); setBusy([...locks.current])
    try { await action(); if (success) setToast({ message: success, error: false }) }
    catch (error) { setToast({ message: errorMessage(error), error: true }) }
    finally { locks.current.delete(key); setBusy([...locks.current]) }
  }

  function cacheAnime(anime: Anime, detailsFetched = false, aliasKey = ''): Anime {
    const key = animeKey(anime)
    metadataCacheRef.current.set(key, anime)
    if (aliasKey && aliasKey !== key) metadataCacheRef.current.set(aliasKey, anime)
    if (detailsFetched) {
      fetchedDetails.current.add(key)
      if (aliasKey) fetchedDetails.current.add(aliasKey)
    }
    if (metadataCacheRef.current.size > 80) {
      const first = metadataCacheRef.current.keys().next().value
      if (first) { metadataCacheRef.current.delete(first); fetchedDetails.current.delete(first) }
    }
    setMetadataCache(Object.fromEntries(metadataCacheRef.current))
    return anime
  }

  function rememberCardAnime(cardKey: string, anime: Anime): Anime {
    const key = animeKey(anime)
    const remembered = preserveUsableCatalogDetails(metadataCacheRef.current.get(key), anime)
    const preserved = preserveUsableCatalogDetails(metadataCacheRef.current.get(cardKey), remembered)
    metadataCacheRef.current.set(cardKey, preserved)
    metadataCacheRef.current.set(key, preserveUsableCatalogDetails(metadataCacheRef.current.get(key), preserved))
    return preserved
  }

  function enrichAnime(anime: Anime): Anime {
    return metadataCacheRef.current.get(animeKey(anime)) ?? metadataCache[animeKey(anime)] ?? anime
  }

  function ensureDetails(anime: Anime, reportError = false): Promise<Anime> {
    const key = animeKey(anime)
    const cached = metadataCacheRef.current.get(key)
    if (cached && (fetchedDetails.current.has(key) || hasUsableCatalogDetails(cached))) return Promise.resolve(cached)
    if (!cached && hasUsableCatalogDetails(anime)) return Promise.resolve(cacheAnime(anime))
    const pending = metadataInflight.current.get(key)
    if (pending) return pending
    const app = window.go?.main?.App
    if (!app) return Promise.resolve(anime)
    const request = app.GetAnimeDetails(anime).then(details => cacheAnime(details, true, key)).catch(error => {
      if (reportError) setToast({ message: errorMessage(error), error: true })
      if (hasUsableCatalogDetails(anime)) cacheAnime(anime)
      return anime
    }).finally(() => metadataInflight.current.delete(key))
    metadataInflight.current.set(key, request)
    return request
  }

  function syncTrailerState() {
    setTrailerPlayers(Object.fromEntries(trailerPlayersRef.current))
    scheduleAutoplay()
  }

  function updateTrailerPlayer(cardKey: string, update: (current: TrailerPlayer | undefined) => TrailerPlayer | undefined) {
    const next = update(trailerPlayersRef.current.get(cardKey))
    if (next) trailerPlayersRef.current.set(cardKey, next)
    else trailerPlayersRef.current.delete(cardKey)
    syncTrailerState()
  }

  function normalizeTrailerPhase(phase: string, fallback: ManualTrailerPhase): ManualTrailerPhase {
    return ['starting', 'playing', 'paused', 'hidden', 'unavailable', 'failed'].includes(phase) ? phase as ManualTrailerPhase : fallback
  }

  function previewBounds(element: HTMLElement) {
    const rect = element.getBoundingClientRect()
    const topbar = document.querySelector('.topbar')?.getBoundingClientRect()
    const player = document.querySelector('.player-bar')?.getBoundingClientRect()
    const viewportTop = Math.max(0, topbar?.bottom ?? 0)
    const viewportBottom = Math.min(window.innerHeight, player?.top ?? window.innerHeight)
    return trailerPreviewBounds({ x: rect.left, y: rect.top, width: rect.width, height: rect.height }, { x: 0, y: viewportTop, width: window.innerWidth, height: Math.max(0, viewportBottom - viewportTop) })
  }

  function autoplayDisabled() {
    const runtime = trailerRuntime.current
    return !appMounted.current || autoSuppressed.current || !runtime.ready || !runtime.trailerPreviewAvailable || runtime.reducedMotion || document.hidden || runtime.selected || runtime.playerActive
  }

  function manualTrailerActive() {
    return hasActiveManualTrailer([...trailerPlayersRef.current.values()])
  }

  function cardTrailer(anime: Anime): string {
    return (metadataCacheRef.current.get(animeKey(anime)) ?? anime).trailer?.youtubeId ?? ''
  }

  function trailerSuppressionKey(cardKey: string, anime: Anime): string {
    return `${cardKey}:${cardTrailer(anime) || 'missing'}`
  }

  function suppressAutomaticCard(cardKey: string) {
    const card = visibleCards.current.get(cardKey)
    if (card) autoSuppressedCards.current.set(cardKey, trailerSuppressionKey(cardKey, card.anime))
  }

  function clearAutomaticSuppression(cardKey: string) {
    autoSuppressedCards.current.delete(cardKey)
  }

  function automaticCardSuppressed(cardKey: string, anime: Anime): boolean {
    return autoSuppressedCards.current.get(cardKey) === trailerSuppressionKey(cardKey, anime)
  }

  function scheduleAutoplay(delay = 140) {
    if (!appMounted.current || autoSuppressed.current) return
    if (autoScheduler.current) window.clearTimeout(autoScheduler.current)
    autoScheduler.current = window.setTimeout(() => {
      autoScheduler.current = null
      const request = autoGate.current.begin()
      if (!request.run) return
      void runAutoplayScheduler(request.generation).finally(() => {
        const queued = autoGate.current.finish()
        if (queued && appMounted.current && !autoSuppressed.current) scheduleAutoplay(0)
      })
    }, delay)
  }

  function validAutoplayRun(generation: number): boolean {
    return appMounted.current && !autoSuppressed.current && autoGate.current.accepts(generation) && !autoplayDisabled()
  }

  async function pauseAutomaticTrailer() {
    const key = autoTrailerKey.current
    if (!key) return
    const player = trailerPlayersRef.current.get(key)
    if (!player || player.mode !== 'auto') { autoTrailerKey.current = ''; return }
    await stopTrailer(key)
  }

  async function runAutoplayScheduler(generation: number) {
    if (!validAutoplayRun(generation)) { await pauseAutomaticTrailer(); return }
    if (manualTrailerActive()) { await pauseAutomaticTrailer(); return }
    if (!validAutoplayRun(generation) || manualTrailerActive()) return
    const candidates = [...visibleCards.current.entries()].flatMap(([cardKey, card]) => {
      if (!card.element.isConnected) { visibleCards.current.delete(cardKey); return [] }
      const player = trailerPlayersRef.current.get(cardKey)
      const blocked = Boolean(player && (player.mode === 'manual' || player.viewerPaused || player.phase === 'paused' || player.phase === 'hidden' || player.phase === 'unavailable' || player.phase === 'failed'))
      return [{ cardKey, order: card.order, visible: card.visible && Boolean(previewBounds(card.element)), hasTrailer: Boolean(cardTrailer(card.anime)), blocked, suppressed: automaticCardSuppressed(cardKey, card.anime) }]
    })
    const nextKey = chooseTrailerAutoplayCandidate(candidates, autoTrailerKey.current)
    if (!nextKey) { await pauseAutomaticTrailer(); return }
    if (!validAutoplayRun(generation) || manualTrailerActive()) return
    const activeAuto = autoTrailerKey.current ? trailerPlayersRef.current.get(autoTrailerKey.current) : undefined
    if (nextKey === autoTrailerKey.current && activeAuto && !activeAuto.cover && (activeAuto.phase === 'starting' || activeAuto.phase === 'playing')) return
    if (autoTrailerKey.current && nextKey !== autoTrailerKey.current) {
      await pauseAutomaticTrailer()
      if (!validAutoplayRun(generation) || manualTrailerActive()) return
    }
    const card = visibleCards.current.get(nextKey)
    if (!card || !card.element.isConnected || !card.visible || !previewBounds(card.element)) return
    if (!trailerPlayersRef.current.has(nextKey)) {
      trailerPlayersRef.current.set(nextKey, { token: 0, cardKey: nextKey, youtubeId: '', element: card.element, phase: 'idle', message: '', position: 0, cover: true, visible: true, commandAt: 0, revision: ++trailerRevision.current, mode: 'auto', viewerPaused: false })
      syncTrailerState()
      if (!validAutoplayRun(generation) || manualTrailerActive()) return
    } else {
      updateTrailerPlayer(nextKey, player => player ? { ...player, element: card.element, mode: 'auto' as TrailerPlaybackMode, viewerPaused: false } : player)
      if (!validAutoplayRun(generation) || manualTrailerActive()) return
    }
    await startTrailer(nextKey, 'auto', generation)
  }

  function handleCardVisibility(anime: Anime, cardKey: string, element: HTMLElement, visible: boolean, order: number) {
    const remembered = rememberCardAnime(cardKey, anime)
    visibleCards.current.set(cardKey, { anime: remembered, element, visible, order })
    if (visible) queueVisibleMetadata(remembered, cardKey)
    scheduleAutoplay()
  }


  function unregisterCard(cardKey: string) {
    visibleCards.current.delete(cardKey)
    clearAutomaticSuppression(cardKey)
    scheduleAutoplay()
  }

  function syncTrailerPlayer(cardKey: string) {
    const player = trailerPlayersRef.current.get(cardKey)
    if (!player || !player.token) return
    const bounds = previewBounds(player.element)
    if (!bounds) {
      if (player.mode === 'auto') { suppressAutomaticCard(cardKey); void stopTrailer(cardKey); return }
      if (player.phase === 'playing' || player.phase === 'starting') void pauseTrailer(cardKey, true)
      else updateTrailerPlayer(cardKey, current => current ? { ...current, visible: false, cover: true, phase: current.phase === 'playing' ? 'hidden' : current.phase } : current)
      return
    }
    if (!player.visible) updateTrailerPlayer(cardKey, current => current ? { ...current, visible: true, cover: true, phase: current.phase === 'hidden' ? 'paused' : current.phase } : current)
    const current = trailerPlayersRef.current.get(cardKey)
    if (current && current.visible && !current.cover && (current.phase === 'playing' || current.phase === 'starting')) {
      void window.go?.main?.App?.MoveTrailerPreview(current.token, bounds.x, bounds.y, bounds.width, bounds.height)
    }
  }

  function queueVisibleMetadata(anime: Anime, cardKey: string) {
    if (!ready || metadataQueued.current.has(cardKey) || fetchedDetails.current.has(animeKey(anime)) || hasUsableCatalogDetails(enrichAnime(anime))) return
    rememberCardAnime(cardKey, anime)
    metadataQueued.current.add(cardKey)
    metadataQueue.current.push(cardKey)
    void pumpMetadataQueue()
  }

  async function pumpMetadataQueue(seed = new Map<string, Anime>()) {
    while (metadataActive.current < 2 && metadataQueue.current.length > 0) {
      const cardKey = metadataQueue.current.shift()!
      const anime = seed.get(cardKey) ?? metadataCacheRef.current.get(cardKey)
      if (!anime) { metadataQueued.current.delete(cardKey); continue }
      metadataActive.current++
      ensureDetails(anime).finally(() => {
        metadataActive.current--
        metadataQueued.current.delete(cardKey)
        scheduleAutoplay()
        void pumpMetadataQueue()
      })
    }
  }

  function queueTrailerClick(cardKey: string, clickCount: number) {
    const existing = clickTimers.current.get(cardKey)
    if (existing) window.clearTimeout(existing)
    if (clickCount > 1) { void runTrailerGesture(cardKey, clickCount); return }
    clickTimers.current.set(cardKey, window.setTimeout(() => {
      clickTimers.current.delete(cardKey)
      void runTrailerGesture(cardKey, 1)
    }, 500))
  }

  function handleTrailerClick(anime: Anime, cardKey: string, element: HTMLElement, clickCount: number) {
    autoGate.current.invalidate()
    clearAutomaticSuppression(cardKey)
    const activeAutoKey = autoTrailerKey.current
    if (activeAutoKey && activeAutoKey !== cardKey) void stopTrailer(activeAutoKey)
    const current = trailerPlayersRef.current.get(cardKey)
    if (current) {
      current.element = element
      current.mode = 'manual'
      current.viewerPaused = false
      if (!current.token) current.phase = 'starting'
    } else {
      trailerPlayersRef.current.set(cardKey, { token: 0, cardKey, youtubeId: '', element, phase: 'starting', message: '', position: 0, cover: true, visible: true, commandAt: Date.now(), revision: ++trailerRevision.current, mode: 'manual', viewerPaused: false })
    }
    if (autoTrailerKey.current === cardKey) autoTrailerKey.current = ''
    rememberCardAnime(cardKey, anime)
    syncTrailerState()
    queueTrailerClick(cardKey, clickCount)
  }

  async function runTrailerGesture(cardKey: string, clickCount: number) {
    const player = trailerPlayersRef.current.get(cardKey)
    if (!player) return
    const action = !player.token && player.phase === 'starting' ? 'start' : manualTrailerGesture(player.phase, clickCount)
    if (action === 'pause') return pauseTrailer(cardKey, false, '', true)
    if (action === 'resume') return resumeTrailer(cardKey, 'manual')
    if (action === 'restart') return restartTrailer(cardKey, 'manual')
    return startTrailer(cardKey, 'manual')
  }

  async function startTrailer(cardKey: string, mode: TrailerPlaybackMode = 'manual', autoplayGeneration = 0) {
    const placeholder = trailerPlayersRef.current.get(cardKey)
    if (!placeholder) return
    if (mode === 'auto' && (!validAutoplayRun(autoplayGeneration) || manualTrailerActive())) return
    const baseAnime = metadataCacheRef.current.get(cardKey)
    if (!baseAnime) return
    const requestAt = Date.now()
    if (mode === 'auto') autoTrailerKey.current = cardKey
    const requestRevision = ++trailerRevision.current
    updateTrailerPlayer(cardKey, player => player ? { ...player, mode, viewerPaused: false, phase: 'starting', message: mode === 'auto' ? '' : 'Loading trailer', cover: mode === 'manual', commandAt: requestAt, revision: requestRevision } : player)
    const currentAnime = await ensureDetails(baseAnime)
    if (mode === 'auto' && (!validAutoplayRun(autoplayGeneration) || manualTrailerActive())) return
    const current = trailerPlayersRef.current.get(cardKey)
    if (!current || current.revision !== requestRevision || current.mode !== mode) return
    if (mode === 'auto' && (!validAutoplayRun(autoplayGeneration) || manualTrailerActive())) return
    if (!trailerRuntime.current.trailerPreviewAvailable) {
      if (mode === 'manual') openTrailerFallback(currentAnime)
      if (mode === 'auto') suppressAutomaticCard(cardKey)
      updateTrailerPlayer(cardKey, player => player && player.revision === requestRevision ? { ...player, phase: 'unavailable', message: mode === 'manual' ? 'Opened on YouTube' : 'Trailer preview unavailable', cover: true, commandAt: Date.now() } : player)
      return
    }
    const youtubeId = currentAnime.trailer?.youtubeId
    const bounds = previewBounds(current.element)
    if (!youtubeId) {
      if (mode === 'auto') suppressAutomaticCard(cardKey)
      updateTrailerPlayer(cardKey, player => player && player.revision === requestRevision ? { ...player, phase: 'unavailable', message: 'Trailer unavailable', cover: true, commandAt: Date.now() } : player)
      return
    }
    if (!bounds) {
      updateTrailerPlayer(cardKey, player => player && player.revision === requestRevision ? { ...player, youtubeId, phase: 'paused', message: mode === 'manual' ? 'Scroll this card fully into view to play.' : '', cover: true, visible: false, commandAt: Date.now() } : player)
      return
    }
    if (mode === 'auto') {
      if (autoTrailerKey.current && autoTrailerKey.current !== cardKey) {
        await pauseAutomaticTrailer()
        if (!validAutoplayRun(autoplayGeneration) || manualTrailerActive()) return
      }
      autoTrailerKey.current = cardKey
    }
    const token = nextTrailerToken(trailerToken.current)
    trailerToken.current = token
    const next = { ...current, token, youtubeId, mode, viewerPaused: false, phase: 'starting' as ManualTrailerPhase, message: mode === 'manual' ? 'Loading trailer' : '', position: 0, cover: false, visible: true, commandAt: Date.now(), revision: requestRevision }
    trailerPlayersRef.current.set(cardKey, next)
    syncTrailerState()
    try {
      await bridge().StartTrailerPreview(youtubeId, bounds.x, bounds.y, bounds.width, bounds.height, token)
      const latest = trailerPlayersRef.current.get(cardKey)
      if (!latest || !shouldApplyTrailerCommand(latest.token, latest.revision, token, requestRevision) || latest.mode !== mode || (mode === 'auto' && (!validAutoplayRun(autoplayGeneration) || manualTrailerActive()))) await bridge().StopTrailerPreview(token)
    } catch (error) {
      if (mode === 'auto') suppressAutomaticCard(cardKey)
      updateTrailerPlayer(cardKey, player => player && shouldApplyTrailerCommand(player.token, player.revision, token, requestRevision) ? { ...player, phase: 'failed', message: errorMessage(error), cover: true, commandAt: Date.now() } : player)
    }
  }

  async function pauseTrailer(cardKey: string, hidden = false, message = '', viewerPaused = false) {
    const player = trailerPlayersRef.current.get(cardKey)
    if (!player) return
    const revision = ++trailerRevision.current
    const token = player.token
    updateTrailerPlayer(cardKey, current => current ? { ...current, phase: hidden ? 'hidden' : 'paused', message: message || current.message, cover: true, visible: !hidden, commandAt: Date.now(), revision, viewerPaused: current.viewerPaused || viewerPaused } : current)
    if (autoTrailerKey.current === cardKey && viewerPaused) autoTrailerKey.current = ''
    if (!token) return
    try { await bridge().PauseTrailerPreview(token) }
    catch (error) { updateTrailerPlayer(cardKey, current => current && shouldApplyTrailerCommand(current.token, current.revision, token, revision) ? { ...current, phase: 'failed', cover: true, message: errorMessage(error), commandAt: Date.now() } : current) }
  }

  async function resumeTrailer(cardKey: string, mode: TrailerPlaybackMode = 'manual') {
    const player = trailerPlayersRef.current.get(cardKey)
    if (!player) return
    const bounds = previewBounds(player.element)
    if (!bounds) { await pauseTrailer(cardKey, true); return }
    if (!player.token) return startTrailer(cardKey, mode)
    const revision = ++trailerRevision.current
    const token = player.token
    updateTrailerPlayer(cardKey, current => current ? { ...current, mode, viewerPaused: false, phase: 'playing', cover: false, visible: true, message: '', commandAt: Date.now(), revision } : current)
    try {
      await bridge().MoveTrailerPreview(token, bounds.x, bounds.y, bounds.width, bounds.height)
      await bridge().ResumeTrailerPreview(token)
    } catch (error) {
      updateTrailerPlayer(cardKey, current => current && shouldApplyTrailerCommand(current.token, current.revision, token, revision) ? { ...current, phase: 'failed', cover: true, message: errorMessage(error), commandAt: Date.now() } : current)
    }
  }

  async function restartTrailer(cardKey: string, mode: TrailerPlaybackMode = 'manual') {
    const player = trailerPlayersRef.current.get(cardKey)
    if (!player || !player.token) return startTrailer(cardKey, mode)
    const bounds = previewBounds(player.element)
    if (!bounds) { await pauseTrailer(cardKey, true); return }
    const revision = ++trailerRevision.current
    const token = player.token
    updateTrailerPlayer(cardKey, current => current ? { ...current, mode, viewerPaused: false, phase: 'playing', cover: false, visible: true, position: 0, message: '', commandAt: Date.now(), revision } : current)
    try {
      await bridge().MoveTrailerPreview(token, bounds.x, bounds.y, bounds.width, bounds.height)
      await bridge().RestartTrailerPreview(token)
    } catch (error) {
      updateTrailerPlayer(cardKey, current => current && shouldApplyTrailerCommand(current.token, current.revision, token, revision) ? { ...current, phase: 'failed', cover: true, message: errorMessage(error), commandAt: Date.now() } : current)
    }
  }

  async function stopTrailer(cardKey: string) {
    autoGate.current.invalidate()
    const player = trailerPlayersRef.current.get(cardKey)
    const timer = clickTimers.current.get(cardKey)
    if (timer) window.clearTimeout(timer)
    clickTimers.current.delete(cardKey)
    trailerPlayersRef.current.delete(cardKey)
    if (autoTrailerKey.current === cardKey) autoTrailerKey.current = ''
    syncTrailerState()
    if (player?.token) { try { await bridge().StopTrailerPreview(player.token) } catch {} }
  }

  async function stopAllTrailers() {
    const stopGeneration = ++stopAllGeneration.current
    autoSuppressed.current = true
    autoGate.current.invalidate()
    if (autoScheduler.current) window.clearTimeout(autoScheduler.current); autoScheduler.current = null
    autoTrailerKey.current = ''
    clickTimers.current.forEach(timer => window.clearTimeout(timer)); clickTimers.current.clear()
    metadataQueue.current = []
    metadataQueued.current.clear()
    autoSuppressedCards.current.clear()
    const tokens = [...trailerPlayersRef.current.values()].map(player => player.token).filter(Boolean)
    trailerPlayersRef.current.clear()
    setTrailerPlayers({})
    await Promise.allSettled(tokens.map(token => window.go?.main?.App?.StopTrailerPreview(token)))
    try { await window.go?.main?.App?.StopTrailerPreview(0) } catch {}
    if (stopGeneration !== stopAllGeneration.current) return
    autoSuppressed.current = false
    autoGate.current.invalidate()
    if (appMounted.current) scheduleAutoplay(0)
  }

  function openTrailerFallback(anime: Anime) {
    const youtubeId = (metadataCacheRef.current.get(animeKey(anime)) ?? anime).trailer?.youtubeId
    if (!youtubeId) return
    void operation(`trailer:${youtubeId}`, async () => bridge().OpenTrailer(youtubeId))
  }

  function closeAnimeDetails() {
    openVersion.current++
    episodeVersion.current++
    setSelected(null)
    setEpisodesLoading(false)
  }

  async function search(event?: FormEvent) {
    event?.preventDefault()
    const term = query.trim()
    if (!term || !ready) return
    const version = ++searchVersion.current
    setSearching(true); setSearchError(''); setSearchWarnings([]); setResults([]); setSearched(term)
    try {
      const found = await bridge().SearchWithStatus(term, source)
      if (version === searchVersion.current) {
        setResults(found.animes ?? [])
        setSearchWarnings(found.warnings ?? [])
      }
    } catch (error) { if (version === searchVersion.current) setSearchError(errorMessage(error)) }
    finally { if (version === searchVersion.current) setSearching(false) }
  }

  async function openAnime(anime: Anime) {
    void stopAllTrailers()
    const version = ++openVersion.current
    episodeVersion.current++
    setSelected(anime); setEpisodes([]); setEpisodeFilter(''); setEpisodesError(''); setEpisodesLoading(true)
    try {
      const detailsPromise = ensureDetails(anime, true)
      if (anime.source === 'mal') {
        const withDetails = await detailsPromise
        if (version !== openVersion.current) return
        setSelected(withDetails)
        const playable = await bridge().ResolveAnime(withDetails)
        if (version !== openVersion.current) return
        const merged = cacheAnime({ ...withDetails, ...playable, tags: playable.tags ?? withDetails.tags, trailer: playable.trailer ?? withDetails.trailer, metadataSource: playable.metadataSource || withDetails.metadataSource, alternateTitles: playable.alternateTitles ?? withDetails.alternateTitles })
        setSelected(merged)
        const found = await bridge().GetEpisodes(merged)
        if (version === openVersion.current) setEpisodes(found ?? [])
      } else {
        detailsPromise.then(withDetails => { if (version === openVersion.current) setSelected(previous => previous && sameAnime(previous, anime) ? withDetails : previous) })
        const found = await bridge().GetEpisodes(anime)
        if (version === openVersion.current) setEpisodes(found ?? [])
      }
    } catch (error) { if (version === openVersion.current) setEpisodesError(errorMessage(error)) }
    finally { if (version === openVersion.current) setEpisodesLoading(false) }
  }

  const isSaved = (anime: Anime) => state.saved.some(item => sameAnime(item, anime))
  const saveAnime = (anime: Anime) => operation(`save:${anime.id}`, async () => acceptState(await bridge().SaveAnime(anime, !isSaved(anime))), isSaved(anime) ? 'Removed from your library' : 'Saved to your library')
  function changeFeedback(anime: Anime, action: FeedbackValue) {
    const key = animeKey(anime)
    const lock = `feedback:${key}`
    if (!ready || locks.current.has(lock)) return
    const value = feedbackFor(state.feedback, anime) === action ? '' : action
    locks.current.add(lock); setBusy([...locks.current])
    pendingFeedback.current.set(key, { anime, value })
    exploreGate.current.invalidate(); setExploring(true)
    acceptState(authoritativeState.current)
    // Persist in click order so overlapping feedback cannot restore an older state.
    feedbackQueue.current = feedbackQueue.current.then(async () => {
      try {
        const next = await bridge().SetFeedback(anime, value)
        pendingFeedback.current.delete(key)
        acceptState(next)
      } catch (error) {
        pendingFeedback.current.delete(key)
        acceptState(authoritativeState.current)
        setToast({ message: errorMessage(error), error: true })
      } finally {
        locks.current.delete(lock); setBusy([...locks.current])
        if (pendingFeedback.current.size === 0) void loadExplore()
      }
    })
  }
  const play = (anime: Anime, episode: Episode, position = 0) => operation('play', async () => { invalidatePlayerCommands(); await stopAllTrailers(); await bridge().Play(anime, episode, position); closeAnimeDetails() })
  const download = (anime: Anime, episode: Episode) => operation(`download:${anime.id}:${episode.number}`, async () => { await bridge().DownloadEpisode(anime, episode) }, 'Episode added to downloads')
  const playerSessionKey = () => playerSessionRef.current
  const seekRelative = (delta: number) => relativeSeekPosition(delta, state.player.position, state.player.duration, seekDraft)
  const command = (name: string, value = 0) => {
    if (name === 'stop') {
      invalidatePlayerCommands()
      return operation('player:stop', async () => bridge().PlayerCommand('stop', value))
    }
    if (name !== 'seek' && name !== 'volume' && name !== 'speed') return operation(`player:${name}`, async () => bridge().PlayerCommand(name, value))
    const session = playerSessionKey()
    if (!session) return
    pendingPlayerCommands.current.set(name, { value, session })
    if (locks.current.has(`player:${name}`)) return
    return operation(`player:${name}`, async () => {
      try {
        while (pendingPlayerCommands.current.has(name)) {
          const current = nextQueuedPlayerCommand(pendingPlayerCommands.current, name, playerSessionKey())
          if (current === null) continue
          await bridge().PlayerCommand(name, current)
        }
      } catch (error) {
        if (playerSessionKey() === session) {
          if (name === 'seek') setSeekDraft(null)
          else if (name === 'volume') setVolumeDraft(null)
          else setSpeedDraft(null)
        }
        throw error
      }
    })
  }
  const saveSettings = (settings: Settings) => operation('settings', async () => {
    acceptState(await bridge().SaveSettings(settings)); setSettingsDirty(false)
  }, 'Preferences saved')
  const pickPath = (kind: 'downloadDir' | 'mpvPath') => operation(`pick:${kind}`, async () => {
    const path = await (kind === 'downloadDir' ? bridge().ChooseDownloadDirectory() : bridge().ChooseMPV())
    if (path) { setSettingsDraft(previous => ({ ...previous, [kind]: path })); setSettingsDirty(true) }
  })
  const goSearch = () => { setPage('search'); requestAnimationFrame(() => searchInput.current?.focus()) }
  const visibleResults = results.filter(anime => language === 'all' || anime.language === language)
  const exploreItems = unreviewedExplore(explore.items ?? [], state)
  const libraryItems = libraryView === 'saved' ? state.saved : state.feedback.filter(item => item.value === libraryView).map(item => item.anime)
  const activeDownloads = state.downloads.filter(item => ['queued', 'resolving', 'downloading'].includes(item.status)).length
  const playbackAvailable = currentRendererAvailable(state)
  const currentRendererLabel = state.settings.upscaler === 'metalfx' ? 'MetalFX' : 'mpv'
  const playerRendererLabel = rendererLabel(state.player.renderer)
  const playerResolution = playbackResolutionLabel(state.player)

  function renderCards(anime: Anime[], reasons?: Map<string, string>) {
    return <div className="anime-grid">{anime.map((item, index) => {
      const cardKey = animeKey(item)
      const enriched = enrichAnime(item)
      const activeTrailer = trailerPlayers[cardKey]
      return <AnimeCard key={cardKey} cardKey={cardKey} order={index} anime={enriched} trailer={activeTrailer} onOpen={() => void openAnime(enriched)} onSave={() => void saveAnime(enriched)} onFeedback={value => changeFeedback(enriched, value)} onVisible={queueVisibleMetadata} onVisibility={handleCardVisibility} onUnmountCard={unregisterCard} onTrailerClick={handleTrailerClick} onStopTrailer={() => void stopTrailer(cardKey)} onOpenTrailerFallback={openTrailerFallback} saved={isSaved(enriched)} value={feedbackFor(state.feedback, enriched)} reason={reasons?.get(item.id)} disabled={!ready || busy.includes(`save:${enriched.id}`)} feedbackBusy={!ready || busy.includes(`feedback:${animeKey(enriched)}`)} />
    })}</div>
  }

  function renderExplore(preview = false) {
    const items = preview ? exploreItems.slice(0, 5) : exploreItems
    const selectedKey = tagFilterKey(selectedTagIds)
    const filtered = selectedKey && selectedKey === exploreFilterKey
    return <>
      {!preview && <div className="tag-filter-bar" aria-label="Filter Explore by tags">{(showAllFilterTags ? tags : tags.slice(0, 18)).map(tag => <button key={tag.id} aria-pressed={selectedTagIds.includes(tag.id)} disabled={!ready || exploring || (!selectedTagIds.includes(tag.id) && selectedTagIds.length >= 3)} onClick={() => setSelectedTagIds(value => toggleTagId(value, tag.id))}>{tag.name}</button>)}{!showAllFilterTags && tags.length > 18 && <button type="button" className="tag-filter-disclosure" disabled={!ready || exploring} onClick={() => setShowAllFilterTags(true)}>More tags +{tags.length - 18}</button>}{showAllFilterTags && tags.length > 18 && <button type="button" className="tag-filter-disclosure" disabled={!ready || exploring} onClick={() => setShowAllFilterTags(false)}>Show less</button>}{selectedTagIds.length > 0 && <Button variant="ghost" onClick={() => setSelectedTagIds([])}>Clear</Button>}{tagsLoading && <span className="tag-filter-status"><span className="spinner small" />Loading tags</span>}{tagsError && <span className="tag-filter-error">{tagsError}</span>}</div>}
      <div className="feed-status" role="status" aria-live="polite"><p>{exploring && items.length ? 'Updating your recommendations…' : filtered ? explore.message || 'Filtered by selected tags.' : selectedKey && exploreError ? 'Showing previous recommendations; the new tags could not load.' : explore.personalized ? explore.message || 'Recommendations shaped by your likes.' : 'Like a few anime to make these recommendations yours.'}</p>{exploring && items.length > 0 && <span className="spinner small" />}</div>
      {exploreError && <div className="feed-notice" role="alert"><p>{items.length ? 'Showing your last recommendations. ' : ''}{exploreError}</p><Button icon="refresh" variant="ghost" disabled={!ready || exploring} onClick={() => void loadExplore(true)}>Retry</Button></div>}
      {(explore.warnings ?? []).length > 0 && <details className="feed-warnings"><summary>Recommendation source update</summary>{explore.warnings?.map((warning, index) => <p key={index}>{warning}</p>)}</details>}
      {items.length > 0 ? renderCards(items.map(item => item.anime), new Map(items.map(item => [item.anime.id, item.reason]))) : exploring ? <div className="search-loading" role="status"><span className="spinner" /><p>Finding anime…</p></div> : <EmptyState icon="explore" title={!ready ? 'Explore in the desktop app' : exploreError ? 'Recommendations are unavailable' : 'No recommendations yet'} action={<Button icon="search" onClick={goSearch}>Search anime</Button>}>{!ready ? 'Open SERIAL to see real recommendations and save your feedback.' : state.feedback.some(item => item.value === 'dislike') ? 'Search for something you enjoy, or undo a dislike in Library.' : 'Browse a title in Search while the recommendation source is unavailable.'}</EmptyState>}
    </>
  }

  function renderHistory(history: HistoryEntry) {
    const title = actualEpisodeTitle(history.episode)
    return <article className="history-card" key={history.anime.id}>
      <button className="history-cover" onClick={() => void openAnime(history.anime)} aria-label={`View ${history.anime.title}`}><Poster anime={history.anime} compact /></button>
      <div className="history-information"><span className="eyebrow">{episodeLabel(history.episode).toUpperCase()}</span><h3><button className="text-button" onClick={() => void openAnime(history.anime)}>{history.anime.title}</button></h3>{title && <p className="episode-name">{title}</p>}<p>{history.duration ? `${formatTime(history.position)} of ${formatTime(history.duration)}` : `Paused at ${formatTime(history.position)}`}</p><div className="progress-track"><div style={{ width: `${percentage(history.position, history.duration)}%` }} /></div></div>
      <Button icon="play" variant="primary" disabled={!ready || !playbackAvailable || busy.includes('play') || busy.includes('settings')} onClick={() => void play(history.anime, history.episode, history.position)}>Resume</Button>
      <Button icon="close" variant="icon" aria-label={`Remove ${history.anime.title} from watching history`} title="Remove from history" disabled={busy.includes(`history:${history.anime.id}`)} onClick={() => void operation(`history:${history.anime.id}`, async () => acceptState(await bridge().RemoveHistory(history.anime.id)))} />
    </article>
  }

  function renderDownload(item: Download) {
    const active = ['queued', 'resolving', 'downloading'].includes(item.status)
    const status: Record<string, string> = { queued: 'Queued', resolving: 'Finding stream', downloading: 'Downloading', completed: 'Completed', cancelled: 'Cancelled', failed: 'Failed' }
    const title = actualEpisodeTitle(item.episode)
    return <article className="download-row" key={item.id}>
      <Poster anime={item.anime} compact />
      <div className="download-information"><h3>{item.anime.title}</h3><p>{episodeLabel(item.episode)}{title && ` · ${title}`}</p><div className="download-status"><span className={`status-tag status-${item.status}`}>{status[item.status] ?? item.status}</span>{active && <span>{item.status === 'downloading' ? `${Math.round(item.progress)}%` : 'Waiting for source'}{item.bytes > 0 && ` · ${formatBytes(item.bytes)}${item.totalBytes > 0 ? ` / ${formatBytes(item.totalBytes)}` : ''}`}</span>}</div>{active && <div className="progress-track"><div style={{ width: `${Math.min(100, Math.max(0, item.progress))}%` }} /></div>}{item.error && <p className="inline-error">{item.error}</p>}</div>
      <div className="download-actions">{active && <Button icon="close" disabled={!ready || busy.includes(`cancel:${item.id}`)} onClick={() => void operation(`cancel:${item.id}`, async () => bridge().CancelDownload(item.id))}>Cancel</Button>}{['failed', 'cancelled'].includes(item.status) && <Button icon="refresh" disabled={!ready || busy.includes(`retry:${item.id}`)} onClick={() => void operation(`retry:${item.id}`, async () => bridge().RetryDownload(item.id))}>Retry</Button>}{item.status === 'completed' && <Button icon="play" disabled={!ready || !playbackAvailable || busy.includes(`open:${item.id}`)} onClick={() => void operation(`open:${item.id}`, async () => bridge().OpenDownload(item.id))}>Play file</Button>}</div>
    </article>
  }

  return <div className="app-shell">
    <aside className="sidebar" aria-label="Main navigation">
      <a className="brand" href="#home" onClick={event => { event.preventDefault(); setPage('home') }}><span className="brand-mark"><Icon name="play" size={19} /></span><span>SERIAL</span></a>
      <nav>{navigation.map(item => <button key={item.id} className={`nav-item ${page === item.id ? 'active' : ''}`} aria-label={item.label} aria-current={page === item.id ? 'page' : undefined} onClick={() => setPage(item.id)}><Icon name={item.icon} size={18} /><span>{item.label}</span>{item.id === 'downloads' && activeDownloads > 0 && <span className="nav-count">{activeDownloads}</span>}</button>)}</nav>
      <div className="sidebar-bottom"><button className={`nav-item ${page === 'settings' ? 'active' : ''}`} aria-label="Settings" aria-current={page === 'settings' ? 'page' : undefined} onClick={() => setPage('settings')}><Icon name="settings" size={18} /><span>Settings</span></button><div className="sidebar-note"><span className={`connection-dot ${ready ? 'online' : ''}`} /><span>{ready ? 'On this device' : connection === 'loading' ? 'Connecting…' : connection === 'failed' ? 'Disconnected' : 'Browser preview'}</span></div><span className="version">0.5.3</span></div>
    </aside>
    <div className="workspace">
      <header className="topbar"><span>{page === 'settings' ? 'Settings' : navigation.find(item => item.id === page)?.label}</span><button className="topbar-search" aria-label="Search anime, Command or Control K" onClick={goSearch}><Icon name="search" size={15} /><span>Search anime</span><kbd>⌘ K</kbd></button></header>
      <main id="main-content">
        {connection === 'preview' && <div className="connection-banner" role="status"><Icon name="external" size={17} /><p><strong>You’re viewing the browser preview.</strong> Explore, feedback, search and playback are available in the desktop app.</p></div>}
        {connection === 'failed' && <div className="connection-banner error" role="alert"><Icon name="alert" size={17} /><p><strong>Couldn’t connect to SERIAL.</strong> {connectionError}</p><Button icon="refresh" onClick={() => void bootstrap()}>Reconnect</Button></div>}
        {page === 'home' && <>
          <div className="page-heading"><div><h1>Home</h1><p>Continue a series or find something new.</p></div></div>
          <section className="content-section first-section"><div className="section-heading"><h2>Continue watching</h2>{state.history.length > 3 && <Button variant="ghost" onClick={() => setShowAllHistory(value => !value)}>{showAllHistory ? 'Show less' : `View all (${state.history.length})`}</Button>}</div>{state.history.length > 0 ? <div className="history-list">{(showAllHistory ? state.history : state.history.slice(0, 3)).map(renderHistory)}</div> : <div className="history-empty"><Icon name="play" size={20} /><div><h3>Nothing in progress</h3><p>Your watching progress will appear here.</p></div><Button variant="ghost" onClick={goSearch}>Search anime<Icon name="arrow" size={15} /></Button></div>}</section>
          <section className="content-section"><div className="section-heading"><h2>{explore.personalized ? 'For you' : 'Explore'}</h2><Button variant="ghost" onClick={() => setPage('explore')}>View all<Icon name="arrow" size={15} /></Button></div>{renderExplore(true)}</section>
        </>}
        {page === 'explore' && <>
          <div className="page-heading"><div><h1>Explore</h1><p>Find your next series. Like what you enjoy; dislike what you want to skip.</p></div><Button icon="refresh" disabled={!ready || exploring || pendingFeedback.current.size > 0} onClick={() => void loadExplore(true)}>{exploring ? 'Updating…' : 'Refresh'}</Button></div>
          {renderExplore()}
        </>}
        {page === 'search' && <>
          <div className="page-heading"><div><h1>Search</h1><p>Find a title across available sources.</p></div></div>
          <form className="search-form" onSubmit={event => void search(event)}><label className="search-field"><Icon name="search" size={23} /><input ref={searchInput} aria-label="Search anime" placeholder="Search an anime title…" value={query} onChange={event => setQuery(event.target.value)} autoComplete="off" /><kbd>↵</kbd></label><Button variant="primary" disabled={!ready || !query.trim() || searching} type="submit">{searching ? 'Searching…' : 'Search'}<Icon name="arrow" size={17} /></Button></form>
          <div className="search-filters"><label>Source<select value={source} disabled={!ready || searching} onChange={event => setSource(event.target.value)}><option value="">All sources</option>{availableSources(state.sources, language).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><label>Language<select value={language} disabled={searching} onChange={event => { setLanguage(event.target.value); setSource('') }}><option value="all">All languages</option>{[...new Set(state.sources.map(item => item.language))].map(item => <option key={item} value={item}>{item === 'EN' ? 'English' : item === 'PT-BR' ? 'Português (Brasil)' : item}</option>)}</select></label><p><Icon name="film" size={15} />Availability varies by source.</p></div>
          {searchError && <div className="error-panel" role="alert"><Icon name="alert" /><div><h3>Search couldn’t finish</h3><p>{searchError}</p></div><Button icon="refresh" onClick={() => void search()}>Try again</Button></div>}
          {!searching && searchWarnings.length > 0 && <div className="source-notice" role="status"><Icon name="alert" size={17} /><div><h3>Some sources are unavailable</h3><ul>{searchWarnings.map((warning, index) => <li key={`${warning.source}:${index}`}><strong>{state.sources.find(item => item.id === warning.source)?.name ?? warning.source}</strong><span>{warning.message}</span></li>)}</ul></div></div>}
          {searching ? <div className="search-loading" role="status"><span className="spinner" /><h3>Searching anime…</h3><p>Searching available anime sources.</p></div> : searched && !searchError ? <section className="content-section"><div className="section-heading"><div><h2>Results for “{searched}”</h2><p>{visibleResults.length} {visibleResults.length === 1 ? 'series' : 'series'} found{language !== 'all' && ` in ${language}`}</p></div></div>{visibleResults.length > 0 ? renderCards(visibleResults) : <EmptyState icon="search" title="No series found">Try another title or choose a different source or language.</EmptyState>}</section> : !searchError && <EmptyState icon="search" title="Find a series">Enter an anime title to see available episodes.</EmptyState>}
        </>}
        {page === 'library' && <><div className="page-heading"><div><h1>Library</h1><p>Saved series and your recommendation preferences.</p></div><Button icon="search" onClick={goSearch}>Search anime</Button></div><div className="library-tabs" role="group" aria-label="Library view">{([{ id: 'saved', label: 'Saved', count: state.saved.length }, { id: 'like', label: 'Liked', count: state.feedback.filter(item => item.value === 'like').length }, { id: 'dislike', label: 'Disliked', count: state.feedback.filter(item => item.value === 'dislike').length }] as const).map(tab => <button key={tab.id} aria-pressed={libraryView === tab.id} className={libraryView === tab.id ? 'active' : ''} onClick={() => setLibraryView(tab.id)}>{tab.label}<span>{tab.count}</span></button>)}</div>{libraryView !== 'saved' && <p className="library-hint">{libraryView === 'like' ? 'These likes guide Explore. Click Like again to undo.' : 'These series are hidden from Explore. Click Dislike again to undo.'}</p>}{libraryItems.length ? renderCards(libraryItems) : <EmptyState icon={libraryView === 'saved' ? 'library' : libraryView} title={libraryView === 'saved' ? 'No saved anime' : libraryView === 'like' ? 'No likes yet' : 'No dislikes'} action={<Button icon="explore" onClick={() => setPage('explore')}>Explore anime</Button>}>{libraryView === 'saved' ? 'Use the + on an anime card to save it for later.' : libraryView === 'like' ? 'Like anime you enjoy to shape your recommendations.' : 'Disliked anime will appear here so you can undo your feedback.'}</EmptyState>}</>}
        {page === 'downloads' && <><div className="page-heading"><div><h1>Downloads{state.downloads.length > 0 && <span className="title-count">{state.downloads.length}</span>}</h1><p>Episodes saved on this device.</p></div>{activeDownloads > 0 && <span className="activity-tag"><span className="spinner small" />{activeDownloads} in progress</span>}</div>{state.downloads.length ? <div className="download-list">{state.downloads.map(renderDownload)}</div> : <EmptyState icon="download" title="No downloads yet" action={<Button variant="primary" icon="search" onClick={goSearch}>Find an episode</Button>}>Choose Download next to an episode to save it on this device.</EmptyState>}<p className="page-footnote"><Icon name="folder" size={15} />{state.settings.downloadDir || 'Choose a download folder in Settings.'}</p></>}
        {page === 'settings' && <><div className="page-heading"><div><h1>Settings</h1><p>Playback and download preferences for this device.</p></div></div><div className="settings-layout"><section className="settings-card"><div className="settings-section-title"><Icon name="play" /><div><h2>Playback</h2><p>Your episodes open with {currentRendererLabel}.</p></div></div><div className="setting-row"><div><label htmlFor="quality">Preferred quality</label><p>Use the best stream available, or a smaller one.</p></div><select id="quality" value={settingsDraft.quality} disabled={!ready} onChange={event => { setSettingsDraft({ ...settingsDraft, quality: event.target.value }); setSettingsDirty(true) }}><option value="best">Best available</option><option value="1080p">1080p</option><option value="720p">720p</option><option value="480p">480p</option><option value="360p">360p</option><option value="worst">Smallest available</option></select></div><div className="setting-row"><div><label htmlFor="mode">Version</label><p>Sub or dub availability depends on the series and source.</p></div><select id="mode" value={settingsDraft.mode} disabled={!ready} onChange={event => { setSettingsDraft({ ...settingsDraft, mode: event.target.value }); setSettingsDirty(true) }}><option value="sub">Subtitled</option><option value="dub">Dubbed</option></select></div><div className="setting-row"><div><label htmlFor="upscaler">Upscaling</label><p>{state.metalFXAvailable ? `MetalFX 2x available on ${state.metalFXDevice || 'Apple GPU'}.` : state.metalFXReason || 'MetalFX availability is checked by the desktop app.'}</p></div><select id="upscaler" value={settingsDraft.upscaler} disabled={!ready} onChange={event => { setSettingsDraft({ ...settingsDraft, upscaler: event.target.value as Settings['upscaler'] }); setSettingsDirty(true) }}><option value="off">Off</option><option value="metalfx" disabled={!state.metalFXAvailable}>MetalFX 2x</option></select></div><div className="path-setting"><div className="path-setting-heading"><label htmlFor="mpv-path">mpv executable</label><span className={`status-tag ${state.mpvAvailable ? 'status-completed' : 'status-cancelled'}`}>{state.mpvAvailable ? 'Available' : ready ? 'Not found' : 'Not connected'}</span></div><div className="path-control"><input id="mpv-path" value={settingsDraft.mpvPath} disabled={!ready} placeholder="Auto-detect mpv" onChange={event => { setSettingsDraft({ ...settingsDraft, mpvPath: event.target.value }); setSettingsDirty(true) }} /><Button icon="folder" disabled={!ready || busy.includes('pick:mpvPath')} onClick={() => void pickPath('mpvPath')}>Choose</Button></div><p>Leave blank to automatically find mpv. It is only required when upscaling is off.</p></div></section><section className="settings-card"><div className="settings-section-title"><Icon name="download" /><div><h2>Downloads</h2><p>Choose where downloaded episodes are saved.</p></div></div><div className="path-setting"><label htmlFor="download-path">Download folder</label><div className="path-control"><input id="download-path" value={settingsDraft.downloadDir} disabled={!ready} placeholder="Choose a folder" onChange={event => { setSettingsDraft({ ...settingsDraft, downloadDir: event.target.value }); setSettingsDirty(true) }} /><Button icon="folder" disabled={!ready || busy.includes('pick:downloadDir')} onClick={() => void pickPath('downloadDir')}>Choose</Button></div></div></section><div className="settings-footer"><span>{settingsDirty ? 'You have unsaved changes.' : 'All preferences are up to date.'}</span><Button variant="primary" icon="check" disabled={!ready || !settingsDirty || busy.includes('settings')} onClick={() => void saveSettings(settingsDraft)}>{busy.includes('settings') ? 'Saving...' : 'Save preferences'}</Button></div></div><div className="about-card"><span className="brand-mark"><Icon name="play" size={20} /></span><div><h3>SERIAL Desktop</h3><p>Your library and feedback stay on this device.</p></div><span>v0.5.3</span></div></>}
      </main>
      <footer className={`player-bar ${state.player.active ? 'player-active' : ''}`} aria-label="Playback controls">
        <div className="player-track">{state.player.anime ? <Poster anime={state.player.anime} compact /> : <div className="player-placeholder"><Icon name="film" size={23} /></div>}<div><strong>{state.player.active ? state.player.anime?.title ?? 'Now playing' : 'Nothing playing'}</strong><p>{state.player.active ? `${episodeLabel(state.player.episode)}${actualEpisodeTitle(state.player.episode) ? ` · ${actualEpisodeTitle(state.player.episode)}` : ''} · Playing in ${playerRendererLabel}${playerResolution ? ` · ${playerResolution}` : ''}` : `Choose an episode to watch with ${currentRendererLabel}.`}</p></div></div>
        <div className="player-center"><div className="player-buttons"><Button variant="icon" icon="stop" aria-label="Stop playback" disabled={!ready || !state.player.active} onClick={() => void command('stop')} /><Button variant="ghost" className="player-jump" aria-label="Rewind 10 seconds" disabled={!ready || !state.player.active} onClick={() => { const value = seekRelative(-10); setSeekDraft(value); void command('seek', value) }}>-10</Button><button className="player-play" aria-label={state.player.paused ? 'Resume playback' : 'Pause playback'} disabled={!ready || !state.player.active} onClick={() => void command('pause')}><Icon name={state.player.active && !state.player.paused ? 'pause' : 'play'} size={18} /></button><Button variant="ghost" className="player-jump" aria-label="Forward 10 seconds" disabled={!ready || !state.player.active || !state.player.duration} onClick={() => { const value = seekRelative(10); setSeekDraft(value); void command('seek', value) }}>+10</Button><span className="player-external"><Icon name="external" size={14} />{playerRendererLabel}</span></div><div className="player-progress"><span>{formatTime(state.player.position)}</span><input type="range" min="0" max={state.player.duration || 1} value={seekDraft ?? Math.min(state.player.position, state.player.duration || 1)} step="1" disabled={!ready || !state.player.active || !state.player.duration} aria-label="Playback position" onChange={event => { const value = Number(event.target.value); setSeekDraft(value); void command('seek', value) }} /><span>{formatTime(state.player.duration)}</span></div></div>
        <div className="player-controls-right"><label className="player-speed">Speed<select value={speedDraft ?? state.player.speed} disabled={!ready || !state.player.active} aria-label="Playback speed" onChange={event => { const value = Number(event.target.value); setSpeedDraft(value); void command('speed', value) }}>{playbackSpeeds.map(speed => <option key={speed} value={speed}>{speed}x</option>)}</select></label><div className="player-volume"><Button variant="icon" icon="volume" aria-label={(volumeDraft ?? state.player.volume) > 0 ? 'Mute playback' : 'Restore volume'} disabled={!ready || !state.player.active} onClick={() => { const current = volumeDraft ?? state.player.volume; const value = current > 0 ? 0 : Math.max(1, lastVolume.current || 100); setVolumeDraft(value); void command('volume', value) }} /><input type="range" min="0" max="100" value={volumeDraft ?? state.player.volume} disabled={!ready || !state.player.active} aria-label="Volume" onChange={event => { const value = Number(event.target.value); setVolumeDraft(value); void command('volume', value) }} /><span>{Math.round(volumeDraft ?? state.player.volume)}%</span></div></div>
      </footer>
    </div>
    {selected && <div className="dialog-backdrop" onClick={event => { if (event.target === event.currentTarget) closeAnimeDetails() }}><section ref={dialogRef} className="detail-dialog" role="dialog" aria-modal="true" aria-labelledby="detail-title"><button className="dialog-close button button-icon" aria-label="Close anime details" onClick={closeAnimeDetails}><Icon name="close" /></button><div className="detail-summary"><Poster anime={selected} /><div><span className="detail-source">{selected.source === 'mal' ? 'MyAnimeList' : selected.source} · {selected.language || selected.metadataSource || 'Metadata'}</span><h2 id="detail-title">{selected.title}</h2>{selected.score > 0 && <span className="detail-rating">★ {selected.score.toFixed(1)}</span>}{visibleTags(selected, Number.MAX_SAFE_INTEGER).length === 0 && (selected.genres ?? []).length > 0 && <div className="genres">{selected.genres?.map(genre => <span key={genre}>{genre}</span>)}</div>}<TagDisclosure anime={selected} initialLimit={10} className="genres tag-genres" /><p className="detail-description">{selected.description || 'This source hasn’t provided a description for this series.'}</p><div className="detail-actions"><Button icon={isSaved(selected) ? 'check' : 'plus'} disabled={!ready || busy.includes(`save:${selected.id}`)} onClick={() => void saveAnime(selected)}>{isSaved(selected) ? 'Saved' : 'Save for later'}</Button>{selected.trailer?.youtubeId && <Button icon="external" disabled={!ready} onClick={() => openTrailerFallback(selected)}>Open trailer</Button>}<FeedbackButtons anime={selected} value={feedbackFor(state.feedback, selected)} onChange={value => changeFeedback(selected, value)} disabled={!ready || busy.includes(`feedback:${animeKey(selected)}`)} /></div></div></div><div className="episode-heading"><div><h3>Episodes{episodes.length > 0 && <span className="title-count">{episodes.length}</span>}</h3><p>{selected.source === 'mal' ? 'Resolving a streaming source' : selected.language || 'Source language'} · {state.settings.mode === 'dub' ? 'Dub preferred' : 'Sub preferred'}</p></div><label className="episode-search"><Icon name="search" size={16} /><input aria-label="Filter episodes" placeholder="Find episode" value={episodeFilter} onChange={event => setEpisodeFilter(event.target.value)} /></label></div><div className="episode-preferences"><label>Quality<select value={state.settings.quality} disabled={!ready || busy.includes('settings')} onChange={event => void operation('settings', async () => acceptState(await bridge().SaveSettings({ ...state.settings, quality: event.target.value })))}><option value="best">Best available</option><option value="1080p">1080p</option><option value="720p">720p</option><option value="480p">480p</option><option value="360p">360p</option><option value="worst">Smallest</option></select></label><label>Version<select value={state.settings.mode} disabled={!ready || busy.includes('settings')} onChange={event => void operation('settings', async () => acceptState(await bridge().SaveSettings({ ...state.settings, mode: event.target.value })))}><option value="sub">Subtitled</option><option value="dub">Dubbed</option></select></label><label>Upscale<select value={state.settings.upscaler} disabled={!ready || busy.includes('settings')} onChange={event => void operation('settings', async () => acceptState(await bridge().SaveSettings({ ...state.settings, upscaler: event.target.value as Settings['upscaler'] })))}><option value="off">Off</option><option value="metalfx" disabled={!state.metalFXAvailable}>MetalFX 2x</option></select></label><span>{selected.source === 'animefire' ? /dublado/i.test(`${selected.title} ${selected.url}`) ? 'Dubbed result · choose Dubbed.' : 'Choose a Dublado result for dub.' : 'Availability depends on the series.'}</span></div>{!playbackAvailable && <div className="episode-notice"><Icon name="alert" size={16} /><span>{state.settings.upscaler === 'metalfx' ? state.metalFXReason || 'MetalFX is not available on this device.' : 'Choose an mpv executable in Settings to watch with upscaling off.'}</span></div>}{episodesLoading ? <div className="episode-loading" role="status"><span className="spinner" />Loading episodes…</div> : episodesError ? <div className="error-panel" role="alert"><Icon name="alert" /><p>{episodesError}</p>{selected.source === 'mal' ? <Button icon="search" onClick={() => { setQuery(selected.title); closeAnimeDetails(); goSearch() }}>Find in Search</Button> : <Button icon="refresh" onClick={() => void openAnime(selected)}>Try again</Button>}</div> : episodes.length === 0 ? <EmptyState icon="film" title="No episodes available">This source did not return any episodes. Try another source.</EmptyState> : <div className="episode-list">{episodes.filter(episode => `${episode.number} ${episode.title}`.toLowerCase().includes(episodeFilter.toLowerCase())).map(episode => <div className="episode-row" key={`${episode.number}:${episode.url}`}><span className="episode-number">{episode.number}</span><div className="episode-title"><strong>{actualEpisodeTitle(episode) || episodeLabel(episode)}</strong><span>{selected.language} · {state.settings.mode === 'dub' ? 'Dub preferred' : 'Sub preferred'} · {currentRendererLabel}</span></div><Button variant="icon" icon="download" title="Download episode" aria-label={`Download ${episodeLabel(episode)}`} disabled={!ready || busy.includes('settings') || busy.includes(`download:${selected.id}:${episode.number}`)} onClick={() => void download(selected, episode)} /><Button icon="play" disabled={!ready || !playbackAvailable || busy.includes('play') || busy.includes('settings')} onClick={() => void play(selected, episode)}>{busy.includes('play') ? 'Opening...' : 'Watch'}</Button></div>)}</div>}</section></div>}
    {toast && <div className={`toast ${toast.error ? 'toast-error' : ''}`} role={toast.error ? 'alert' : 'status'}><Icon name={toast.error ? 'alert' : 'check'} size={18} /><span>{toast.message}</span><button aria-label="Dismiss notification" onClick={() => setToast(null)}><Icon name="close" size={16} /></button></div>}
    {state.player.error && <div className="player-error" role="alert"><Icon name="alert" size={17} /><span>{state.player.error}</span></div>}
  </div>
}
