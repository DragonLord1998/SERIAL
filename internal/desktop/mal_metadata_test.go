package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func TestMALHTMLFallbackUsesOriginalAnimeCovers(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<table><tr class="ranking-list"><td><img src="https://cdn.myanimelist.net/images/spacer.gif" data-src="https://cdn.myanimelist.net/r/50x70/images/anime/1015/138006.jpg?s=thumbnail-signature"></td><td><a href="https://myanimelist.net/anime/52991/Sousou_no_Frieren">Sousou no Frieren</a></td></tr></table>`))
	if err != nil {
		t.Fatal(err)
	}
	cards := malHTMLAnimeCards(doc)
	const original = "https://cdn.myanimelist.net/images/anime/1015/138006.jpg"
	if len(cards) != 1 || cards[0].ImageURL != original {
		t.Fatalf("expected original poster, got %#v", cards)
	}
	if got := malBackgroundImage(`background-image: url('https://cdn.myanimelist.net/r/100x140/images/anime/1015/138006.jpg?s=small')`); got != original {
		t.Fatalf("recommendation poster = %q", got)
	}
	for _, unchanged := range []string{
		"https://images.example/r/50x70/images/anime/1015/138006.jpg?s=retain",
		"https://cdn.myanimelist.net/r/50x70/images/manga/1015/138006.jpg?s=retain",
		original,
	} {
		if got := malCoverURL(unchanged); got != unchanged {
			t.Fatalf("unrelated image changed: %q -> %q", unchanged, got)
		}
	}
}

func TestMALDetailsPreservesProviderIdentityAndAddsMetadata(t *testing.T) {
	srv := malMetadataTestServer(t, map[string]any{
		"/anime/20/full": map[string]any{"data": malMetadataAnimeFixture(20, "Naruto", "dQw4w9WgXcQ", []jikanNamed{{MALID: 1, Name: "Action"}}, []jikanNamed{{MALID: 17, Name: "Martial Arts"}}, nil)},
	})
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	got, err := meta.Details(context.Background(), Anime{
		ID: "hianime-keep", Title: "Naruto", URL: "https://hianime.at/naruto-1335", Source: "hianime", Language: "EN", MALID: 20,
	})
	if err != nil {
		t.Fatalf("Details returned error: %v", err)
	}
	if got.ID != "hianime-keep" || got.URL != "https://hianime.at/naruto-1335" || got.Source != "hianime" {
		t.Fatalf("provider identity was not preserved: %#v", got)
	}
	if got.MALID != 20 || got.MetadataSource != "myanimelist" || got.Trailer == nil || got.Trailer.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("metadata not applied: %#v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0].Kind != "genre" || got.Tags[1].Kind != "theme" {
		t.Fatalf("tags not applied: %#v", got.Tags)
	}
}

func TestMALTagsAndValidation(t *testing.T) {
	srv := malMetadataTestServer(t, map[string]any{
		"/genres/anime?filter=genres":       map[string]any{"data": []jikanNamed{{MALID: 1, Name: "Action"}}},
		"/genres/anime?filter=themes":       map[string]any{"data": []jikanNamed{{MALID: 17, Name: "Martial Arts"}}},
		"/genres/anime?filter=demographics": map[string]any{"data": []jikanNamed{{MALID: 27, Name: "Shounen"}}},
	})
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	tags, err := meta.Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags returned error: %v", err)
	}
	if got := len(tags); got != 3 {
		t.Fatalf("tag count = %d, tags=%#v", got, tags)
	}
	valid := Anime{ID: "mal:20", Source: "mal", Title: "Naruto", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20}
	if err := ValidateMALAnime(valid); err != nil {
		t.Fatalf("valid MAL anime rejected: %v", err)
	}
	invalid := valid
	invalid.URL = "https://myanimelist.net/anime/21/Naruto"
	if err := ValidateMALAnime(invalid); err == nil {
		t.Fatalf("mismatched MAL URL accepted")
	}
	invalid = valid
	invalid.ID = "mal:21"
	if err := ValidateMALAnime(invalid); err == nil {
		t.Fatalf("mismatched MAL id accepted")
	}
}

func TestMALExploreRanksRecommendationsAndExcludesKnown(t *testing.T) {
	routes := map[string]any{
		"/anime/20/recommendations": map[string]any{"data": []map[string]any{
			{"entry": malMetadataAnimeFixture(21, "One Piece", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil), "votes": 50},
			{"entry": malMetadataAnimeFixture(22, "Bleach", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil), "votes": 5},
		}},
		"/top/anime?filter=bypopularity": map[string]any{"data": []jikanAnime{
			malMetadataAnimeFixture(21, "One Piece", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil),
			malMetadataAnimeFixture(23, "Frieren", "", []jikanNamed{{MALID: 10, Name: "Fantasy"}}, nil, nil),
		}},
	}
	srv := malMetadataTestServer(t, routes)
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	feedback := []AnimeFeedback{
		{Anime: Anime{ID: "mal:20", Source: "mal", Title: "Naruto", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20, Tags: []AnimeTag{{ID: 1, Name: "Action", Kind: "genre"}}}, Value: "like", UpdatedAt: "2026-10-02T12:00:00Z"},
		{Anime: Anime{ID: "mal:22", Source: "mal", Title: "Bleach", URL: "https://myanimelist.net/anime/22/Bleach", MALID: 22, Tags: []AnimeTag{{ID: 1, Name: "Action", Kind: "genre"}}}, Value: "dislike", UpdatedAt: "2026-10-02T12:00:01Z"},
	}
	result, err := meta.Explore(context.Background(), feedback, nil, nil, false)
	if err != nil {
		t.Fatalf("Explore returned error: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].Anime.Title != "One Piece" || result.Items[1].Anime.Title != "Frieren" {
		t.Fatalf("unexpected ranked recommendations: %#v", result.Items)
	}
	for _, item := range result.Items {
		if item.Anime.MALID == 22 {
			t.Fatalf("disliked anime was not excluded: %#v", result.Items)
		}
	}
	if !strings.Contains(result.Items[0].Reason, "MyAnimeList recommendations for Naruto") {
		t.Fatalf("recommendation reason not preserved: %#v", result.Items[0])
	}
}

func TestMALExploreTagFilterAndTrailerSanitization(t *testing.T) {
	srv := malMetadataTestServer(t, map[string]any{
		"/anime?genres=1&limit=25&order_by=members&sfw=true&sort=desc": map[string]any{"data": []jikanAnime{
			malMetadataAnimeFixture(24, "Demon Slayer", "not-valid", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil),
			malMetadataAnimeFixture(25, "Mob Psycho 100", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil),
		}},
	})
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	result, err := meta.Explore(context.Background(), nil, nil, []int{1}, false)
	if err != nil {
		t.Fatalf("Explore tag filter returned error: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].Anime.Trailer != nil {
		t.Fatalf("tag filter/trailer result unexpected: %#v", result.Items)
	}
}

func TestMALMetadataDurableCacheSurvivesOutageAndRestart(t *testing.T) {
	var topRequests atomic.Int64
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/top/anime" {
			topRequests.Add(1)
		}
		if fail.Load() {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"temporary"}`))
			return
		}
		if r.URL.String() != "/top/anime?filter=bypopularity" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []jikanAnime{
			malMetadataAnimeFixture(1, "Cowboy Bebop", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil),
		}})
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	first := malMetadataTestClient(t, srv.URL, dir)
	if result, err := first.Explore(context.Background(), nil, nil, nil, false); err != nil || len(result.Items) != 1 {
		t.Fatalf("initial Explore = %#v, %v", result, err)
	}
	fail.Store(true)
	second := malMetadataTestClient(t, srv.URL, dir)
	result, err := second.Explore(context.Background(), nil, nil, nil, true)
	if err != nil || len(result.Items) != 1 || len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "using cached MyAnimeList metadata") {
		t.Fatalf("cached outage Explore = %#v, %v", result, err)
	}
	if topRequests.Load() != 2 {
		t.Fatalf("expected one live retry after restart, got %d", topRequests.Load())
	}
}

