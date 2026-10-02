export interface Anime {
  id: string
  title: string
  url: string
  imageUrl: string
  source: string
  language: string
  description: string
  genres: string[] | null
  score: number
  episodeCount: number
  malId?: number
  tags?: AnimeTag[] | null
  trailer?: AnimeTrailer | null
  metadataSource?: string
  alternateTitles?: string[] | null
}

export interface Episode { number: string; title: string; url: string }
export interface Source { id: string; name: string; language: string }
export interface AnimeTag { id: number; name: string; kind: string }
export interface AnimeTrailer { youtubeId: string; url: string }
export interface TrailerPreviewStatus { token: number; phase: string; message: string; position: number }
export interface SearchWarning { source: string; message: string }
export interface SearchResult { animes: Anime[] | null; warnings: SearchWarning[] | null }
export type FeedbackValue = 'like' | 'dislike'
export interface Feedback { anime: Anime; value: FeedbackValue; updatedAt: string }
export interface ExploreItem { anime: Anime; reason: string }
export interface ExploreResult { items: ExploreItem[] | null; personalized: boolean; message: string; warnings: string[] | null }
export type Upscaler = 'off' | 'metalfx'
export type Renderer = 'mpv' | 'metalfx'
export interface Settings { quality: string; mode: string; downloadDir: string; mpvPath: string; upscaler: Upscaler }
export interface HistoryEntry {
  anime: Anime
  episode: Episode
  position: number
  duration: number
  updatedAt: string
}
export interface Download {
  id: string
  anime: Anime
  episode: Episode
  status: string
  progress: number
  bytes: number
  totalBytes: number
  path: string
  error: string
}
export interface PlayerState {
  active: boolean
  paused: boolean
  position: number
  duration: number
  volume: number
  speed: number
  renderer: Renderer
  upscaling: boolean
  videoWidth: number
  videoHeight: number
  outputWidth: number
  outputHeight: number
  anime?: Anime
  episode?: Episode
  error: string
}
export interface State {
  settings: Settings
  sources: Source[]
  history: HistoryEntry[]
  saved: Anime[]
  feedback: Feedback[]
  feedbackRevision: number
  downloads: Download[]
  player: PlayerState
  mpvAvailable: boolean
  metalFXAvailable: boolean
  metalFXDevice: string
  metalFXReason: string
  trailerPreviewAvailable: boolean
}

export interface AppBridge {
  Bootstrap(): Promise<State>
  Search(query: string, source: string): Promise<Anime[] | null>
  SearchWithStatus(query: string, source: string): Promise<SearchResult>
  GetExplore(refresh: boolean): Promise<ExploreResult>
  GetExploreWithTags(tagIds: number[], refresh: boolean): Promise<ExploreResult>
  GetAnimeDetails(anime: Anime): Promise<Anime>
  ResolveAnime(anime: Anime): Promise<Anime>
  GetAnimeTags(): Promise<AnimeTag[]>
  SetFeedback(anime: Anime, value: FeedbackValue | ''): Promise<State>
  GetEpisodes(anime: Anime): Promise<Episode[] | null>
  Play(anime: Anime, episode: Episode, resumeSeconds: number): Promise<void>
  PlayerCommand(command: string, value: number): Promise<void>
  SaveAnime(anime: Anime, saved: boolean): Promise<State>
  SaveSettings(settings: Settings): Promise<State>
  ChooseDownloadDirectory(): Promise<string>
  ChooseMPV(): Promise<string>
  DownloadEpisode(anime: Anime, episode: Episode): Promise<string>
  CancelDownload(id: string): Promise<void>
  RetryDownload(id: string): Promise<void>
  OpenDownload(id: string): Promise<void>
  RemoveHistory(animeId: string): Promise<State>
  StartTrailerPreview(youtubeId: string, x: number, y: number, width: number, height: number, token: number): Promise<void>
  MoveTrailerPreview(token: number, x: number, y: number, width: number, height: number): Promise<void>
  GetTrailerPreviewStatus(token: number): Promise<TrailerPreviewStatus>
  StopTrailerPreview(token: number): Promise<void>
  PauseTrailerPreview(token: number): Promise<void>
  ResumeTrailerPreview(token: number): Promise<void>
  RestartTrailerPreview(token: number): Promise<void>
  OpenTrailer(youtubeId: string): Promise<void>
}

declare global {
  interface Window {
    go?: { main?: { App?: AppBridge } }
    runtime?: {
      EventsOn(name: string, callback: (state: State) => void): () => void
    }
  }
}
