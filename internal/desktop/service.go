package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Service is the sole owner of durable GUI state. Provider and player work
// happen outside its lock; short state changes are serialized and persisted.
type Service struct {
	mu                sync.Mutex
	playMu            sync.Mutex
	enqueueMu         sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	state             State
	store             *Store
	catalog           *Catalog
	player            *Player
	downloads         *Downloader
	emit              func(State)
	lastCheckpoint    time.Time
	recommender       *Recommender
	episodeTitles     *EpisodeTitleEnricher
	metadata          *MALMetadata
	metalFXExecutable string
}

func NewService(path string, emit func(State)) (*Service, error) {
	store, err := NewStore(path)
	if err != nil {
		return nil, err
	}
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{ctx: ctx, cancel: cancel, state: state, store: store, catalog: NewCatalog(), emit: emit}
	s.recommender = NewRecommender()
	s.episodeTitles = NewEpisodeTitleEnricher()
	s.metadata = NewMALMetadata(s.episodeTitles, filepath.Join(filepath.Dir(path), "metadata-cache.json"))
	s.metalFXExecutable, s.state.MetalFXAvailable, s.state.MetalFXDevice, s.state.MetalFXReason = metalFXCapabilities()
	s.state.Sources = s.catalog.Sources()
	s.state.Player = PlayerState{Volume: 100, Speed: 1}
	s.state.TrailerPreviewAvailable = runtime.GOOS == "darwin"
	s.player = NewPlayer(s.playerChanged)
	s.downloads = NewDownloader(s.catalog.Resolve, s.downloadChanged)
	return s, nil
}
func DefaultDataPath() (string, error) {
	if custom := os.Getenv("GOANIME_GUI_DATA_DIR"); custom != "" {
		return filepath.Join(custom, "state.json"), nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "GoAnimeGUI", "state.json"), nil
}
func (s *Service) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := cloneState(s.state)
	_, err := FindMPV(state.Settings.MPVPath)
	state.MPVAvailable = err == nil
	return state
}
func cloneState(state State) State {
	state.History = append([]HistoryEntry{}, state.History...)
	state.Saved = append([]Anime{}, state.Saved...)
	state.Downloads = append([]Download{}, state.Downloads...)
	state.Sources = append([]Source{}, state.Sources...)
	state.Feedback = append([]AnimeFeedback{}, state.Feedback...)
	for i := range state.Feedback {
		state.Feedback[i].Anime = cloneAnime(state.Feedback[i].Anime)
	}
	for i := range state.Saved {
		state.Saved[i] = cloneAnime(state.Saved[i])
	}
	for i := range state.History {
		state.History[i].Anime = cloneAnime(state.History[i].Anime)
	}
	for i := range state.Downloads {
		state.Downloads[i].Anime = cloneAnime(state.Downloads[i].Anime)
	}
	if state.Player.Anime != nil {
		anime := cloneAnime(*state.Player.Anime)
		state.Player.Anime = &anime
	}
	if state.Player.Episode != nil {
		episode := *state.Player.Episode
		state.Player.Episode = &episode
	}
	return state
}
func (s *Service) publish() {
	if s.emit != nil {
		s.emit(s.Snapshot())
	}
}
func (s *Service) Search(query, source string) ([]Anime, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	return s.catalog.Search(ctx, query, source)
}
func (s *Service) SearchWithStatus(query, source string) (SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	return s.catalog.SearchWithStatus(ctx, query, source)
}

func (s *Service) GetExplore(refresh bool) (ExploreResult, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	state := s.Snapshot()
	known := append([]Anime{}, state.Saved...)
	for _, entry := range state.History {
		known = append(known, entry.Anime)
	}
	return s.recommender.Explore(ctx, state.Feedback, known, refresh)
}

