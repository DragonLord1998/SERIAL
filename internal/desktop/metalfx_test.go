//go:build darwin

package desktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetalFXLocalConfigUsesObjectsAndPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "captured.json")
	script := filepath.Join(dir, "capture-player")
	// This protocol fixture captures the launch document then exits; it creates
	// no window. Native decoding/rendering is verified separately on macOS.
	body := "#!/bin/sh\n/usr/bin/stat -f '%Lp' \"$2\" > \"$GOANIME_TEST_CAPTURE.mode\"\n/bin/cp \"$2\" \"$GOANIME_TEST_CAPTURE\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOANIME_TEST_CAPTURE", capture)
	media := filepath.Join(dir, "episode.mp4")
	if err := os.WriteFile(media, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	player := NewPlayer(nil)
	err := player.StartMetalFX(ctx, script, Stream{URL: media}, Anime{Title: "Anime"}, Episode{Number: "1", Title: "Actual title"}, 0, "sub")
	if err == nil {
		t.Fatal("fixture exit was reported as ready playback")
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if _, ok := config["headers"].(map[string]any); !ok {
		t.Fatal("native headers must decode as an object, even for local playback")
	}
	if _, ok := config["subtitles"].([]any); !ok {
		t.Fatal("native subtitles must decode as an array")
	}
	mode, _ := os.ReadFile(capture + ".mode")
	if string(mode) != "600\n" {
		t.Fatalf("private config mode=%q", mode)
	}
	if config["title"] != "Anime · Episode 1 · Actual title" {
		t.Fatal("episode name missing from player title")
	}
	socket := config["socket"].(string)
	if _, err := os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatal("failed startup leaked playback config directory")
	}
}

func TestMetalFXPreferenceRejectsUnknownModeAndPreservesLegacyOff(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	settings := service.Snapshot().Settings
	if settings.Upscaler != "off" {
		t.Fatal("legacy state unexpectedly enables an upscaler")
	}
	settings.Upscaler = "fake-hd"
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("unrecognized upscaler accepted")
	}
	if service.Snapshot().Settings.Upscaler != "off" {
		t.Fatal("rejected setting mutated preferences")
	}
}
