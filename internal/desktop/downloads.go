package desktop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/alvarorichard/Goanime/internal/downloader/hls"
	"github.com/alvarorichard/Goanime/internal/util"
)

// Resolver obtains a fresh, source-specific URL for each download.
type Resolver func(context.Context, Anime, Episode, Settings) (Stream, error)

type downloadJob struct {
	item     Download
	settings Settings
	ctx      context.Context
	cancel   context.CancelFunc
	ready    chan struct{}
}

// Downloader owns a FIFO queue with one active download. Its callback receives
// immutable snapshots; persistence belongs to the application.
type Downloader struct {
	mu      sync.Mutex
	jobs    map[string]*downloadJob
	queue   []*downloadJob
	closed  bool
	wake    chan struct{}
	done    chan struct{}
	resolve Resolver
	changed func(Download)
	client  *http.Client  // Injectable before Start for HTTP fixture tests.
	ffmpeg  func() string // Injectable before Start; an empty result disables it.
}

func NewDownloader(resolve Resolver, changed func(Download)) *Downloader {
	d := &Downloader{
		jobs: make(map[string]*downloadJob), wake: make(chan struct{}, 1), done: make(chan struct{}),
		resolve: resolve, changed: changed, ffmpeg: findFFmpeg,
	}
	go d.work()
	return d
}