// Feedback is separate from the watchlist. Saving a title does not imply a
// positive rating, and clearing a rating restores its discovery eligibility.
func (s *Service) SetFeedback(anime Anime, value string) (State, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" && value != "like" && value != "dislike" {
		return State{}, errors.New("choose like, dislike, or clear feedback")
	}
	if strings.TrimSpace(anime.ID) == "" || feedbackTitleKey(anime.Title) == "" || len(anime.Title) > 500 {
		return State{}, errors.New("invalid anime feedback")
	}
	if anime.Source == "mal" {
		if err := ValidateMALAnime(anime); err != nil {
			return State{}, err
		}
	} else if anime.Source == "hianime" {
		if _, _, err := hiAnimeIdentity(anime); err != nil {
			return State{}, err
		}
	} else {
		kind, _, err := catalogSource(anime.Source)
		if err != nil {
			return State{}, err
		}
		if err := catalogSourceURL(anime.URL, kind); err != nil {
			return State{}, err
		}
	}
	anime = cloneAnime(anime)
	s.mu.Lock()
	previous := s.state.Feedback
	previousRevision := s.state.FeedbackRevision
	entries := []AnimeFeedback{}
	for _, entry := range previous {
		if !sameStoredAnime(entry.Anime, anime) {
			entries = append(entries, entry)
		}
	}
	if value != "" {
		entries = append([]AnimeFeedback{{Anime: anime, Value: value, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, entries...)
	}
	s.state.Feedback = entries
	s.state.FeedbackRevision++
	err := s.store.Save(s.state)
	if err != nil {
		s.state.Feedback = previous
		s.state.FeedbackRevision = previousRevision
	}
	s.mu.Unlock()
	if err != nil {
		return State{}, fmt.Errorf("could not save feedback: %w", err)
	}
	s.publish()
	return s.Snapshot(), nil
}

func feedbackTitleKey(title string) string {
	title = animeIdentityTitle(title)
	var out strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
func (s *Service) Episodes(anime Anime) ([]Episode, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	episodes, err := s.catalog.Episodes(ctx, anime)
	if err != nil {
		return nil, err
	}
	return s.episodeTitles.Enrich(ctx, anime, episodes), nil
}
func (s *Service) Play(anime Anime, episode Episode, resume float64) error {
	if !s.playMu.TryLock() {
		return errors.New("another episode is opening; please wait")
	}
	defer s.playMu.Unlock()
	settings := s.Snapshot().Settings
	executable, err := s.playbackExecutable(settings)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
	defer cancel()
	stream, err := s.catalog.Resolve(ctx, anime, episode, settings)
	if err != nil {
		return err
	}
	named := s.episodeTitles.Enrich(ctx, anime, []Episode{episode})
	if len(named) == 1 {
		episode = named[0]
	}
	if settings.Upscaler == "metalfx" {
		return s.player.StartMetalFX(ctx, executable, stream, anime, episode, resume, settings.Mode)
	}
	return s.player.Start(ctx, executable, stream, anime, episode, resume, settings.Mode)
}
func (s *Service) PlayerCommand(command string, value float64) error {
	return s.player.Command(command, value)
}
func (s *Service) SaveSettings(settings Settings) (State, error) {
	if settings.Upscaler == "" {
		settings.Upscaler = "off"
	}
	if settings.Upscaler != "off" && settings.Upscaler != "metalfx" {
		return State{}, errors.New("choose Off or MetalFX upscaling")
	}
	if settings.Upscaler == "metalfx" && !s.state.MetalFXAvailable {
		return State{}, errors.New(s.state.MetalFXReason)
	}
	qualityValid := false
	for _, quality := range []string{"best", "worst", "1080p", "720p", "480p", "360p"} {
		if settings.Quality == quality {
			qualityValid = true
		}
	}
	if !qualityValid {
		return State{}, errors.New("choose a supported video quality")
	}
	if settings.Mode != "sub" && settings.Mode != "dub" {
		return State{}, errors.New("choose sub or dub")
	}
	if !filepath.IsAbs(settings.DownloadDir) {
		return State{}, errors.New("choose an absolute download folder")
	}
	settings.DownloadDir = filepath.Clean(settings.DownloadDir)
	if strings.ContainsAny(settings.DownloadDir, "\x00") {
		return State{}, errors.New("invalid download folder")
	}
	if settings.MPVPath != "" {
		path, err := FindMPV(settings.MPVPath)
		if err != nil {
			return State{}, err
		}
		settings.MPVPath = path
	}
	s.mu.Lock()
	previous := s.state.Settings
	s.state.Settings = settings
	err := s.store.Save(s.state)
	if err != nil {
		s.state.Settings = previous
	}
	s.mu.Unlock()
	if err != nil {
		return State{}, fmt.Errorf("could not save preferences: %w", err)
	}
	s.publish()
	return s.Snapshot(), nil
}
func (s *Service) SaveAnime(anime Anime, saved bool) (State, error) {
	if anime.ID == "" || anime.Title == "" || anime.URL == "" {
		return State{}, errors.New("invalid anime")
	}
	if anime.Source == "mal" {
		if err := ValidateMALAnime(anime); err != nil {
			return State{}, err
		}
	}
	anime = cloneAnime(anime)
	s.mu.Lock()
	previous := append([]Anime{}, s.state.Saved...)
	entries := []Anime{}
	for _, entry := range s.state.Saved {
		if !sameStoredAnime(entry, anime) {
			entries = append(entries, entry)
		}
	}
	if saved {
		entries = append([]Anime{anime}, entries...)
	}
	s.state.Saved = entries
	err := s.store.Save(s.state)
	if err != nil {
		s.state.Saved = previous
	}
	s.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	s.publish()
	return s.Snapshot(), nil
}
func (s *Service) RemoveHistory(id string) (State, error) {
	s.mu.Lock()
	previous := append([]HistoryEntry{}, s.state.History...)
	entries := []HistoryEntry{}
	for _, entry := range s.state.History {
		if entry.Anime.ID != id {
			entries = append(entries, entry)
		}
	}
	s.state.History = entries
	err := s.store.Save(s.state)
	if err != nil {
		s.state.History = previous
	}
	s.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	s.publish()
	return s.Snapshot(), nil
}
func (s *Service) playerChanged(player PlayerState) {
	s.mu.Lock()
	s.state.Player = player
	if player.Anime != nil && player.Episode != nil {
		entry := HistoryEntry{Anime: *player.Anime, Episode: *player.Episode, Position: player.Position, Duration: player.Duration, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		entries := []HistoryEntry{entry}
		for _, prior := range s.state.History {
			if prior.Anime.ID != entry.Anime.ID {
				entries = append(entries, prior)
			}
		}
		if len(entries) > 100 {
			entries = entries[:100]
		}
		s.state.History = entries
	}
	if !player.Active || time.Since(s.lastCheckpoint) >= 3*time.Second {
		if err := s.store.Save(s.state); err != nil {
			s.state.Player.Error = "Could not save playback history: " + err.Error()
		}
		s.lastCheckpoint = time.Now()
	}
	s.mu.Unlock()
	s.publish()
}
func (s *Service) downloadChanged(download Download) {
	s.mu.Lock()
	found := false
	for i, entry := range s.state.Downloads {
		if entry.ID == download.ID {
			s.state.Downloads[i] = download
			found = true
			break
		}
	}
	if !found {
		s.state.Downloads = append([]Download{download}, s.state.Downloads...)
	}
	if err := s.store.Save(s.state); err != nil {
		s.state.Player.Error = "Could not save download history: " + err.Error()
	}
	s.mu.Unlock()
	s.publish()
}
func (s *Service) DownloadEpisode(anime Anime, episode Episode) (string, error) {
	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	settings := s.Snapshot().Settings
	for _, entry := range s.Snapshot().Downloads {
		if entry.Anime.ID == anime.ID && entry.Episode.Number == episode.Number && (entry.Status == "queued" || entry.Status == "resolving" || entry.Status == "downloading") {
			return "", errors.New("this episode is already in the download queue")
		}
	}
	download, err := s.downloads.Start(anime, episode, settings)
	return download.ID, err
}
func (s *Service) CancelDownload(id string) error { return s.downloads.Cancel(id) }
func (s *Service) RetryDownload(id string) error {
	for _, entry := range s.Snapshot().Downloads {
		if entry.ID == id {
			if entry.Status != "failed" && entry.Status != "cancelled" {
				return errors.New("this download cannot be retried")
			}
			_, err := s.DownloadEpisode(entry.Anime, entry.Episode)
			return err
		}
	}
	return errors.New("download not found")
}
func (s *Service) DownloadPath(id string) (string, error) {
	for _, entry := range s.Snapshot().Downloads {
		if entry.ID == id && entry.Status == "completed" {
			info, err := os.Stat(entry.Path)
			if err != nil {
				return "", errors.New("the downloaded file has been moved or deleted")
			}
			if info.IsDir() {
				return "", errors.New("the downloaded file is unavailable")
			}
			return entry.Path, nil
		}
	}
	return "", errors.New("download is not complete")
}
func (s *Service) OpenDownload(id string) error {
	path, err := s.DownloadPath(id)
	if err != nil {
		return err
	}
	if !s.playMu.TryLock() {
		return errors.New("another episode is opening; please wait")
	}
	defer s.playMu.Unlock()
	settings := s.Snapshot().Settings
	executable, err := s.playbackExecutable(settings)
	if err != nil {
		return err
	}
	for _, entry := range s.Snapshot().Downloads {
		if entry.ID == id {
			ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
			defer cancel()
			if settings.Upscaler == "metalfx" {
				return s.player.StartMetalFX(ctx, executable, Stream{URL: path}, entry.Anime, entry.Episode, 0, settings.Mode)
			}
			return s.player.Start(ctx, executable, Stream{URL: path}, entry.Anime, entry.Episode, 0, settings.Mode)
		}
	}
	return errors.New("download not found")
}
func (s *Service) Close() {
	s.cancel()
	s.player.Stop()
	s.downloads.Close()
	s.mu.Lock()
	s.store.Save(s.state)
	s.mu.Unlock()
}
