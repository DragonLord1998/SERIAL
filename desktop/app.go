package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/alvarorichard/Goanime/internal/desktop"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	mu             sync.Mutex
	ctx            context.Context
	ready          bool
	service        *desktop.Service
	startupError   error
	trailerOpening int
	trailerClosing bool
}

func NewApp() *App {
	app := &App{}
	path, err := desktop.DefaultDataPath()
	if err != nil {
		app.startupError = err
		return app
	}
	app.service, app.startupError = desktop.NewService(path, app.emit)
	return app
}
func (a *App) startup(ctx context.Context) { a.mu.Lock(); a.ctx = ctx; a.mu.Unlock() }
func (a *App) domReady(ctx context.Context) {
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	if a.service != nil {
		a.emit(a.service.Snapshot())
	}
}
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	a.ready = false
	a.trailerClosing = true
	a.mu.Unlock()
	stopTrailerPreview(0)
	if a.service != nil {
		a.service.Close()
	}
}
func (a *App) emit(state desktop.State) {
	a.mu.Lock()
	ctx, ready := a.ctx, a.ready
	a.mu.Unlock()
	if ready {
		runtime.EventsEmit(ctx, "app:state", state)
	}
}
func (a *App) available() error {
	if a.startupError != nil {
		return a.startupError
	}
	if a.service == nil {
		return errors.New("the app could not load local data")
	}
	return nil
}
func (a *App) Bootstrap() (desktop.State, error) {
	if err := a.available(); err != nil {
		return desktop.State{}, err
	}
	return a.service.Snapshot(), nil
}
func (a *App) Search(query, source string) ([]desktop.Anime, error) {
	if err := a.available(); err != nil {
		return nil, err
	}
	return a.service.Search(query, source)
}
func (a *App) SearchWithStatus(query, source string) (desktop.SearchResult, error) {
	if err := a.available(); err != nil {
		return desktop.SearchResult{}, err
	}
	return a.service.SearchWithStatus(query, source)
}

func (a *App) GetExplore(refresh bool) (desktop.ExploreResult, error) {
	if err := a.available(); err != nil {
		return desktop.ExploreResult{}, err
	}
	return a.service.GetExplore(refresh)
}
func (a *App) SetFeedback(anime desktop.Anime, value string) (desktop.State, error) {
	if err := a.available(); err != nil {
		return desktop.State{}, err
	}
	return a.service.SetFeedback(anime, value)
}

func (a *App) GetAnimeDetails(anime desktop.Anime) (desktop.Anime, error) {
	if err := a.available(); err != nil {
		return desktop.Anime{}, err
	}
	return a.service.GetAnimeDetails(anime)
}
func (a *App) GetAnimeTags() ([]desktop.AnimeTag, error) {
	if err := a.available(); err != nil {
		return nil, err
	}
	return a.service.GetAnimeTags()
}
func (a *App) GetExploreWithTags(tagIDs []int, refresh bool) (desktop.ExploreResult, error) {
	if err := a.available(); err != nil {
		return desktop.ExploreResult{}, err
	}
	return a.service.GetExploreWithTags(tagIDs, refresh)
}
func (a *App) ResolveAnime(anime desktop.Anime) (desktop.Anime, error) {
	if err := a.available(); err != nil {
		return desktop.Anime{}, err
	}
	return a.service.ResolveAnime(anime)
}

func (a *App) trailerPlaybackAllowed() error {
	if err := a.available(); err != nil {
		return err
	}
	a.mu.Lock()
	blocked := a.trailerClosing || a.trailerOpening > 0
	a.mu.Unlock()
	if blocked || a.service.Snapshot().Player.Active {
		return errors.New("Stop the episode before previewing a trailer.")
	}
	return nil
}

