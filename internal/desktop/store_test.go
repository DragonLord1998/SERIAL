package desktop

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if state.Settings.Quality != "best" || state.Settings.Mode != "sub" || state.Settings.DownloadDir != filepath.Join(home, "Downloads", "GoAnime") {
		t.Fatalf("defaults: %#v", state.Settings)
	}
	if state.Sources == nil || state.History == nil || state.Saved == nil || state.Downloads == nil || state.Feedback == nil {
		t.Fatal("default collections must encode as arrays")
	}
	state.Settings.Quality, state.Settings.Mode = "720p", "dub"
	state.Saved = []Anime{{ID: "a", Title: "Saved show", Source: "anidb", URL: "https://anidb.app/anime/show-1"}}
	state.History = []HistoryEntry{{Anime: state.Saved[0], Episode: Episode{Number: "2"}, Position: 123.5, Duration: 1400}}
	state.Downloads = []Download{{ID: "d", Status: "completed", Path: "/video/episode.mp4"}}
	state.Feedback = []AnimeFeedback{{Anime: state.Saved[0], Value: "like", UpdatedAt: "2026-10-02T08:00:00Z"}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load()
	if err != nil || !reflect.DeepEqual(reloaded, state) {
		t.Fatalf("round trip: %#v err=%v", reloaded, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v err=%v", info, err)
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v err=%v", info, err)
	}
	state.Saved[0].Title = "Updated"
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	reloaded, err = store.Load()
	if err != nil || reloaded.Saved[0].Title != "Updated" {
		t.Fatalf("atomic replacement failed: %#v err=%v", reloaded, err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".goanime-state-*"))
	if len(files) != 0 {
		t.Fatalf("temporary state files leaked: %v", files)
	}
}

func TestStoreInterruptedDownloads(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state := State{Downloads: []Download{
		{ID: "queued", Status: "queued"}, {ID: "resolving", Status: "resolving"},
		{ID: "downloading", Status: "downloading", Bytes: 42, Progress: .25},
		{ID: "completed", Status: "completed"}, {ID: "failed", Status: "failed", Error: "original failure"},
	}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, download := range reloaded.Downloads[:3] {
		if download.Status != "failed" || !strings.Contains(download.Error, "Retry this episode") {
			t.Fatalf("interrupted state appears live: %#v", download)
		}
	}
	if reloaded.Downloads[2].Bytes != 42 || reloaded.Downloads[2].Progress != .25 || reloaded.Downloads[3].Status != "completed" || reloaded.Downloads[4].Error != "original failure" {
		t.Fatalf("unrelated download data altered: %#v", reloaded.Downloads)
	}
}

func TestStoreCorruptionPreserved(t *testing.T) {
	for _, data := range []string{"not JSON", "null", "[]", `{"saved":`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "corrupt") {
				t.Fatalf("corruption not surfaced: %v", err)
			}
			preserved, err := os.ReadFile(path)
			if err != nil || string(preserved) != data {
				t.Fatalf("corrupt state overwritten: %q err=%v", preserved, err)
			}
		})
	}
}

func TestStoreFailuresPreservePreviousState(t *testing.T) {
	if _, err := NewStore(" "); err == nil {
		t.Fatal("empty path accepted")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(State{Saved: []Anime{{Title: "Keep me"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(State{Saved: []Anime{{Score: math.NaN()}}}); err == nil {
		t.Fatal("unencodable state reported saved")
	}
	state, err := store.Load()
	if err != nil || len(state.Saved) != 1 || state.Saved[0].Title != "Keep me" {
		t.Fatalf("failed save damaged existing state: %#v err=%v", state, err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(filepath.Join(blocked, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(State{}); err == nil {
		t.Fatal("unwritable state directory reported saved")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("read failure hidden as a fresh default state")
	}
}