func (d *Downloader) Start(anime Anime, ep Episode, settings Settings) (Download, error) {
	if strings.TrimSpace(settings.DownloadDir) == "" {
		return Download{}, errors.New("choose a download folder in Settings")
	}
	if d.resolve == nil {
		return Download{}, errors.New("stream resolver is unavailable")
	}
	id, err := downloadID()
	if err != nil {
		return Download{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &downloadJob{item: Download{ID: id, Anime: anime, Episode: ep, Status: "queued"}, settings: settings, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		cancel()
		return Download{}, errors.New("download queue is closed")
	}
	d.jobs[id] = j
	d.queue = append(d.queue, j)
	d.mu.Unlock()
	initial := j.item
	d.emit(initial)
	close(j.ready) // A worker never emits resolving before queued is delivered.
	d.signal()
	return initial, nil
}

func (d *Downloader) Cancel(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	j, ok := d.jobs[id]
	if !ok {
		return errors.New("download is no longer active")
	}
	j.cancel()
	return nil
}

func (d *Downloader) Close() {
	d.mu.Lock()
	d.closed = true
	for _, j := range d.jobs {
		j.cancel()
	}
	d.mu.Unlock()
	d.signal()
	<-d.done
}

func (d *Downloader) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
func (d *Downloader) emit(item Download) {
	if d.changed != nil {
		d.changed(item)
	}
}

func (d *Downloader) work() {
	defer close(d.done)
	for {
		d.mu.Lock()
		if len(d.queue) == 0 {
			closed := d.closed
			d.mu.Unlock()
			if closed {
				return
			}
			<-d.wake
			continue
		}
		j := d.queue[0]
		d.queue = d.queue[1:]
		d.mu.Unlock()
		<-j.ready
		err := d.run(j)
		if err != nil {
			j.item.Status, j.item.Error = "failed", err.Error()
			if j.ctx.Err() != nil {
				j.item.Status, j.item.Error = "cancelled", ""
			}
		} else {
			j.item.Status, j.item.Progress = "completed", 100
		}
		j.cancel()
		d.mu.Lock()
		delete(d.jobs, j.item.ID)
		d.mu.Unlock()
		d.emit(j.item)
	}
}

func (d *Downloader) httpClient() *http.Client {
	if d.client != nil {
		return d.client
	}
	return util.GetDownloadClient()
}

func (d *Downloader) run(j *downloadJob) (err error) {
	if err := j.ctx.Err(); err != nil {
		return err
	}
	j.item.Status = "resolving"
	d.emit(j.item)
	stream, err := d.resolve(j.ctx, j.item.Anime, j.item.Episode, j.settings)
	if err != nil {
		return err
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	resp, reader, isHLS, err := d.openMedia(j.ctx, stream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ffmpegPath := ""
	ext := directExtension(resp, stream.URL)
	var playlist []byte
	if isHLS {
		playlist, err = readPlaylist(reader)
		if err != nil {
			return err
		}
		if d.ffmpeg != nil {
			ffmpegPath = d.ffmpeg()
		}
		if ffmpegPath == "" {
			ext = ".ts"
		} else {
			ext = ".mp4"
		}
	}
	if err := os.MkdirAll(j.settings.DownloadDir, 0o700); err != nil {
		return err
	}
	name := safeDownloadName(j.item.Anime.Title) + " - Episode " + safeDownloadName(j.item.Episode.Number) + " - " + j.item.ID[:12] + ext
	finalPath := filepath.Join(j.settings.DownloadDir, name)
	partPath := finalPath + ".part"
	if _, err := os.Stat(finalPath); err == nil {
		return errors.New("download destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(partPath)
		}
	}()
	j.item.Status, j.item.Path = "downloading", finalPath
	d.emit(j.item)
	if !isHLS {
		err = d.copyDirect(j, file, reader, resp.ContentLength)
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	} else {
		file.Close()
		resp.Body.Close()
		if ffmpegPath != "" {
			err = d.downloadFFmpeg(j, stream, ffmpegPath, partPath, playlistDuration(playlist))
		} else {
			err = d.downloadNativeHLS(j, stream, playlist, partPath)
		}
	}
	if err != nil {
		return err
	}
	if err := j.ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(partPath)
	if err != nil {
		return err
	}
	if info.Size() <= 0 {
		return errors.New("download produced an empty file")
	}
	if _, err := os.Stat(finalPath); err == nil {
		return errors.New("download destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		return err
	}
	j.item.Bytes, j.item.TotalBytes = info.Size(), info.Size()
	return nil
}

func (d *Downloader) openMedia(ctx context.Context, stream Stream) (*http.Response, *bufio.Reader, bool, error) {
	u, err := url.Parse(stream.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil, false, errors.New("stream URL is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stream.URL, nil)
	if err != nil {
		return nil, nil, false, err
	}
	for k, v := range stream.Headers {
		req.Header.Set(k, v)
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return nil, nil, false, err
	}
	fail := func(err error) (*http.Response, *bufio.Reader, bool, error) {
		resp.Body.Close()
		return nil, nil, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("stream returned HTTP %d", resp.StatusCode))
	}
	reader := bufio.NewReader(resp.Body)
	prefix, err := reader.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return fail(err)
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(prefix, []byte{0xef, 0xbb, 0xbf}))
	if bytes.HasPrefix(trimmed, []byte("#EXTM3U")) {
		return resp, reader, true, nil
	}
	ct := strings.ToLower(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if len(trimmed) == 0 {
		return fail(errors.New("stream returned an empty response"))
	}
	if ct == "application/json" || strings.Contains(ct, "html") || ct == "text/plain" || trimmed[0] == '<' || trimmed[0] == '{' || trimmed[0] == '[' {
		return fail(errors.New("source returned a page or error response instead of media"))
	}
	detected := http.DetectContentType(prefix)
	isVideo := strings.HasPrefix(ct, "video/") || strings.HasPrefix(detected, "video/") || (len(prefix) >= 8 && string(prefix[4:8]) == "ftyp") || (len(prefix) > 188 && prefix[0] == 0x47 && prefix[188] == 0x47) || (len(prefix) >= 4 && bytes.Equal(prefix[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}))
	if !isVideo {
		return fail(errors.New("source response could not be recognized as video or HLS"))
	}
	return resp, reader, false, nil
}

func directExtension(resp *http.Response, rawURL string) string {
	u, _ := url.Parse(rawURL)
	if u != nil {
		switch strings.ToLower(filepath.Ext(u.Path)) {
		case ".mkv":
			return ".mkv"
		case ".webm":
			return ".webm"
		case ".ts":
			return ".ts"
		}
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "webm") {
		return ".webm"
	}
	return ".mp4"
}

func (d *Downloader) copyDirect(j *downloadJob, dst *os.File, src io.Reader, total int64) error {
	j.item.TotalBytes = total
	if total < 0 {
		j.item.TotalBytes = 0
	}
	buffer := make([]byte, 128*1024)
	last := time.Time{}
	for {
		if err := j.ctx.Err(); err != nil {
			return err
		}
		n, err := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			j.item.Bytes += int64(written)
			if writeErr != nil {
				return writeErr
			}
			if total > 0 {
				j.item.Progress = min(99, float64(j.item.Bytes)*100/float64(total))
			}
			if time.Since(last) >= 250*time.Millisecond {
				d.emit(j.item)
				last = time.Now()
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if j.item.Bytes == 0 {
		return errors.New("download produced an empty file")
	}
	if total >= 0 && j.item.Bytes != total {
		return fmt.Errorf("incomplete download: received %d of %d bytes", j.item.Bytes, total)
	}
	return nil
}

func downloadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func safeDownloadName(name string) string {
	var out []rune
	length := 0
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == ' ' {
			if length+len(string(r)) > 80 {
				break
			}
			out = append(out, r)
			length += len(string(r))
		}
		if len(out) >= 80 {
			break
		}
	}
	name = strings.TrimSpace(strings.Join(strings.Fields(string(out)), " "))
	if name == "" {
		return "Anime"
	}
	return name
}

func readPlaylist(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, 4*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4*1024*1024 {
		return nil, errors.New("HLS playlist is too large")
	}
	if !bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})), []byte("#EXTM3U")) {
		return nil, errors.New("invalid HLS playlist")
	}
	return body, nil
}

var hlsBandwidth = regexp.MustCompile(`(?:^|,)BANDWIDTH=(\d+)`)

// nativePlaylist resolves the highest-bandwidth variant and checks the native
// downloader's limited transport-stream contract before creating final media.
func (d *Downloader) nativePlaylist(ctx context.Context, stream Stream, body []byte, depth int) (Stream, []string, error) {
	if depth > 3 {
		return Stream{}, nil, errors.New("HLS variant nesting is unsupported")
	}
	lines := strings.Split(string(body), "\n")
	master, bestBW, bestURL := false, -1, ""
	var segments []string
	end := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "#EXT-X-KEY:") && !strings.Contains(line, "METHOD=NONE"):
			return Stream{}, nil, errors.New("encrypted HLS downloads require ffmpeg")
		case strings.HasPrefix(line, "#EXT-X-MAP:") || strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			return Stream{}, nil, errors.New("fragmented HLS downloads require ffmpeg")
		case strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, "TYPE=AUDIO") && strings.Contains(line, "URI="):
			return Stream{}, nil, errors.New("HLS with separate audio requires ffmpeg")
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			master = true
			bw := 0
			if m := hlsBandwidth.FindStringSubmatch(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")); len(m) > 1 {
				bw, _ = strconv.Atoi(m[1])
			}
			if i+1 < len(lines) && bw > bestBW {
				next := strings.TrimSpace(lines[i+1])
				if next != "" && !strings.HasPrefix(next, "#") {
					bestBW, bestURL = bw, next
				}
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			if i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if next != "" && !strings.HasPrefix(next, "#") {
					segmentURL, err := absoluteSegmentURL(stream.URL, next)
					if err != nil {
						return Stream{}, nil, err
					}
					segments = append(segments, segmentURL)
				}
			}
		case line == "#EXT-X-ENDLIST":
			end = true
		}
	}
	if master {
		if bestURL == "" {
			return Stream{}, nil, errors.New("HLS has no playable variant")
		}
		base, _ := url.Parse(stream.URL)
		ref, err := url.Parse(bestURL)
		if err != nil {
			return Stream{}, nil, err
		}
		stream.URL = base.ResolveReference(ref).String()
		resp, r, isHLS, err := d.openMedia(ctx, stream)
		if err != nil {
			return Stream{}, nil, err
		}
		defer resp.Body.Close()
		if !isHLS {
			return Stream{}, nil, errors.New("HLS variant is not a playlist")
		}
		body, err = readPlaylist(r)
		if err != nil {
			return Stream{}, nil, err
		}
		return d.nativePlaylist(ctx, stream, body, depth+1)
	}
	if !end {
		return Stream{}, nil, errors.New("live HLS downloads require ffmpeg")
	}
	if len(segments) == 0 {
		return Stream{}, nil, errors.New("HLS playlist contains no segments")
	}
	return stream, segments, nil
}

