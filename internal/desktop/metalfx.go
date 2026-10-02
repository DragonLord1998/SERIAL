package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type metalFXSubtitle struct {
	URL      string `json:"url"`
	Language string `json:"language"`
	Label    string `json:"label"`
}
type metalFXConfig struct {
	Socket    string            `json:"socket"`
	Title     string            `json:"title"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Subtitles []metalFXSubtitle `json:"subtitles"`
	Start     float64           `json:"start"`
	Mode      string            `json:"mode"`
	Scale     int               `json:"scale"`
}

func metalFXCapabilities() (string, bool, string, string) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return "", false, "", "MetalFX playback requires an Apple Silicon Mac."
	}
	path := os.Getenv("GOANIME_METALFX_PLAYER")
	if path == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", false, "", "Could not locate the native MetalFX player."
		}
		path = filepath.Join(filepath.Dir(executable), "..", "Resources", "tools", "GoAnimeMetalFX.app", "Contents", "MacOS", "GoAnimeMetalFX")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", false, "", "Could not locate the native MetalFX player."
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return "", false, "", "The native MetalFX player is not included in this build."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, "--capabilities").Output()
	if err != nil || len(data) > 16384 {
		return "", false, "", "Could not verify MetalFX support on this Mac."
	}
	var result struct {
		Available bool   `json:"available"`
		Device    string `json:"device"`
		Reason    string `json:"reason"`
	}
	if json.Unmarshal(data, &result) != nil {
		return "", false, "", "The native MetalFX player returned invalid capabilities."
	}
	if !result.Available {
		if result.Reason == "" {
			result.Reason = "MetalFX is unavailable on this Mac."
		}
		return "", false, result.Device, result.Reason
	}
	return path, true, result.Device, ""
}

func (s *Service) playbackExecutable(settings Settings) (string, error) {
	if settings.Upscaler == "metalfx" {
		if !s.state.MetalFXAvailable || s.metalFXExecutable == "" {
			reason := s.state.MetalFXReason
			if reason == "" {
				reason = "MetalFX is unavailable in this build."
			}
			return "", errors.New(reason)
		}
		return s.metalFXExecutable, nil
	}
	return FindMPV(settings.MPVPath)
}

func (p *Player) StartMetalFX(ctx context.Context, executable string, stream Stream, anime Anime, episode Episode, start float64, mode string) error {
	if !validNumber(start) || start < 0 {
		return errors.New("invalid resume position")
	}
	if mode != "sub" && mode != "dub" {
		return errors.New("choose sub or dub")
	}
	if err := validateMediaTarget(stream.URL); err != nil {
		return err
	}
	for key, value := range stream.Headers {
		if strings.ContainsAny(key+value, "\r\n\x00") {
			return errors.New("invalid stream header")
		}
	}
	subtitles := []metalFXSubtitle{}
	for _, subtitle := range stream.Subtitles {
		if err := validateMediaURL(subtitle.URL); err != nil {
			return err
		}
		subtitles = append(subtitles, metalFXSubtitle{subtitle.URL, subtitle.Language, subtitle.Label})
	}
	if err := p.Stop(); err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "goanime-metal-")
	if err != nil {
		return err
	}
	socket := filepath.Join(directory, "ipc")
	title := anime.Title + " · Episode " + episode.Number
	if episode.Title != "" && !strings.EqualFold(episode.Title, "Episode "+episode.Number) {
		title += " · " + episode.Title
	}
	headers := map[string]string{}
	for key, value := range stream.Headers {
		headers[key] = value
	}
	config := metalFXConfig{Socket: socket, Title: title, URL: stream.URL, Headers: headers, Subtitles: subtitles, Start: start, Mode: mode, Scale: 2}
	data, err := json.Marshal(config)
	if err != nil {
		os.RemoveAll(directory)
		return err
	}
	configPath := filepath.Join(directory, "playback.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		os.RemoveAll(directory)
		return fmt.Errorf("prepare MetalFX playback: %w", err)
	}
	return p.startProcess(ctx, executable, []string{"--config", configPath}, anime, episode, start, "metalfx", directory, socket)
}
