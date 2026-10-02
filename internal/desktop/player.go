package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Player owns one mpv process. Every IPC exchange has a deadline and consumes
// complete JSON messages, including messages larger than a socket read buffer.
type Player struct {
	mu        sync.Mutex
	session   *playerSession
	changed   func(PlayerState)
	extraArgs []string // Internal test seam for a headless playback smoke test.
}
type playerSession struct {
	command     *exec.Cmd
	socket      string
	directory   string
	done        chan struct{}
	ioMu        sync.Mutex
	stateMu     sync.Mutex
	state       PlayerState
	intentional bool
}

func NewPlayer(changed func(PlayerState)) *Player { return &Player{changed: changed} }

func FindMPV(configured string) (string, error) {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	} else {
		if path, err := exec.LookPath("mpv"); err == nil {
			candidates = append(candidates, path)
		}
		if executable, err := os.Executable(); err == nil {
			base := filepath.Dir(executable)
			candidates = append(candidates, filepath.Join(base, "..", "Resources", "tools", "mpv.app", "Contents", "MacOS", "mpv"), filepath.Join(base, "..", "Resources", "tools", "mpv"), filepath.Join(base, "mpv.exe"))
		}
		home, _ := os.UserHomeDir()
		candidates = append(candidates, "/Applications/mpv.app/Contents/MacOS/mpv", filepath.Join(home, "Applications", "mpv.app", "Contents", "MacOS", "mpv"), "/opt/homebrew/bin/mpv", "/usr/local/bin/mpv")
	}
	for _, candidate := range candidates {
		candidate, _ = filepath.Abs(candidate)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
			return candidate, nil
		}
	}
	return "", errors.New("mpv could not be found. Select the mpv executable in Settings")
}

func (p *Player) Start(ctx context.Context, executable string, stream Stream, anime Anime, episode Episode, start float64, mode string) error {
	if !validNumber(start) || start < 0 {
		return errors.New("invalid resume position")
	}
	if err := validateMediaTarget(stream.URL); err != nil {
		return err
	}
	if err := p.Stop(); err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "goanime-player-")
	if err != nil {
		return err
	}
	socket := filepath.Join(directory, "ipc")
	if runtime.GOOS == "windows" {
		socket = fmt.Sprintf("\\\\.\\pipe\\goanime_gui_%d", time.Now().UnixNano())
	}
	args := []string{"--no-config", "--no-terminal", "--force-window=yes", "--input-ipc-server=" + socket, "--force-media-title=" + anime.Title + " · Episode " + episode.Number, "--start=" + strconv.FormatFloat(start, 'f', 3, 64)}
	if mode == "dub" {
		args = append(args, "--alang=eng,en")
	} else {
		args = append(args, "--alang=jpn,ja", "--slang=eng,en")
	}
	for key, value := range stream.Headers {
		if strings.ContainsAny(key+value, "\r\n\x00") {
			os.RemoveAll(directory)
			return errors.New("invalid stream header")
		}
		switch strings.ToLower(key) {
		case "user-agent":
			args = append(args, "--user-agent="+value)
		case "referer":
			args = append(args, "--referrer="+value)
		default:
			args = append(args, "--http-header-fields-append="+key+": "+value)
		}
	}
	for _, subtitle := range stream.Subtitles {
		if err := validateMediaURL(subtitle.URL); err != nil {
			os.RemoveAll(directory)
			return err
		}
		args = append(args, "--sub-file="+subtitle.URL)
	}
	args = append(args, p.extraArgs...)
	args = append(args, "--", stream.URL)
	return p.startProcess(ctx, executable, args, anime, episode, start, "mpv", directory, socket)
}

// The native player implements the IPC commands used by this lifecycle.
func (p *Player) startProcess(ctx context.Context, executable string, args []string, anime Anime, episode Episode, start float64, renderer, directory, socket string) error {
	playerName := "mpv"
	if renderer == "metalfx" {
		playerName = "MetalFX player"
	}
	command := exec.Command(executable, args...)
	command.Stdout = io.Discard
	stderr := &synchronizedBuffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		os.RemoveAll(directory)
		return fmt.Errorf("could not open %s: %w", playerName, err)
	}
	session := &playerSession{command: command, socket: socket, directory: directory, done: make(chan struct{}), state: PlayerState{Active: true, Volume: 100, Speed: 1, Position: start, Anime: &anime, Episode: &episode, Renderer: renderer}}
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()
	go func() {
		err := command.Wait()
		session.stateMu.Lock()
		session.state.Active = false
		if err != nil && !session.intentional {
			session.state.Error = playerName + " stopped: " + strings.TrimSpace(stderr.String())
			if session.state.Error == playerName+" stopped: " {
				session.state.Error = err.Error()
			}
		}
		session.stateMu.Unlock()
		os.RemoveAll(directory)
		close(session.done)
		p.notify(session)
	}()
	ready := time.NewTicker(50 * time.Millisecond)
	defer ready.Stop()
	readyTimeout := 12 * time.Second
	if renderer == "metalfx" {
		readyTimeout = 30 * time.Second
	}
	timeout := time.NewTimer(readyTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			p.Stop()
			return ctx.Err()
		case <-session.done:
			return errors.New(playerName + " closed before playback started: " + stderr.String())
		case <-timeout.C:
			p.Stop()
			return fmt.Errorf("%s did not become ready within %s", playerName, readyTimeout)
		case <-ready.C:
			if _, err := session.exchange([]any{"get_property", "mpv-version"}); err == nil {
				p.notify(session)
				go p.monitor(session)
				return nil
			}
		}
	}
}

