package desktop

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testDownloader(t *testing.T, server *httptest.Server, changed chan Download) *Downloader {
	t.Helper()
	d := NewDownloader(func(context.Context, Anime, Episode, Settings) (Stream, error) {
		return Stream{URL: server.URL, Headers: map[string]string{"Referer": "https://fixture.example/"}}, nil
	}, func(item Download) { changed <- item })
	d.client = server.Client()
	d.ffmpeg = func() string { return "" }
	t.Cleanup(d.Close)
	return d
}

func terminalDownload(t *testing.T, changed <-chan Download, id string) Download {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case item := <-changed:
			if item.ID == id && (item.Status == "completed" || item.Status == "failed" || item.Status == "cancelled") {
				return item
			}
		case <-timeout.C:
			t.Fatal("download did not reach a terminal state")
			return Download{}
		}
	}
}

func fixtureVideo() []byte {
	return append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, bytes.Repeat([]byte{1}, 2048)...)
}

func TestDownloadDirectComplete(t *testing.T) {
	media := fixtureVideo()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://fixture.example/" {
			t.Error("source headers were not propagated")
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(media)
	}))
	defer server.Close()
	updates := make(chan Download, 64)
	d := testDownloader(t, server, updates)
	dir := t.TempDir()
	initial, err := d.Start(Anime{Title: "../ My: Anime?"}, Episode{Number: "1"}, Settings{DownloadDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Status != "queued" {
		t.Fatalf("initial status %s", initial.Status)
	}
	item := terminalDownload(t, updates, initial.ID)
	if item.Status != "completed" || item.Progress != 100 || item.Bytes != int64(len(media)) || item.TotalBytes != int64(len(media)) {
		t.Fatalf("unexpected download: %+v", item)
	}
	got, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, media) {
		t.Fatal("downloaded bytes differ")
	}
	if filepath.Dir(item.Path) != dir {
		t.Fatal("unsafe title escaped destination")
	}
	if _, err := os.Stat(item.Path + ".part"); !os.IsNotExist(err) {
		t.Fatal("partial file remains after completion")
	}
}

func TestDownloadCancelCleansPartialAndQueuedJob(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(fixtureVideo())
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	updates := make(chan Download, 64)
	d := testDownloader(t, server, updates)
	dir := t.TempDir()
	first, err := d.Start(Anime{Title: "A"}, Episode{Number: "1"}, Settings{DownloadDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	second, err := d.Start(Anime{Title: "A"}, Episode{Number: "2"}, Settings{DownloadDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	firstResult := terminalDownload(t, updates, first.ID)
	secondResult := terminalDownload(t, updates, second.ID)
	if firstResult.Status != "cancelled" || secondResult.Status != "cancelled" {
		t.Fatalf("statuses %s %s", firstResult.Status, secondResult.Status)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("cancelled downloads retained files: %v", files)
	}
	if requests.Load() != 1 {
		t.Fatal("cancelled queued job made a network request")
	}
}

func TestDownloadRejectsNonMediaResponses(t *testing.T) {
	for _, fixture := range []struct{ kind, body string }{{"application/json", `{"error":"expired"}`}, {"text/html", "<!doctype html><html>Access denied</html>"}, {"video/mp4", `{"error":"expired"}`}, {"video/mp4", ""}} {
		t.Run(fixture.kind+fmt.Sprint(len(fixture.body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", fixture.kind)
				fmt.Fprint(w, fixture.body)
			}))
			defer server.Close()
			updates := make(chan Download, 64)
			d := testDownloader(t, server, updates)
			dir := t.TempDir()
			initial, err := d.Start(Anime{Title: "A"}, Episode{Number: "1"}, Settings{DownloadDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			item := terminalDownload(t, updates, initial.ID)
			if item.Status != "failed" {
				t.Fatalf("error page became %s", item.Status)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("error response produced a file")
			}
		})
	}
}

func TestDownloadHLSMissingSegmentFails(t *testing.T) {
	var playlist strings.Builder
	playlist.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:1\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&playlist, "#EXTINF:1,\n/segment-%d.ts\n", i)
	}
	playlist.WriteString("#EXT-X-ENDLIST\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, playlist.String())
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		if r.URL.Path == "/segment-3.ts" {
			return
		}
		segment := bytes.Repeat([]byte{0x47}, 188)
		w.Write(segment)
	}))
	defer server.Close()
	updates := make(chan Download, 128)
	d := testDownloader(t, server, updates)
	dir := t.TempDir()
	initial, err := d.Start(Anime{Title: "A"}, Episode{Number: "1"}, Settings{DownloadDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	item := terminalDownload(t, updates, initial.ID)
	if item.Status != "failed" || !strings.Contains(item.Error, "incomplete HLS") {
		t.Fatalf("missing HLS segment was accepted: %+v", item)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("partial HLS file remains")
	}
}

func TestDownloadNativeHLSComplete(t *testing.T) {
	segment := bytes.Repeat([]byte{0x47}, 376)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// An extensionless URL with a generic MIME type must still be HLS.
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n/segment.ts\n#EXTINF:1,\n/segment.ts\n#EXT-X-ENDLIST\n")
			return
		}
		if r.URL.Path != "/segment.ts" {
			t.Errorf("incorrect root-relative segment URL: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(segment)
	}))
	defer server.Close()
	updates := make(chan Download, 64)
	d := testDownloader(t, server, updates)
	initial, err := d.Start(Anime{Title: "A"}, Episode{Number: "1"}, Settings{DownloadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	item := terminalDownload(t, updates, initial.ID)
	if item.Status != "completed" || item.Bytes != int64(2*len(segment)) || !strings.HasSuffix(item.Path, ".ts") {
		t.Fatalf("valid HLS download failed: %+v", item)
	}
	got, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(append([]byte{}, segment...), segment...)) {
		t.Fatal("HLS media bytes changed")
	}
}

func TestDownloadDirectTruncationFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "99999")
		w.Write(fixtureVideo())
	}))
	defer server.Close()
	updates := make(chan Download, 64)
	d := testDownloader(t, server, updates)
	dir := t.TempDir()
	initial, err := d.Start(Anime{Title: "A"}, Episode{Number: "1"}, Settings{DownloadDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	item := terminalDownload(t, updates, initial.ID)
	if item.Status != "failed" {
		t.Fatalf("truncated direct media accepted: %+v", item)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("truncated download retained a file")
	}
}

func TestDownloadRejectsUnsupportedNativeHLS(t *testing.T) {
	for name, extra := range map[string]string{"encrypted": "#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n", "fragmented": "#EXT-X-MAP:URI=\"init.mp4\"\n", "live": ""} {
		t.Run(name, func(t *testing.T) {
			end := "#EXT-X-ENDLIST\n"
			if name == "live" {
				end = ""
			}
			body := "#EXTM3U\n" + extra + "#EXTINF:1,\nsegment.ts\n" + end
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			updates := make(chan Download, 64)
			d := testDownloader(t, server, updates)
			dir := t.TempDir()
			initial, err := d.Start(Anime{Title: "A"}, Episode{Number: "1"}, Settings{DownloadDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			item := terminalDownload(t, updates, initial.ID)
			if item.Status != "failed" || !strings.Contains(item.Error, "require ffmpeg") {
				t.Fatalf("unsupported HLS accepted: %+v", item)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("unsupported HLS left a file")
			}
		})
	}
}