// Do not hold a Go mutex while dispatching to AppKit's main thread. Check both
// sides of creation so an overlapping episode open/shutdown cannot leave a player.
func (a *App) performTrailerPlayback(token uint64, action func() error) error {
	if err := a.trailerPlaybackAllowed(); err != nil {
		return err
	}
	if err := action(); err != nil {
		return err
	}
	if err := a.trailerPlaybackAllowed(); err != nil {
		stopTrailerPreview(token)
		return err
	}
	return nil
}
func (a *App) openingEpisode() func() {
	a.mu.Lock()
	a.trailerOpening++
	a.mu.Unlock()
	return func() { a.mu.Lock(); a.trailerOpening--; a.mu.Unlock() }
}
func (a *App) StartTrailerPreview(youtubeID string, x, y, width, height float64, token uint64) error {
	return a.performTrailerPlayback(token, func() error { return startTrailerPreview(youtubeID, x, y, width, height, token) })
}
func (a *App) MoveTrailerPreview(token uint64, x, y, width, height float64) error {
	return moveTrailerPreview(token, x, y, width, height)
}
func (a *App) PauseTrailerPreview(token uint64) error {
	if err := a.available(); err != nil {
		return err
	}
	return pauseTrailerPreview(token)
}
func (a *App) ResumeTrailerPreview(token uint64) error {
	return a.performTrailerPlayback(token, func() error { return resumeTrailerPreview(token) })
}
func (a *App) RestartTrailerPreview(token uint64) error {
	return a.performTrailerPlayback(token, func() error { return restartTrailerPreview(token) })
}
func (a *App) StopTrailerPreview(token uint64) { stopTrailerPreview(token) }
func (a *App) GetTrailerPreviewStatus(token uint64) TrailerPreviewStatus {
	return trailerPreviewStatus(token)
}

var validTrailerID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func (a *App) OpenTrailer(youtubeID string) error {
	if !validTrailerID.MatchString(youtubeID) {
		return errors.New("Invalid trailer.")
	}
	stopTrailerPreview(0)
	runtime.BrowserOpenURL(a.ctx, "https://www.youtube.com/watch?v="+youtubeID)
	return nil
}
func (a *App) GetEpisodes(anime desktop.Anime) ([]desktop.Episode, error) {
	if err := a.available(); err != nil {
		return nil, err
	}
	return a.service.Episodes(anime)
}
func (a *App) Play(anime desktop.Anime, episode desktop.Episode, resumeSeconds float64) error {
	finishOpening := a.openingEpisode()
	defer finishOpening()
	if err := a.available(); err != nil {
		return err
	}
	stopTrailerPreview(0)
	return a.service.Play(anime, episode, resumeSeconds)
}
func (a *App) PlayerCommand(command string, value float64) error {
	if err := a.available(); err != nil {
		return err
	}
	return a.service.PlayerCommand(command, value)
}
func (a *App) SaveAnime(anime desktop.Anime, saved bool) (desktop.State, error) {
	if err := a.available(); err != nil {
		return desktop.State{}, err
	}
	return a.service.SaveAnime(anime, saved)
}
func (a *App) SaveSettings(settings desktop.Settings) (desktop.State, error) {
	if err := a.available(); err != nil {
		return desktop.State{}, err
	}
	return a.service.SaveSettings(settings)
}
func (a *App) RemoveHistory(id string) (desktop.State, error) {
	if err := a.available(); err != nil {
		return desktop.State{}, err
	}
	return a.service.RemoveHistory(id)
}
func (a *App) DownloadEpisode(anime desktop.Anime, episode desktop.Episode) (string, error) {
	if err := a.available(); err != nil {
		return "", err
	}
	return a.service.DownloadEpisode(anime, episode)
}
func (a *App) CancelDownload(id string) error {
	if err := a.available(); err != nil {
		return err
	}
	return a.service.CancelDownload(id)
}
func (a *App) RetryDownload(id string) error {
	if err := a.available(); err != nil {
		return err
	}
	return a.service.RetryDownload(id)
}
func (a *App) OpenDownload(id string) error {
	finishOpening := a.openingEpisode()
	defer finishOpening()
	if err := a.available(); err != nil {
		return err
	}
	stopTrailerPreview(0)
	return a.service.OpenDownload(id)
}
func (a *App) ChooseDownloadDirectory() (string, error) {
	if err := a.available(); err != nil {
		return "", err
	}
	directory := a.service.Snapshot().Settings.DownloadDir
	if _, err := os.Stat(directory); err != nil {
		directory, _ = os.UserHomeDir()
	}
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose a download folder", DefaultDirectory: directory, CanCreateDirectories: true})
}
func (a *App) ChooseMPV() (string, error) {
	if err := a.available(); err != nil {
		return "", err
	}
	directory := ""
	if executable, err := desktop.FindMPV(a.service.Snapshot().Settings.MPVPath); err == nil {
		directory = filepath.Dir(executable)
	}
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose the mpv executable", DefaultDirectory: directory, Filters: []runtime.FileFilter{{DisplayName: "mpv executable", Pattern: "mpv;mpv.exe"}, {DisplayName: "All files", Pattern: "*"}}})
}