func absoluteSegmentURL(base, ref string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	resolved := u.ResolveReference(r)
	if resolved.Scheme != "https" && resolved.Scheme != "http" {
		return "", errors.New("invalid HLS segment URL")
	}
	return resolved.String(), nil
}

func (d *Downloader) downloadNativeHLS(j *downloadJob, stream Stream, body []byte, path string) error {
	stream, segments, err := d.nativePlaylist(j.ctx, stream, body, 0)
	if err != nil {
		return err
	}
	base := d.httpClient()
	client := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	tracked := &hlsCompletionTransport{base: transport, expected: make(map[string]int), completed: make(map[string]int), playlistURL: stream.URL}
	var canonical strings.Builder
	canonical.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:1\n")
	for _, segment := range segments {
		tracked.expected[segment]++
		fmt.Fprintf(&canonical, "#EXTINF:1,\n%s\n", segment)
	}
	canonical.WriteString("#EXT-X-ENDLIST\n")
	tracked.playlist = canonical.String()
	client.Transport = tracked
	var lastBytes int64
	var written, total int
	lastUpdate := time.Time{}
	err = hls.DownloadToFileWithClient(j.ctx, &client, stream.URL, path, stream.Headers, func(n int64, s, t int) {
		lastBytes, written, total = n, s, t
		j.item.Bytes = n
		if t > 0 {
			j.item.Progress = min(99, float64(s)*100/float64(t))
		}
		if time.Since(lastUpdate) >= 250*time.Millisecond {
			d.emit(j.item)
			lastUpdate = time.Now()
		}
	})
	if err != nil {
		return err
	}
	tracked.mu.Lock()
	defer tracked.mu.Unlock()
	for segment, count := range tracked.expected {
		if tracked.completed[segment] < count {
			return errors.New("incomplete HLS download: a media segment was missing or empty")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if written != len(segments) || total != len(segments) || lastBytes <= 0 || lastBytes != tracked.bytes || info.Size() != lastBytes {
		return errors.New("incomplete HLS download: segment or file totals do not agree")
	}
	return nil
}

type hlsCompletionTransport struct {
	base                  http.RoundTripper
	mu                    sync.Mutex
	expected, completed   map[string]int
	bytes                 int64
	playlistURL, playlist string
}

func (t *hlsCompletionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The validated playlist snapshot uses absolute URLs, repairing the native
	// parser's root-relative URL handling and avoiding a second mutable playlist.
	if req.URL.String() == t.playlistURL {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl"}}, Body: io.NopCloser(strings.NewReader(t.playlist)), ContentLength: int64(len(t.playlist)), Request: req}, nil
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if _, ok := t.expected[req.URL.String()]; ok && resp.StatusCode == http.StatusOK {
		resp.Body = &hlsCompletionBody{ReadCloser: resp.Body, done: func(n int64) { t.mu.Lock(); t.completed[req.URL.String()]++; t.bytes += n; t.mu.Unlock() }}
	}
	return resp, nil
}

type hlsCompletionBody struct {
	io.ReadCloser
	bytes  int64
	marked bool
	prefix []byte
	done   func(int64)
}

func (b *hlsCompletionBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes += int64(n)
	if len(b.prefix) < 376 {
		keep := min(n, 376-len(b.prefix))
		b.prefix = append(b.prefix, p[:keep]...)
	}
	// Native output is a transport stream, so pages, encrypted bytes and
	// fragmented MP4 masquerading as segments must never count as completed.
	isTransportStream := len(b.prefix) >= 188 && b.bytes%188 == 0 && b.prefix[0] == 0x47 && (len(b.prefix) < 189 || b.prefix[188] == 0x47)
	if errors.Is(err, io.EOF) && isTransportStream && !b.marked {
		b.marked = true
		b.done(b.bytes)
	}
	return n, err
}

func findFFmpeg() string {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "ffmpeg"
	if strings.HasSuffix(strings.ToLower(exe), ".exe") {
		name += ".exe"
	}
	for _, path := range []string{filepath.Join(filepath.Dir(exe), name), filepath.Join(filepath.Dir(exe), "tools", name), filepath.Join(filepath.Dir(exe), "..", "Resources", "tools", name), filepath.Join("/opt/homebrew/bin", name), filepath.Join("/usr/local/bin", name)} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func playlistDuration(body []byte) float64 {
	var total float64
	for _, raw := range strings.Split(string(body), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(raw), "#EXTINF:"); ok {
			value, _, _ = strings.Cut(value, ",")
			if n, err := strconv.ParseFloat(value, 64); err == nil {
				total += n
			}
		}
	}
	return total
}

func (d *Downloader) downloadFFmpeg(j *downloadJob, stream Stream, executable, path string, duration float64) error {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-xerror", "-y"}
	var fields []string
	for name, value := range stream.Headers {
		if strings.EqualFold(name, "User-Agent") {
			args = append(args, "-user_agent", value)
		} else {
			fields = append(fields, name+": "+value)
		}
	}
	if len(fields) > 0 {
		args = append(args, "-headers", strings.Join(fields, "\r\n")+"\r\n")
	}
	args = append(args, "-allowed_extensions", "ALL", "-i", stream.URL, "-map", "0:v:0", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart", "-progress", "pipe:1", "-f", "mp4", path)
	cmd := exec.CommandContext(j.ctx, executable, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	lastUpdate := time.Time{}
	for scanner.Scan() {
		line := scanner.Text()
		if value, ok := strings.CutPrefix(line, "total_size="); ok {
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				j.item.Bytes = n
			}
		}
		if value, ok := strings.CutPrefix(line, "out_time_us="); ok && duration > 0 {
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				j.item.Progress = min(99, float64(n)/1e6*100/duration)
			}
		}
		if strings.HasPrefix(line, "progress=") && time.Since(lastUpdate) >= 250*time.Millisecond {
			d.emit(j.item)
			lastUpdate = time.Now()
		}
	}
	readErr := scanner.Err()
	waitErr := cmd.Wait()
	if j.ctx.Err() != nil {
		return j.ctx.Err()
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 1500 {
			message = message[len(message)-1500:]
		}
		return fmt.Errorf("HLS download failed: %w: %s", waitErr, message)
	}
	if readErr != nil {
		return readErr
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		if len(message) > 1500 {
			message = message[len(message)-1500:]
		}
		return fmt.Errorf("ffmpeg reported media errors: %s", message)
	}
	return nil
}