func TestMALDetailsUsesExpiredCacheAfterRestartWhenAllProvidersDown(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"down"}`))
			return
		}
		if r.URL.Path != "/anime/20/full" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": malMetadataAnimeFixture(20, "Naruto", "dQw4w9WgXcQ", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil)})
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	first := malMetadataTestClient(t, srv.URL, dir)
	first.now = func() time.Time { return now }
	if got, err := first.Details(context.Background(), Anime{ID: "mal:20", Source: "mal", Title: "Naruto", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20}); err != nil || got.Trailer == nil {
		t.Fatalf("initial cached Details = %#v, %v", got, err)
	}
	fail.Store(true)
	second := malMetadataTestClient(t, srv.URL, dir)
	second.now = func() time.Time { return now.Add(25 * time.Hour) }
	got, err := second.Details(context.Background(), Anime{ID: "mal:20", Source: "mal", Title: "Naruto", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20})
	if err != nil || got.MALID != 20 || got.Trailer == nil || got.Trailer.YouTubeID != "dQw4w9WgXcQ" {
		t.Fatalf("expired restart cache Details = %#v, %v", got, err)
	}
}

func TestMALHTMLCacheSurvivesRestartAndTrailerNoCookieMarkup(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	fail := atomic.Bool{}
	var videoRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`down`))
			return
		}
		switch r.URL.Path {
		case "/anime/44511/full":
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"down"}`))
		case "/anime/44511":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Chainsaw Man"><meta property="og:description" content="Devils"><meta property="og:image" content="https://img.example/chainsaw.jpg"><div><span class="dark_text">Genres:</span><a href="/anime/genre/1/Action">Action</a></div>`))
		case "/anime/44511/_/video":
			videoRequests.Add(1)
			_, _ = w.Write([]byte(`<div class="video-block music-video"><a class="iframe video-list" href="https://www.youtube-nocookie.com/embed/dFlDRhvM4L0?enablejsapi=1" data-title="OP 1"><span class="title">OP 1</span></a></div><div class="video-block promotional-video"><h2>Trailers</h2><a class="iframe js-fancybox-video video-list di-ib po-r" href="https://www.youtube-nocookie.com/embed/jk7QSGwupPA?enablejsapi=1&wmode=opaque&autoplay=1" data-title="PV 3"><span class="title">PV 3</span></a></div>`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	first := malMetadataTestClient(t, srv.URL, dir)
	first.now = func() time.Time { return now }
	got, err := first.Details(context.Background(), Anime{ID: "mal:44511", Source: "mal", Title: "Chainsaw Man", URL: "https://myanimelist.net/anime/44511/Chainsaw_Man", MALID: 44511})
	if err != nil || got.Trailer == nil || got.Trailer.YouTubeID != "jk7QSGwupPA" {
		t.Fatalf("initial HTML trailer = %#v, %v", got.Trailer, err)
	}
	fail.Store(true)
	second := malMetadataTestClient(t, srv.URL, dir)
	second.now = func() time.Time { return now.Add(25 * time.Hour) }
	got, err = second.Details(context.Background(), Anime{ID: "mal:44511", Source: "mal", Title: "Chainsaw Man", URL: "https://myanimelist.net/anime/44511/Chainsaw_Man", MALID: 44511})
	if err != nil || got.Trailer == nil || got.Trailer.YouTubeID != "jk7QSGwupPA" || videoRequests.Load() != 1 {
		t.Fatalf("restart HTML cache trailer = %#v, %v videoRequests=%d", got.Trailer, err, videoRequests.Load())
	}
}

func TestMALHTMLFallbackCoversCurrentJikanOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anime/20":
			_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Naruto"><meta property="og:description" content="A loud ninja"><meta property="og:image" content="https://img.example/naruto.jpg"></head><body><h2>Alternative Titles</h2><div class="spaceit_pad"><span class="dark_text">Synonyms:</span> NARUTO</div><div class="spaceit_pad"><span class="dark_text">Japanese:</span> ナルト</div><span itemprop="ratingValue">8.0</span><div class="spaceit_pad"><span class="dark_text">Episodes:</span> 220</div><div><span class="dark_text">Genres:</span><a href="/anime/genre/1/Action">Action</a></div><div><span class="dark_text">Themes:</span><a href="/anime/genre/17/Martial_Arts">Martial Arts</a></div></body></html>`))
		case "/anime.php":
			if r.URL.Query().Get("q") != "" {
				_, _ = w.Write([]byte(`<table><tr><td><a href="https://myanimelist.net/anime/20/Naruto"><strong>Naruto</strong></a><div>Episodes: 220</div></td></tr></table>`))
				return
			}
			_, _ = w.Write([]byte(`<a href="/anime/genre/1/Action">Action</a><a href="/anime/genre/10/Fantasy">Fantasy</a>`))
		case "/anime/20/_/video":
			_, _ = w.Write([]byte(`<div class="oped theme-song"><input id="youtube_url_1" value="https://music.youtube.com/watch?v=L8EA-K-If2E"></div><div class="video-promotion"><a title="PV Trailer" href="https://www.youtube.com/watch?v=dQw4w9WgXcQ">PV</a></div>`))
		case "/anime/20/_/userrecs":
			_, _ = w.Write([]byte(`<div id="anime_recommendation"><li class="btn-anime" title="Black Clover" style="background-image:url('https://img.example/black-clover.jpg')"><a class="link" href="/recommendations/anime/20-34572">Black Clover</a><span>12 users</span></li></div>`))
		case "/topanime.php":
			_, _ = w.Write([]byte(`<table><tr><td><a href="https://myanimelist.net/anime/5114/Fullmetal_Alchemist_Brotherhood" title="Fullmetal Alchemist: Brotherhood"><img data-src="https://img.example/fma.jpg"></a><span class="score">9.1</span></td></tr></table>`))
		case "/anime/genre/1":
			_, _ = w.Write([]byte(`<div><a href="https://myanimelist.net/anime/1535/Death_Note" title="Death Note"><img src="https://img.example/death-note.jpg"></a></div>`))
		default:
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"Jikan outage"}`))
		}
	}))
	t.Cleanup(srv.Close)
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	got, err := meta.Details(context.Background(), Anime{ID: "mal:20", Source: "mal", Title: "Naruto", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20})
	if err != nil || got.Description != "A loud ninja" || got.EpisodeCount != 220 || len(got.Tags) != 2 || got.Trailer == nil || got.Trailer.YouTubeID != "dQw4w9WgXcQ" || len(got.AlternateTitles) < 3 {
		t.Fatalf("HTML Details fallback = %#v, %v", got, err)
	}
	searched, err := meta.Details(context.Background(), Anime{ID: "hianime-naruto", Source: "hianime", Title: "Naruto", URL: "https://hianime.at/naruto-1335", EpisodeCount: 220})
	if err != nil || searched.MALID != 20 || searched.Source != "hianime" {
		t.Fatalf("HTML exact search Details fallback = %#v, %v", searched, err)
	}
	tags, err := meta.Tags(context.Background())
	if err != nil || len(tags) != 2 || tags[0].Name != "Action" {
		t.Fatalf("HTML Tags fallback = %#v, %v", tags, err)
	}
	result, err := meta.Explore(context.Background(), []AnimeFeedback{{Anime: got, Value: "like", UpdatedAt: "2026-10-02T12:00:00Z"}}, nil, nil, true)
	if err != nil || len(result.Items) != 2 || result.Items[0].Anime.Title != "Black Clover" {
		t.Fatalf("HTML Explore fallback = %#v, %v", result, err)
	}
	filtered, err := meta.Explore(context.Background(), nil, nil, []int{1}, true)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Anime.Title != "Death Note" {
		t.Fatalf("HTML genre fallback = %#v, %v", filtered, err)
	}
}

func TestMALHardeningCapsPartialsRedirectsAndFilteredExplore(t *testing.T) {
	var searches atomic.Int64
	var htmlSearches atomic.Int64
	var recs atomic.Int64
	var htmlTop atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/anime" && r.URL.Query().Get("q") != "":
			searches.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []jikanAnime{}})
		case r.URL.Path == "/anime.php" && r.URL.Query().Get("q") != "":
			htmlSearches.Add(1)
			_, _ = w.Write([]byte(`<table></table>`))
		case r.URL.Path == "/anime.php":
			_, _ = w.Write([]byte(`<a href="/anime/genre/1/Action">Action (8,123)</a><a href="/anime/genre/10/Fantasy">Fantasy</a>`))
		case r.URL.Path == "/top/anime":
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"down"}`))
		case r.URL.Path == "/topanime.php":
			htmlTop.Add(1)
			_, _ = w.Write([]byte(`<table><tr class="ranking-list"><td><img src="https://cdn.myanimelist.net/images/spacer.gif" data-src="https://img.example/cowboy.jpg"></td><td><a href="https://myanimelist.net/anime/1/Cowboy_Bebop" title="Cowboy Bebop">Cowboy Bebop</a></td></tr></table>`))
		case r.URL.Path == "/genres/anime" && r.URL.Query().Get("filter") == "genres":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []jikanNamed{{MALID: 1, Name: "Action"}}})
		case r.URL.Path == "/genres/anime":
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"partial"}`))
		case r.URL.Path == "/anime/20/recommendations":
			recs.Add(1)
			t.Fatalf("filtered Explore should not fetch seed community recommendations")
		case r.URL.Path == "/anime/20/full":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": jikanAnime{MALID: 20, Title: "Naruto", Episodes: 220, Genres: []jikanNamed{{MALID: 1, Name: "Action"}}}})
		case r.URL.Path == "/anime" && r.URL.Query().Get("genres") == "1,10":
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"down"}`))
		case r.URL.Path == "/anime/genre/1":
			_, _ = w.Write([]byte(`<a href="https://myanimelist.net/anime/1535/Death_Note" title="Death Note"></a><a href="https://myanimelist.net/anime/24/School_Rumble" title="School Rumble"></a>`))
		case r.URL.Path == "/anime/genre/10":
			_, _ = w.Write([]byte(`<a href="https://myanimelist.net/anime/1535/Death_Note" title="Death Note"></a>`))
		case r.URL.Path == "/redirect":
			http.Redirect(w, r, "https://example.com/out", http.StatusFound)
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
	}))
	t.Cleanup(srv.Close)
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	feedback := []AnimeFeedback{}
	for i := 0; i < 8; i++ {
		feedback = append(feedback, AnimeFeedback{Anime: Anime{Title: "Missing " + strconv.Itoa(i)}, Value: "like", UpdatedAt: time.Date(2026, 10, 2, 12, 0, i, 0, time.UTC).Format(time.RFC3339)})
	}
	if result, err := meta.Explore(context.Background(), feedback, nil, nil, false); err != nil || len(result.Items) != 1 || result.Items[0].Anime.ImageURL != "https://img.example/cowboy.jpg" {
		t.Fatalf("capped Explore = %#v, %v", result, err)
	}
	if searches.Load() != 3 {
		t.Fatalf("detail search calls were not capped before fetch: %d", searches.Load())
	}
	if htmlSearches.Load() != 3 {
		t.Fatalf("HTML search fallback calls were not capped before fetch: %d", htmlSearches.Load())
	}
	if result, err := meta.Explore(context.Background(), nil, nil, nil, false); err != nil || len(result.Items) != 1 {
		t.Fatalf("cached HTML top Explore = %#v, %v", result, err)
	}
	if htmlTop.Load() != 1 {
		t.Fatalf("HTML fallback was not cached, top requests=%d", htmlTop.Load())
	}
	tags, err := meta.Tags(context.Background())
	if err != nil || len(tags) != 2 || tags[0].Name != "Action" || tags[1].Name != "Fantasy" {
		t.Fatalf("partial Tags should return usable data without error: %#v, %v", tags, err)
	}
	filtered, err := meta.Explore(context.Background(), []AnimeFeedback{{Anime: Anime{ID: "mal:20", Title: "Naruto", Source: "mal", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20}, Value: "like", UpdatedAt: "2026-10-02T12:00:00Z"}}, nil, []int{1, 10}, true)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Anime.Title != "Death Note" || recs.Load() != 0 {
		t.Fatalf("filtered Explore fallback = %#v, err=%v recs=%d", filtered, err, recs.Load())
	}
	if _, err := meta.fetchHTML(context.Background(), srv.URL+"/redirect"); err == nil {
		t.Fatalf("cross-host redirect was accepted")
	}
}