func (p *Player) monitor(session *playerSession) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-session.done:
			return
		case <-ticker.C:
			position, posErr := session.exchange([]any{"get_property", "time-pos"})
			duration, _ := session.exchange([]any{"get_property", "duration"})
			paused, _ := session.exchange([]any{"get_property", "pause"})
			volume, _ := session.exchange([]any{"get_property", "volume"})
			speed, _ := session.exchange([]any{"get_property", "speed"})
			var scale map[string]any
			if session.state.Renderer == "metalfx" {
				value, _ := session.exchange([]any{"get_property", "metalfx-status"})
				scale, _ = value.(map[string]any)
			}
			session.stateMu.Lock()
			if value, ok := position.(float64); ok && posErr == nil {
				session.state.Position = value
			}
			if value, ok := duration.(float64); ok {
				session.state.Duration = value
			}
			if value, ok := paused.(bool); ok {
				session.state.Paused = value
			}
			if value, ok := volume.(float64); ok {
				session.state.Volume = value
			}
			if value, ok := speed.(float64); ok && validPlaybackSpeed(value) {
				session.state.Speed = value
			}
			if session.state.Speed == 0 {
				session.state.Speed = 1
			}
			if scale != nil {
				session.state.Upscaling, _ = scale["upscaling"].(bool)
				if value, ok := scale["videoWidth"].(float64); ok {
					session.state.VideoWidth = int(value)
				}
				if value, ok := scale["videoHeight"].(float64); ok {
					session.state.VideoHeight = int(value)
				}
				if value, ok := scale["outputWidth"].(float64); ok {
					session.state.OutputWidth = int(value)
				}
				if value, ok := scale["outputHeight"].(float64); ok {
					session.state.OutputHeight = int(value)
				}
			}
			session.stateMu.Unlock()
			p.notify(session)
		}
	}
}
func (p *Player) notify(session *playerSession) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != session || p.changed == nil {
		return
	}
	session.stateMu.Lock()
	state := session.state
	session.stateMu.Unlock()
	// Hold the session lock through delivery so an exiting previous process
	// cannot overwrite state published by a newly started process.
	p.changed(state)
}
func (s *playerSession) exchange(command []any) (any, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	connection, err := dialPlayer(ctx, s.socket)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if err := json.NewEncoder(connection).Encode(map[string]any{"command": command, "request_id": 1}); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(io.LimitReader(connection, 1024*1024))
	for {
		var message map[string]any
		if err := decoder.Decode(&message); err != nil {
			return nil, err
		}
		if id, ok := message["request_id"].(float64); !ok || id != 1 {
			continue
		}
		if result, _ := message["error"].(string); result != "success" {
			return nil, fmt.Errorf("mpv: %s", result)
		}
		return message["data"], nil
	}
}
func (p *Player) Command(command string, value float64) error {
	if command == "stop" {
		return p.Stop()
	}
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == nil {
		return errors.New("no episode is playing")
	}
	session.stateMu.Lock()
	active := session.state.Active
	session.stateMu.Unlock()
	if !active {
		return errors.New("no episode is playing")
	}
	var request []any
	switch command {
	case "pause":
		request = []any{"cycle", "pause"}
	case "seek":
		if !validNumber(value) || value < 0 {
			return errors.New("invalid seek position")
		}
		request = []any{"seek", value, "absolute"}
	case "volume":
		if !validNumber(value) || value < 0 || value > 100 {
			return errors.New("volume must be between 0 and 100")
		}
		request = []any{"set_property", "volume", value}
	case "speed":
		if !validPlaybackSpeed(value) {
			return errors.New("speed must be between 0.5 and 2")
		}
		request = []any{"set_property", "speed", value}
	default:
		return errors.New("unknown playback control")
	}
	_, err := session.exchange(request)
	return err
}
func (p *Player) Stop() error {
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == nil {
		return nil
	}
	select {
	case <-session.done:
		return nil
	default:
	}
	// Capture final position before quitting, including a seek made immediately
	// before Stop. This is the durable resume checkpoint.
	position, err := session.exchange([]any{"get_property", "time-pos"})
	session.stateMu.Lock()
	session.intentional = true
	if value, ok := position.(float64); ok && err == nil {
		session.state.Position = value
	}
	session.stateMu.Unlock()
	session.exchange([]any{"quit"})
	select {
	case <-session.done:
	case <-time.After(2 * time.Second):
		session.command.Process.Kill()
		<-session.done
	}
	p.notify(session)
	return nil
}
func validNumber(value float64) bool        { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func validPlaybackSpeed(value float64) bool { return validNumber(value) && value >= 0.5 && value <= 2 }
func validateMediaURL(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(target, "\x00\r\n") {
		return errors.New("the provider did not return a playable HTTP video URL")
	}
	return nil
}
func validateMediaTarget(target string) error {
	if filepath.IsAbs(target) {
		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			return errors.New("the downloaded file is unavailable")
		}
		return nil
	}
	return validateMediaURL(target)
}

type synchronizedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data) < 16384 {
		remaining := 16384 - len(b.data)
		b.data = append(b.data, data[:min(len(data), remaining)]...)
	}
	return len(data), nil
}
func (b *synchronizedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
