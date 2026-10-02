package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live tests are opt-in: provider outages must not break the deterministic suite.
func TestDesktopLiveCatalogAndPlayback(t *testing.T) {
	if os.Getenv("GOANIME_TEST_LIVE") != "1" {
		t.Skip("set GOANIME_TEST_LIVE=1 for real provider verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	catalog := NewCatalog()
	result, err := catalog.SearchWithStatus(ctx, "Naruto", "")
	if err != nil {
		t.Fatal(err)
	}
	var anime Anime
	for _, item := range result.Animes {
		if item.Source == "hianime" && strings.EqualFold(item.Title, "Naruto") {
			anime = item
			break
		}
	}
	if anime.ID == "" {
		t.Fatalf("real Naruto result missing: %+v", result)
	}
	episodes, err := catalog.Episodes(ctx, anime)
	if err != nil || len(episodes) < 200 {
		t.Fatalf("real episode catalog: count=%d err=%v", len(episodes), err)
	}
	t.Logf("Search: %d results, %d source warnings; Naruto: %d episodes", len(result.Animes), len(result.Warnings), len(episodes))
	for _, query := range []string{"Bleach", "One Piece"} {
		found, err := catalog.Search(ctx, query, "hianime")
		if err != nil || len(found) == 0 {
			t.Fatalf("%s search: count=%d err=%v", query, len(found), err)
		}
		t.Logf("%s: %d real results", query, len(found))
	}
	mpv := os.Getenv("GOANIME_TEST_MPV")
	if mpv == "" {
		t.Skip("live catalog verified; set GOANIME_TEST_MPV to test real playback")
	}
	for _, mode := range []string{"sub", "dub"} {
		t.Run(mode, func(t *testing.T) {
			service, err := NewService(filepath.Join(t.TempDir(), "state.json"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			service.player.extraArgs = []string{"--vo=null", "--ao=null"}
			settings := service.Snapshot().Settings
			settings.MPVPath = mpv
			settings.Mode = mode
			if _, err := service.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			if err := service.Play(anime, episodes[0], 0); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Second)
			decoded := false
			for time.Now().Before(deadline) {
				service.player.mu.Lock()
				session := service.player.session
				service.player.mu.Unlock()
				if session != nil {
					video, ve := session.exchange([]any{"get_property", "video-params"})
					audio, ae := session.exchange([]any{"get_property", "audio-params"})
					v, vok := video.(map[string]any)
					a, aok := audio.(map[string]any)
					if ve == nil && ae == nil && vok && aok && len(v) > 0 && len(a) > 0 && service.Snapshot().Player.Position > 0 {
						decoded = true
						t.Logf("%s: decoded video=%v audio=%v", mode, v, a)
						break
					}
				}
				time.Sleep(250 * time.Millisecond)
			}
			if !decoded {
				t.Fatalf("real %s video/audio did not decode: %+v", mode, service.Snapshot().Player)
			}
			if err := service.PlayerCommand("seek", 60); err != nil {
				t.Fatal(err)
			}
			time.Sleep(700 * time.Millisecond)
			if err := service.PlayerCommand("stop", 0); err != nil {
				t.Fatal(err)
			}
			history := service.Snapshot().History
			if len(history) != 1 || history[0].Position < 59 || history[0].Duration < 60 {
				t.Fatalf("real resume checkpoint missing: %+v", history)
			}
		})
	}
}
