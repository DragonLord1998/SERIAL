package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type hiAnimeTestTransport func(*http.Request) (*http.Response, error)

func (f hiAnimeTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

const hiAnimeTestSearch = `<div id="main-content"><div class="film_list-wrap">
<div class="flw-item"><div class="film-poster"><img class="film-poster-img" src="https://cdn.anipixcdn.co/naruto.jpg"><div class="tick-sub">220</div><div class="tick-eps">220</div></div><h3 class="film-name"><a href="https://hianime.at/naruto-1335">Naruto &amp; friends</a></h3><div class="description">A ninja's <b>story</b>.</div></div>
<div class="flw-item"><h3 class="film-name"><a href="/naruto-1335">Duplicate</a></h3></div>
<div class="flw-item"><h3 class="film-name"><a href="https://evil.example/naruto-1335">Untrusted result</a></h3></div>
</div></div><div id="main-sidebar"><div class="film_list-wrap"><div class="flw-item"><h3 class="film-name"><a href="/unrelated-recommendation-999">Unrelated recommendation</a></h3></div></div></div>`

const hiAnimeTestEpisodes = `<a class="ep-item" data-number="2" data-id="22677" href="https://hianime.at/watch/naruto-1335?ep=22677"><div class="ep-name">Episode two</div></a>
<a class="ep-item" data-number="1" data-id="22676" href="https://hianime.at/watch/naruto-1335?ep=22676"><div class="ep-name">Episode one</div></a>
<a class="ep-item" data-number="1" data-id="22676" href="https://hianime.at/watch/naruto-1335?ep=22676">Duplicate</a>
<a class="ep-item" data-number="3" data-id="9" href="https://evil.example/watch/naruto-1335?ep=9">Untrusted host</a>
<a class="ep-item" data-number="4" data-id="10" href="https://hianime.at/watch/bleach-100?ep=10">Wrong show</a>
<a class="ep-item" data-number="5" data-id="11" href="https://hianime.at/watch/naruto-1335?ep=12">Wrong ID</a>`

var hiAnimeTestAnime = Anime{Source: "hianime", URL: "https://hianime.at/naruto-1335", Title: "Naruto"}
var hiAnimeTestEpisode = Episode{Number: "1", URL: "https://hianime.at/watch/naruto-1335?ep=22676"}

func hiAnimeTestJSONHTML(html string) string {
	data, _ := json.Marshal(map[string]any{"status": true, "html": html})
	return string(data)
}

func hiAnimeTestServer(mode, embedURL string) string {
	return fmt.Sprintf(`<div class="server-item" data-type="%s" data-server-name="ZokoAnime" data-hash="%s"></div>`, mode, base64.StdEncoding.EncodeToString([]byte(embedURL)))
}

func hiAnimeTestEmbed(payload string) string {
	encoded := []byte(payload)
	key := []byte("otaku-embed-v1")
	for i := range encoded {
		encoded[i] ^= key[i%len(key)]
	}
	return `<script>window.__P="` + base64.StdEncoding.EncodeToString(encoded) + `";</script>`
}

type hiAnimeFixture struct {
	bodies   map[string]string
	statuses map[string]int
	requests []*http.Request
}

func hiAnimeTestClient(t *testing.T) (*hiAnimeClient, *hiAnimeFixture) {
	t.Helper()
	f := &hiAnimeFixture{bodies: map[string]string{
		"hianime.at/search?keyword=Naruto":                     hiAnimeTestSearch,
		"hianime.at/api/theme/episode/list/1335":               hiAnimeTestJSONHTML(hiAnimeTestEpisodes),
		"hianime.at/api/theme/episode/servers?episodeId=22676": hiAnimeTestJSONHTML(hiAnimeTestServer("sub", "https://zokoanime.video/stream/mal/20/1/sub") + hiAnimeTestServer("dub", "https://zokoanime.video/stream/mal/20/1/dub")),
		"zokoanime.video/stream/mal/20/1/sub":                  hiAnimeTestEmbed(`{"src":"https://hls.dramahot.top/video/master.m3u8","subtitles":[{"lang":"en","label":"English alternate","default":false,"src":"https://hls.dramahot.top/subs/alternate.vtt"},{"lang":"en","label":"English","default":true,"src":"https://hls.dramahot.top/subs/default.vtt"},{"lang":"en","default":true,"src":"http://127.0.0.1/unsafe.vtt"}]}`),
		"zokoanime.video/stream/mal/20/1/dub":                  hiAnimeTestEmbed(`{"src":"https://hls.dramahot.top/dub/master.m3u8"}`),
		"hls.dramahot.top/video/master.m3u8":                   "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1400000,RESOLUTION=1422x800\n800/index.m3u8\n",
		"hls.dramahot.top/video/800/index.m3u8":                "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:8,\nsegment.ts\n#EXT-X-ENDLIST\n",
		"hls.dramahot.top/dub/master.m3u8":                     "#EXTM3U\n#EXTINF:8,\nsegment.ts\n#EXT-X-ENDLIST\n",
	}, statuses: map[string]int{}}
	c := &hiAnimeClient{client: &http.Client{Transport: hiAnimeTestTransport(func(request *http.Request) (*http.Response, error) {
		f.requests = append(f.requests, request.Clone(request.Context()))
		key := request.URL.Host + request.URL.RequestURI()
		body, ok := f.bodies[key]
		if !ok {
			t.Errorf("unexpected HiAnime request: %s", key)
			return nil, errors.New("unexpected request")
		}
		status := f.statuses[key]
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}
	return c, f
}

func TestHiAnimeSearchCards(t *testing.T) {
	c, fixture := hiAnimeTestClient(t)
	items, err := c.Search(context.Background(), " Naruto ")
	if err != nil || len(items) != 1 {
		t.Fatalf("search = %#v, %v", items, err)
	}
	item := items[0]
	if item.Title != "Naruto & friends" || item.Source != "hianime" || item.Language != "EN" || item.URL != hiAnimeTestAnime.URL || item.ImageURL != "https://cdn.anipixcdn.co/naruto.jpg" || item.EpisodeCount != 220 || item.Description != "A ninja's story." || !strings.HasPrefix(item.ID, "hianime-") {
		t.Fatalf("incorrect search card: %#v", item)
	}
	fixture.bodies["hianime.at/search?keyword=Naruto+%26+friends"] = `<div id="main-content"><div class="film_list-wrap"></div></div>`
	items, err = c.Search(context.Background(), "Naruto & friends")
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty search = %#v, %v", items, err)
	}
}

func TestHiAnimeEpisodes(t *testing.T) {
	c, _ := hiAnimeTestClient(t)
	items, err := c.Episodes(context.Background(), hiAnimeTestAnime)
	if err != nil || len(items) != 2 {
		t.Fatalf("episodes = %#v, %v", items, err)
	}
	if items[0].Number != "1" || items[0].Title != "Episode one" || items[0].URL != hiAnimeTestEpisode.URL || items[1].Number != "2" {
		t.Fatalf("incorrect episodes: %#v", items)
	}
}

func TestHiAnimeResolveHeadersQualityAndSubtitle(t *testing.T) {
	c, fixture := hiAnimeTestClient(t)
	stream, err := c.Resolve(context.Background(), hiAnimeTestAnime, hiAnimeTestEpisode, Settings{Quality: "720p", Mode: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if stream.URL != "https://hls.dramahot.top/video/800/index.m3u8" {
		t.Fatalf("800p fallback = %s", stream.URL)
	}
	if stream.Headers["Referer"] != "https://zokoanime.video/" || stream.Headers["User-Agent"] != hiAnimeUserAgent {
		t.Fatalf("stream lost request headers: %#v", stream.Headers)
	}
	if len(stream.Subtitles) != 1 || stream.Subtitles[0].URL != "https://hls.dramahot.top/subs/default.vtt" || stream.Subtitles[0].Language != "en" {
		t.Fatalf("incorrect subtitles: %#v", stream.Subtitles)
	}
	for _, request := range fixture.requests {
		if request.Header.Get("User-Agent") != stream.Headers["User-Agent"] {
			t.Errorf("User-Agent differs between resolver and player: %s", request.URL.Host)
		}
		if request.URL.Host == "hls.dramahot.top" && request.Header.Get("Referer") != stream.Headers["Referer"] {
			t.Errorf("playlist Referer differs between resolver and player")
		}
	}

	stream, err = c.Resolve(context.Background(), hiAnimeTestAnime, hiAnimeTestEpisode, Settings{Quality: "best", Mode: "dub"})
	if err != nil || stream.URL != "https://hls.dramahot.top/dub/master.m3u8" || len(stream.Subtitles) != 0 {
		t.Fatalf("dub selected wrong stream: %#v, %v", stream, err)
	}
}

func TestHiAnimeRejectsUntrustedSelectionsBeforeRequest(t *testing.T) {
	for _, test := range []struct {
		name    string
		anime   Anime
		episode Episode
	}{
		{"wrong show host", Anime{Source: "hianime", URL: "https://evil.example/naruto-1335"}, hiAnimeTestEpisode},
		{"wrong source", Anime{Source: "anidb", URL: hiAnimeTestAnime.URL}, hiAnimeTestEpisode},
		{"show query", Anime{Source: "hianime", URL: hiAnimeTestAnime.URL + "?redirect=http://localhost"}, hiAnimeTestEpisode},
		{"encoded show path", Anime{Source: "hianime", URL: "https://hianime.at/naruto%2D1335"}, hiAnimeTestEpisode},
		{"wrong episode host", hiAnimeTestAnime, Episode{Number: "1", URL: "https://hianime.at.evil.example/watch/naruto-1335?ep=22676"}},
		{"wrong show slug", hiAnimeTestAnime, Episode{Number: "1", URL: "https://hianime.at/watch/bleach-100?ep=22676"}},
		{"malformed episode ID", hiAnimeTestAnime, Episode{Number: "1", URL: "https://hianime.at/watch/naruto-1335?ep=1/2"}},
		{"duplicate episode ID", hiAnimeTestAnime, Episode{Number: "1", URL: hiAnimeTestEpisode.URL + "&ep=1"}},
		{"extra query", hiAnimeTestAnime, Episode{Number: "1", URL: hiAnimeTestEpisode.URL + "&url=http://localhost"}},
		{"userinfo", hiAnimeTestAnime, Episode{Number: "1", URL: "https://user@hianime.at/watch/naruto-1335?ep=22676"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, fixture := hiAnimeTestClient(t)
			_, err := c.Resolve(context.Background(), test.anime, test.episode, Settings{})
			if err == nil || len(fixture.requests) != 0 {
				t.Fatalf("untrusted selection was requested: %v, %d requests", err, len(fixture.requests))
			}
		})
	}
}

func TestHiAnimeRejectsEpisodeIDOutsideShow(t *testing.T) {
	c, fixture := hiAnimeTestClient(t)
	episode := Episode{Number: "1", URL: "https://hianime.at/watch/naruto-1335?ep=99999"}
	_, err := c.Resolve(context.Background(), hiAnimeTestAnime, episode, Settings{})
	if err == nil || !strings.Contains(err.Error(), "does not belong") || len(fixture.requests) != 1 {
		t.Fatalf("outside episode accepted: %v, %d requests", err, len(fixture.requests))
	}
}

func TestHiAnimeMissingDubDoesNotFallBackToSub(t *testing.T) {
	c, fixture := hiAnimeTestClient(t)
	fixture.bodies["hianime.at/api/theme/episode/servers?episodeId=22676"] = hiAnimeTestJSONHTML(hiAnimeTestServer("sub", "https://zokoanime.video/stream/mal/20/1/sub"))
	_, err := c.Resolve(context.Background(), hiAnimeTestAnime, hiAnimeTestEpisode, Settings{Mode: "dub"})
	if err == nil || !strings.Contains(err.Error(), "does not offer a dub") || len(fixture.requests) != 2 {
		t.Fatalf("missing dub fell back: %v, %d requests", err, len(fixture.requests))
	}
}

func TestHiAnimeUnavailableMediaDoesNotBecomeAPlayableStream(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"unavailable selected rendition", 503, "Unavailable"},
		{"HTML selected rendition", 200, "<html>Video removed</html>"},
		{"empty selected rendition", 200, "#EXTM3U\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, fixture := hiAnimeTestClient(t)
			key := "hls.dramahot.top/video/800/index.m3u8"
			fixture.statuses[key] = test.status
			fixture.bodies[key] = test.body
			stream, err := c.Resolve(context.Background(), hiAnimeTestAnime, hiAnimeTestEpisode, Settings{})
			if err == nil || stream.URL != "" {
				t.Fatalf("unavailable media became a playable stream: %#v, %v", stream, err)
			}
		})
	}
}

func TestHiAnimeRejectsUnsupportedEmbedURLsAndPayloads(t *testing.T) {
	for _, embedURL := range []string{"http://zokoanime.video/stream/mal/20/1/sub", "https://127.0.0.1/stream/mal/20/1/sub", "https://zokoanime.video.evil.example/stream/mal/20/1/sub", "https://zokoanime.video/admin", "https://zokoanime.video/stream/mal/20/1/dub", "https://zokoanime.video/stream/mal/20/1/sub?url=http://localhost"} {
		t.Run(embedURL, func(t *testing.T) {
			c, fixture := hiAnimeTestClient(t)
			fixture.bodies["hianime.at/api/theme/episode/servers?episodeId=22676"] = hiAnimeTestJSONHTML(hiAnimeTestServer("sub", embedURL))
			_, err := c.Resolve(context.Background(), hiAnimeTestAnime, hiAnimeTestEpisode, Settings{})
			if err == nil || len(fixture.requests) != 2 {
				t.Fatalf("unsafe embed requested: %v, %d requests", err, len(fixture.requests))
			}
		})
	}
	for _, embedBody := range []string{`<script>window.__P="%%%";</script>`, hiAnimeTestEmbed(`not JSON`), hiAnimeTestEmbed(`{"src":"http://127.0.0.1/video.m3u8"}`), `<html>Server unavailable</html>`} {
		t.Run(embedBody[:min(30, len(embedBody))], func(t *testing.T) {
			c, fixture := hiAnimeTestClient(t)
			fixture.bodies["zokoanime.video/stream/mal/20/1/sub"] = embedBody
			_, err := c.Resolve(context.Background(), hiAnimeTestAnime, hiAnimeTestEpisode, Settings{})
			if err == nil || len(fixture.requests) != 3 {
				t.Fatalf("invalid embed accepted: %v, %d requests", err, len(fixture.requests))
			}
		})
	}
}

func TestHiAnimeResponseFailuresAreNotEmptyResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"403", 403, "Forbidden"},
		{"503", 503, "Unavailable"},
		{"challenge", 200, `<html><title>Just a moment...</title><div class="film_list-wrap"></div></html>`},
		{"empty", 200, ""},
		{"layout changed", 200, "<html>Gone</html>"},
		{"oversize", 200, strings.Repeat("x", (4<<20)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, fixture := hiAnimeTestClient(t)
			fixture.bodies["hianime.at/search?keyword=Naruto"] = test.body
			fixture.statuses["hianime.at/search?keyword=Naruto"] = test.status
			if items, err := c.Search(context.Background(), "Naruto"); err == nil {
				t.Fatalf("failure became successful search: %#v", items)
			}
		})
	}
	for _, body := range []string{`{"status":false,"html":"unavailable"}`, `{"status":true,"html":""}`, `not JSON`} {
		c, fixture := hiAnimeTestClient(t)
		fixture.bodies["hianime.at/api/theme/episode/list/1335"] = body
		if items, err := c.Episodes(context.Background(), hiAnimeTestAnime); err == nil {
			t.Fatalf("invalid API became successful episodes: %#v", items)
		}
	}
}

