package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Store struct {
	path     string
	defaults Settings
	mu       sync.Mutex
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("a state file path is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find download directory: %w", err)
	}
	return &Store{
		path:     filepath.Clean(path),
		defaults: Settings{Quality: "best", Mode: "sub", DownloadDir: filepath.Join(home, "Downloads", "GoAnime")},
	}, nil
}

func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s.normalize(State{}), nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read application state: %w", err)
	}
	var state *State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("application state at %s is corrupt; the file has been preserved: %w", s.path, err)
	}
	if state == nil {
		return State{}, fmt.Errorf("application state at %s is corrupt; expected an object and preserved the file", s.path)
	}
	result := s.normalize(*state)
	for i := range result.Downloads {
		switch result.Downloads[i].Status {
		case "queued", "resolving", "downloading":
			result.Downloads[i].Status = "failed"
			result.Downloads[i].Error = "Download was interrupted when the app closed. Retry this episode."
		}
	}
	return result, nil
}

func (s *Store) Save(state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(s.normalize(state), "", "  ")
	if err != nil {
		return fmt.Errorf("encode application state: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".goanime-state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return fmt.Errorf("secure state file: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("write application state: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("flush application state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close application state: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replace application state: %w", err)
	}
	return nil
}

func (s *Store) normalize(state State) State {
	if state.Settings.Quality == "" {
		state.Settings.Quality = s.defaults.Quality
	}
	if state.Settings.Mode == "" {
		state.Settings.Mode = s.defaults.Mode
	}
	if state.Settings.Upscaler == "" {
		state.Settings.Upscaler = "off"
	}
	if state.Settings.DownloadDir == "" {
		state.Settings.DownloadDir = s.defaults.DownloadDir
	}
	if state.Sources == nil {
		state.Sources = []Source{}
	}
	if state.History == nil {
		state.History = []HistoryEntry{}
	}
	if state.Saved == nil {
		state.Saved = []Anime{}
	}
	if state.Downloads == nil {
		state.Downloads = []Download{}
	}
	if state.Feedback == nil {
		state.Feedback = []AnimeFeedback{}
	}
	return state
}
