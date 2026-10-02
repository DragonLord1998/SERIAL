package desktop

import (
	"context"
	"path/filepath"
	"testing"
)

func TestServicePreferencesAndSavedAnimePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := service.Snapshot().Settings
	settings.Quality = "720p"
	settings.Mode = "dub"
	settings.DownloadDir = t.TempDir()
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	anime := Anime{ID: "saved", Title: "Saved anime", URL: "https://anidb.app/anime/1", Source: "anidb"}
	if _, err := service.SaveAnime(anime, true); err != nil {
		t.Fatal(err)
	}
	service.Close()
	reopened, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state := reopened.Snapshot()
	if state.Settings.Quality != "720p" || state.Settings.Mode != "dub" || len(state.Saved) != 1 || state.Saved[0].ID != anime.ID {
		t.Fatalf("state did not survive reopening: %+v", state)
	}
	if _, err := reopened.SaveAnime(anime, false); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot().Saved) != 0 {
		t.Fatal("removing a saved anime failed")
	}
}
func TestServiceRejectsInvalidSettingsAndPlayerCommands(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	settings := service.Snapshot().Settings
	settings.Quality = "fake-quality"
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("accepted unsupported quality")
	}
	if service.Snapshot().Settings.Quality != "best" {
		t.Fatal("failed save changed preferences")
	}
	if service.Snapshot().Player.Speed != 1 {
		t.Fatalf("default player speed = %v, want 1", service.Snapshot().Player.Speed)
	}
	if err := service.PlayerCommand("pause", 0); err == nil {
		t.Fatal("accepted playback control without player")
	}
	if err := service.OpenDownload("missing"); err == nil {
		t.Fatal("opened unavailable download")
	}
}

func TestServiceConcurrentDuplicateDownloadHasOneJob(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	service.downloads.resolve = func(ctx context.Context, _ Anime, _ Episode, _ Settings) (Stream, error) {
		<-ctx.Done()
		return Stream{}, ctx.Err()
	}
	anime := Anime{ID: "same-anime", Title: "One episode", URL: "https://anidb.app/anime/1", Source: "anidb"}
	episode := Episode{Number: "1", URL: "https://anidb.app/episode/1"}
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { _, err := service.DownloadEpisode(anime, episode); results <- err }()
	}
	success := 0
	for i := 0; i < 20; i++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 || len(service.Snapshot().Downloads) != 1 {
		t.Fatalf("concurrent requests queued %d jobs", success)
	}
}