func TestMALMetadataCancellationDoesNotCacheFailure(t *testing.T) {
	var requests atomic.Int64
	block := make(chan struct{})
	var unblock sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-block
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []jikanAnime{malMetadataAnimeFixture(1, "Cowboy Bebop", "", nil, nil, nil)}})
	}))
	t.Cleanup(func() {
		unblock.Do(func() { close(block) })
		srv.Close()
	})
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := meta.Explore(ctx, nil, nil, nil, false); err == nil {
		t.Fatalf("canceled Explore returned nil error")
	}
	unblock.Do(func() { close(block) })
	if result, err := meta.Explore(context.Background(), nil, nil, nil, false); err != nil || len(result.Items) != 1 {
		t.Fatalf("post-cancel Explore = %#v, %v", result, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("canceled request should not have reached network or cached failure, requests=%d", requests.Load())
	}
}

func TestMALHTMLTagsClassifyAndCleanWithoutDuplicatingGlobalIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/genres/anime" && r.URL.Query().Get("filter") == "genres":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []jikanNamed{{MALID: 22, Name: "Romance"}, {MALID: 24, Name: "Sci-Fi"}}})
		case r.URL.Path == "/genres/anime":
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"down"}`))
		case r.URL.Path == "/anime.php":
			_, _ = w.Write([]byte(`<section><h3>Genres</h3><a href="/anime/genre/22/Romance">Romance (2,300)</a><a href="/anime/genre/24/Sci-Fi">Sci-Fi (1,234)</a></section><section><h3>Themes</h3><a href="/anime/genre/60/Idols_Female">Idols (Female) (123)</a><a href="/anime/genre/61/Idols_Male">Idols (Male)</a></section><section><h3>Demographics</h3><a href="/anime/genre/27/Shounen">Shounen (999)</a></section>`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
	}))
	t.Cleanup(srv.Close)
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	tags, err := meta.Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags returned error: %v", err)
	}
	byID := map[int]AnimeTag{}
	for _, tag := range tags {
		if _, ok := byID[tag.ID]; ok {
			t.Fatalf("duplicate tag id %d in %#v", tag.ID, tags)
		}
		byID[tag.ID] = tag
	}
	for id, want := range map[int]AnimeTag{
		22: {ID: 22, Name: "Romance", Kind: "genre"},
		24: {ID: 24, Name: "Sci-Fi", Kind: "genre"},
		60: {ID: 60, Name: "Idols (Female)", Kind: "theme"},
		61: {ID: 61, Name: "Idols (Male)", Kind: "theme"},
		27: {ID: 27, Name: "Shounen", Kind: "demographic"},
	} {
		if byID[id] != want {
			t.Fatalf("tag %d = %#v, want %#v; all=%#v", id, byID[id], want, tags)
		}
	}
}

func TestMALThinDislikeUsesFetchedTagsToLowerSimilarAnime(t *testing.T) {
	srv := malMetadataTestServer(t, map[string]any{
		"/anime/10/full": map[string]any{"data": malMetadataAnimeFixture(10, "Disliked Action", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil)},
		"/top/anime?filter=bypopularity": map[string]any{"data": []jikanAnime{
			malMetadataAnimeFixture(11, "Popular Action", "", []jikanNamed{{MALID: 1, Name: "Action"}}, nil, nil),
			malMetadataAnimeFixture(12, "Comedy Alternative", "", []jikanNamed{{MALID: 4, Name: "Comedy"}}, nil, nil),
		}},
	})
	meta := malMetadataTestClient(t, srv.URL, t.TempDir())
	feedback := []AnimeFeedback{{Anime: Anime{ID: "mal:10", Source: "mal", Title: "Disliked Action", URL: "https://myanimelist.net/anime/10", MALID: 10}, Value: "dislike"}}
	got, err := meta.Explore(context.Background(), feedback, nil, nil, false)
	if err != nil || len(got.Items) != 2 || got.Items[0].Anime.MALID != 12 {
		t.Fatalf("thin dislike did not affect similar candidates: %+v, %v", got, err)
	}
}

func TestMALMetadataLivePublicPages(t *testing.T) {
	if os.Getenv("GOANIME_LIVE_MAL") != "1" {
		t.Skip("set GOANIME_LIVE_MAL=1 to verify public MyAnimeList metadata fallbacks")
	}
	meta := NewMALMetadata(NewEpisodeTitleEnricher(), filepath.Join(t.TempDir(), "cache.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	tags, err := meta.Tags(ctx)
	if err != nil || len(tags) == 0 {
		t.Fatalf("live Tags = %d, %v", len(tags), err)
	}
	actionID := 0
	for _, tag := range tags {
		if tag.Kind == "genre" && tag.Name == "Action" {
			actionID = tag.ID
			break
		}
	}
	if actionID == 0 {
		t.Fatalf("live Tags did not include Action: %#v", tags[:min(5, len(tags))])
	}
	filtered, err := meta.Explore(ctx, nil, nil, []int{actionID}, true)
	if err != nil || len(filtered.Items) == 0 {
		t.Fatalf("live Action Explore = %d, %v warnings=%#v", len(filtered.Items), err, filtered.Warnings)
	}
	naruto := Anime{ID: "mal:20", Source: "mal", Title: "Naruto", URL: "https://myanimelist.net/anime/20/Naruto", MALID: 20, Tags: []AnimeTag{{ID: actionID, Name: "Action", Kind: "genre"}}}
	recs, err := meta.Explore(ctx, []AnimeFeedback{{Anime: naruto, Value: "like", UpdatedAt: time.Now().Format(time.RFC3339)}}, nil, nil, true)
	if err != nil || len(recs.Items) == 0 || !strings.Contains(recs.Items[0].Reason, "Naruto") {
		t.Fatalf("live Naruto recommendations = %d, %v warnings=%#v", len(recs.Items), err, recs.Warnings)
	}
	trailer, err := meta.Details(ctx, Anime{ID: "mal:44511", Source: "mal", Title: "Chainsaw Man", URL: "https://myanimelist.net/anime/44511/Chainsaw_Man", MALID: 44511})
	if err != nil || trailer.Trailer == nil || !malYouTubeIDRE.MatchString(trailer.Trailer.YouTubeID) {
		t.Fatalf("live trailer metadata = %#v, %v", trailer.Trailer, err)
	}
	t.Logf("live tags=%d action=%d actionItems=%d narutoRecs=%d trailer=%s", len(tags), actionID, len(filtered.Items), len(recs.Items), trailer.Trailer.YouTubeID)
}

func malMetadataTestClient(t *testing.T, baseURL, dir string) *MALMetadata {
	t.Helper()
	oldLimiter := jikanGlobalLimiter
	jikanGlobalLimiter = &episodeTitleRateLimiter{}
	t.Cleanup(func() { jikanGlobalLimiter = oldLimiter })
	enricher := episodeTitleTestEnricher(baseURL)
	return &MALMetadata{
		enricher: enricher,
		client:   &http.Client{Timeout: time.Second},
		baseURL:  baseURL,
		htmlBase: baseURL,
		now:      time.Now,
		cacheDir: filepath.Join(dir, "cache.json"),
		cache:    map[string]malMetadataCacheEntry{},
		flights:  map[string]*malMetadataFlight{},
		failures: map[string]time.Time{},
	}
}

func malMetadataTestServer(t *testing.T, routes map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := routes[r.URL.String()]
		if !ok {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func malMetadataAnimeFixture(id int, title, youtubeID string, genres, themes, demographics []jikanNamed) jikanAnime {
	item := jikanAnime{
		MALID:         id,
		URL:           "https://myanimelist.net/anime/" + strconv.Itoa(id),
		Title:         title,
		TitleEnglish:  title,
		TitleJapanese: title + " JP",
		TitleSynonyms: []string{title + " Synonym"},
		Type:          "TV",
		Episodes:      12,
		Score:         8.5,
		Synopsis:      title + " synopsis",
		Genres:        genres,
		Themes:        themes,
		Demographics:  demographics,
	}
	item.Images.JPG.ImageURL = "https://cdn.example/" + strconv.Itoa(id) + ".jpg"
	item.Trailer.YouTubeID = youtubeID
	return item
}