func TestHiAnimeRequestsRespectContextAndRedirects(t *testing.T) {
	c, fixture := hiAnimeTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, "Naruto"); !errors.Is(err, context.Canceled) || len(fixture.requests) != 0 {
		t.Fatalf("cancelled search: %v", err)
	}
	c.client.Transport = hiAnimeTestTransport(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "Naruto"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active request did not cancel: %v", err)
	}
	requests := 0
	c.client.Transport = hiAnimeTestTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://evil.example/redirect"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	if _, err := c.Search(context.Background(), "Naruto"); err == nil || requests != 1 {
		t.Fatalf("cross-host redirect followed: %v, %d requests", err, requests)
	}
}

func TestHiAnimeHLSQualitySelection(t *testing.T) {
	base := "https://hls.dramahot.top/video/master.m3u8"
	master := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=1920x1080\n1080/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=300000,RESOLUTION=640x360\n/360/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=1280x720\n720/index.m3u8\n")
	for _, test := range []struct{ quality, suffix string }{{"best", "/video/1080/index.m3u8"}, {"auto", "/video/1080/index.m3u8"}, {"1080p", "/video/1080/index.m3u8"}, {"800p", "/video/720/index.m3u8"}, {"480p", "/360/index.m3u8"}, {"240p", "/360/index.m3u8"}, {"worst", "/360/index.m3u8"}} {
		t.Run(test.quality, func(t *testing.T) {
			selected, err := hiAnimeSelectPlaylist(base, master, test.quality)
			if err != nil || selected != "https://hls.dramahot.top"+test.suffix {
				t.Fatalf("quality %s: %s, %v", test.quality, selected, err)
			}
		})
	}
	for _, body := range []string{"<html>not HLS</html>", "#EXTM3U\n", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttp://127.0.0.1/v.m3u8\n"} {
		if selected, err := hiAnimeSelectPlaylist(base, []byte(body), "best"); err == nil {
			t.Fatalf("invalid playlist accepted: %s", selected)
		}
	}
	withAudio := append([]byte("#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",URI=\"audio.m3u8\"\n"), master[len("#EXTM3U\n"):]...)
	if selected, err := hiAnimeSelectPlaylist(base, withAudio, "best"); err != nil || selected != base {
		t.Fatalf("separate audio discarded: %s, %v", selected, err)
	}
	if _, err := hiAnimeSelectPlaylist(base, withAudio, "720p"); err == nil {
		t.Fatal("separate audio silently discarded for fixed quality")
	}
}

func TestHiAnimeLive(t *testing.T) {
	if os.Getenv("GOANIME_TEST_HIANIME") != "1" {
		t.Skip("set GOANIME_TEST_HIANIME=1 to check the live provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := newHiAnimeClient()
	items, err := c.Search(ctx, "Naruto")
	if err != nil {
		t.Fatal(err)
	}
	var anime Anime
	for _, item := range items {
		if item.URL == hiAnimeTestAnime.URL {
			anime = item
			break
		}
	}
	if anime.URL == "" {
		t.Fatal("live HiAnime search did not contain Naruto")
	}
	episodes, err := c.Episodes(ctx, anime)
	if err != nil || len(episodes) != 220 {
		t.Fatalf("live Naruto episodes = %d, %v", len(episodes), err)
	}
	stream, err := c.Resolve(ctx, anime, episodes[0], Settings{Mode: "sub", Quality: "720p"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stream.URL, "/800/index.m3u8") || len(stream.Subtitles) == 0 {
		t.Fatalf("unexpected live rendition or missing English subtitles")
	}
	t.Logf("Live HiAnime: %d search cards, %d episodes, playable HLS playlist with English subtitles", len(items), len(episodes))
}
