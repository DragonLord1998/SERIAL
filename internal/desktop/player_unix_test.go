//go:build !windows

package desktop

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlayerIPCHandlesFragmentedLargeMessages(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ga-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	expected := strings.Repeat("large metadata ", 1000)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var request map[string]any
		json.NewDecoder(connection).Decode(&request)
		io.WriteString(connection, "{\"event\":\"property-change\",\"data\":1}\n")
		data, _ := json.Marshal(map[string]any{"request_id": 1, "error": "success", "data": expected})
		for len(data) > 0 {
			n := min(len(data), 317)
			connection.Write(data[:n])
			data = data[n:]
		}
		io.WriteString(connection, "\n")
	}()
	session := &playerSession{socket: path}
	got, err := session.exchange([]any{"get_property", "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatal("truncated or incorrect JSON IPC response")
	}
}
func TestPlayerIPCReadTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ga-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		time.Sleep(2 * time.Second)
	}()
	started := time.Now()
	_, err = (&playerSession{socket: path}).exchange([]any{"get_property", "duration"})
	if err == nil {
		t.Fatal("missing IPC response was accepted")
	}
	if time.Since(started) > 1900*time.Millisecond {
		t.Fatal("IPC did not respect its read deadline")
	}
}

func TestPlayerSpeedCommandValidationAndIPC(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ga-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []any, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var request map[string]any
		if err := json.NewDecoder(connection).Decode(&request); err != nil {
			return
		}
		if command, ok := request["command"].([]any); ok {
			commands <- command
		}
		io.WriteString(connection, `{"request_id":1,"error":"success","data":null}`+"\n")
	}()
	player := NewPlayer(nil)
	player.session = &playerSession{socket: path, done: make(chan struct{}), state: PlayerState{Active: true, Speed: 1}}
	if err := player.Command("speed", 1.25); err != nil {
		t.Fatal(err)
	}
	select {
	case command := <-commands:
		if len(command) != 3 || command[0] != "set_property" || command[1] != "speed" || command[2] != 1.25 {
			t.Fatalf("unexpected speed IPC command: %#v", command)
		}
	case <-time.After(time.Second):
		t.Fatal("speed command did not reach IPC server")
	}
	for _, value := range []float64{0, 0.49, 2.01, math.Inf(1), math.NaN()} {
		if err := player.Command("speed", value); err == nil {
			t.Fatalf("accepted invalid speed %v", value)
		}
	}
}

func TestPlaybackAndDurableResumeSmoke(t *testing.T) {
	mpv := os.Getenv("GOANIME_TEST_MPV")
	media := os.Getenv("GOANIME_TEST_MEDIA")
	if mpv == "" || media == "" {
		t.Skip("set GOANIME_TEST_MPV and GOANIME_TEST_MEDIA for real player smoke")
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(statePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.player.extraArgs = []string{"--vo=null", "--ao=null"}
	anime := Anime{ID: "local-smoke", Title: "Local playback smoke", URL: "https://example.com/test", Source: "anidb"}
	episode := Episode{Number: "1", URL: "https://example.com/ep/1"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	headers := make(chan http.Header, 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case headers <- r.Header.Clone():
		default:
		}
		http.ServeFile(w, r, media)
	}))
	defer server.Close()
	stream := Stream{URL: server.URL + "/test.mp4", Headers: map[string]string{"User-Agent": "GoAnime-Playback-Test", "Referer": "https://anidb.app/", "Origin": "https://anidb.app"}}
	if err := service.player.Start(ctx, mpv, stream, anime, episode, 0, "sub"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	select {
	case header := <-headers:
		if header.Get("User-Agent") != "GoAnime-Playback-Test" || len(header.Values("User-Agent")) != 1 || header.Get("Referer") != "https://anidb.app/" || header.Get("Origin") != "https://anidb.app" {
			t.Fatalf("stream headers were not preserved: %v", header)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mpv did not request the test video")
	}
	if err := service.PlayerCommand("seek", 8); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if err := service.PlayerCommand("stop", 0); err != nil {
		t.Fatal(err)
	}
	service.Close()
	reopened, err := NewService(statePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history := reopened.Snapshot().History
	if len(history) != 1 || history[0].Position < 8 || history[0].Duration <= 8 {
		t.Fatalf("resume checkpoint not durable: %+v", history)
	}
	reopened.player.extraArgs = []string{"--vo=null", "--ao=null"}
	if err := reopened.player.Start(ctx, mpv, stream, history[0].Anime, history[0].Episode, history[0].Position, "sub"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if err := reopened.PlayerCommand("stop", 0); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().History[0].Position; got < 8 {
		t.Fatalf("reopened player lost resume position: %v", got)
	}
}

func TestPlayerSessionDeliveryIsOrderedAcrossReplacement(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	replaced := make(chan struct{})
	states := make(chan bool, 2)
	player := NewPlayer(func(state PlayerState) {
		if !state.Active {
			close(entered)
			<-release
		}
		states <- state.Active
	})
	previous := &playerSession{state: PlayerState{Active: false}}
	current := &playerSession{state: PlayerState{Active: true}}
	player.session = previous
	go player.notify(previous)
	<-entered
	go func() {
		player.mu.Lock()
		player.session = current
		player.mu.Unlock()
		player.notify(current)
		close(replaced)
	}()
	select {
	case <-replaced:
		t.Fatal("new session published before the previous session finished delivery")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-replaced
	if <-states != false || <-states != true {
		t.Fatal("old session overwrote new playback state")
	}
}
