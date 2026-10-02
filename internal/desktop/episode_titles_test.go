package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEpisodeTitleEnricherExactNaruto(t *testing.T) {
	srv, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}},
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}, {MALID: 2, Title: "My Name is Konohamaru!"}}},
		},
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{
		{Number: "1", Title: "Episode 1", URL: "https://example.invalid/1"},
		{Number: "2", Title: "", URL: "https://example.invalid/2"},
	})
	if got[0].Title != "Enter: Naruto Uzumaki!" || got[1].Title != "My Name is Konohamaru!" {
		t.Fatalf("titles were not enriched: %#v", got)
	}
	if counts.search.Load() != 1 || counts.episodes.Load() != 1 {
		t.Fatalf("unexpected request counts: search=%d episodes=%d", counts.search.Load(), counts.episodes.Load())
	}
}

func TestEpisodeTitleEnricherUsesHiAnimeMALIDBeforeSearch(t *testing.T) {
	var hiAnimeRequests atomic.Int64
	hiAnime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hiAnimeRequests.Add(1)
		if r.URL.Path != "/api/theme/episode/servers" || r.URL.Query().Get("episodeId") != "22676" {
			t.Fatalf("unexpected HiAnime request: %s", r.URL.String())
		}
		hash := base64.StdEncoding.EncodeToString([]byte("https://zokoanime.video/stream/mal/20/1/sub"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true,
			"html":   `<div class="server-item" data-hash="` + hash + `"></div>`,
		})
	}))
	t.Cleanup(hiAnime.Close)

	jikan, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{{MALID: 1735, Title: "Naruto: Shippuuden", Episodes: 500, Type: "TV"}},
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}}},
		},
	})
	enricher := episodeTitleTestEnricher(jikan.URL)
	enricher.hiAnimeBase = hiAnime.URL
	enricher.hiAnimeClient = hiAnime.Client()

	got := enricher.Enrich(context.Background(),
		Anime{Source: "hianime", Title: "Naruto", URL: "https://hianime.at/naruto-1335", EpisodeCount: 220},
		[]Episode{{Number: "1", Title: "Episode 1", URL: "https://hianime.at/watch/naruto-1335?ep=22676"}},
	)
	if got[0].Title != "Enter: Naruto Uzumaki!" {
		t.Fatalf("HiAnime MAL id title was not applied: %#v", got)
	}
	if hiAnimeRequests.Load() != 1 || counts.search.Load() != 0 || counts.episodes.Load() != 1 {
		t.Fatalf("unexpected request counts: hianime=%d search=%d episodes=%d", hiAnimeRequests.Load(), counts.search.Load(), counts.episodes.Load())
	}
}

func TestEpisodeTitleEnricherRejectsHiAnimeRedirectAndAmbiguousMALIDs(t *testing.T) {
	for _, test := range []struct {
		name        string
		hiAnimeHTML func() string
		redirect    bool
	}{
		{
			name:     "redirect",
			redirect: true,
		},
		{
			name: "ambiguous mal ids",
			hiAnimeHTML: func() string {
				first := base64.StdEncoding.EncodeToString([]byte("https://zokoanime.video/stream/mal/20/1/sub"))
				second := base64.StdEncoding.EncodeToString([]byte("https://zokoanime.video/stream/mal/1735/1/sub"))
				return `<div class="server-item" data-hash="` + first + `"></div><div class="server-item" data-hash="` + second + `"></div>`
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			hiAnime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.redirect {
					http.Redirect(w, r, "https://evil.example/servers", http.StatusFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "html": test.hiAnimeHTML()})
			}))
			t.Cleanup(hiAnime.Close)
			jikan, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
				search: []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}},
				pages: map[int]episodeTitlePage{
					1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}}},
				},
			})
			enricher := episodeTitleTestEnricher(jikan.URL)
			enricher.hiAnimeBase = hiAnime.URL
			enricher.hiAnimeClient = hiAnime.Client()
			got := enricher.Enrich(context.Background(),
				Anime{Source: "hianime", Title: "Naruto", URL: "https://hianime.at/naruto-1335", EpisodeCount: 220},
				[]Episode{{Number: "1", Title: "Episode 1", URL: "https://hianime.at/watch/naruto-1335?ep=22676"}},
			)
			if got[0].Title != "Enter: Naruto Uzumaki!" {
				t.Fatalf("strict search fallback did not recover after rejected HiAnime MAL route: %#v", got)
			}
			if counts.search.Load() != 1 {
				t.Fatalf("expected fallback search after rejected HiAnime MAL route, got %d", counts.search.Load())
			}
		})
	}
}

func TestEpisodeTitleEnricherUsesKnownMALIDWhenJikanSearchUnavailable(t *testing.T) {
	srv, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		searchStatus: http.StatusGatewayTimeout,
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}}},
		},
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{{Number: "1", Title: "Episode 1"}})
	if got[0].Title != "Enter: Naruto Uzumaki!" {
		t.Fatalf("known MAL id fallback did not enrich title: %#v", got)
	}
	if counts.search.Load() != 1 || counts.episodes.Load() != 1 {
		t.Fatalf("unexpected request counts: search=%d episodes=%d", counts.search.Load(), counts.episodes.Load())
	}
}

func TestEpisodeTitleEnricherUsesWikipediaWhenJikanEpisodesUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	var wikiRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/anime":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"search failed"}`))
		case strings.HasPrefix(r.URL.Path, "/anime/1735/episodes"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"episodes failed"}`))
		case r.URL.Path == "/List_of_Naruto:_Shippuden_episodes":
			wikiRequests.Add(1)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<table><tr class="vevent module-episode-list-row"><th id="ep1">1</th><td class="summary">"Homecoming"<br>Transliteration: "Kikyō"</td></tr><tr class="vevent module-episode-list-row"><th id="ep2">2</th><td class="summary">"The Akatsuki Makes Its Move"</td></tr></table>`))
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	enricher := episodeTitleTestEnricher(srv.URL)
	enricher.wikipediaBase = srv.URL
	enricher.now = func() time.Time { return now }
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto: Shippuuden", EpisodeCount: 500}, []Episode{
		{Number: "1", Title: "Episode 1"},
		{Number: "2", Title: "Episode 2"},
	})
	if got[0].Title != "Homecoming" || got[1].Title != "The Akatsuki Makes Its Move" {
		t.Fatalf("Wikipedia fallback did not enrich Shippuuden titles: %#v", got)
	}
	if wikiRequests.Load() != 1 {
		t.Fatalf("expected one wiki request, got %d", wikiRequests.Load())
	}
	now = now.Add(episodeTitlePartialTTL + time.Second)
	got = enricher.Enrich(context.Background(), Anime{Title: "Naruto: Shippuuden", EpisodeCount: 500}, []Episode{{Number: "1", Title: "Episode 1"}})
	if got[0].Title != "Homecoming" || wikiRequests.Load() != 2 {
		t.Fatalf("partial Wikipedia cache did not retry after short ttl: %#v requests=%d", got, wikiRequests.Load())
	}
}

func TestEpisodeTitleEnricherMergesWikipediaWhenJikanKnownSeriesPartial(t *testing.T) {
	var wikiRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/anime":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}}})
		case strings.HasPrefix(r.URL.Path, "/anime/20/episodes"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []episodeTitleItem{{MALID: 1, Title: "Jikan Episode One"}},
				"pagination": map[string]any{
					"has_next_page":     false,
					"last_visible_page": 1,
				},
			})
		case r.URL.Path == "/List_of_Naruto_episodes":
			wikiRequests.Add(1)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<table><tr class="vevent module-episode-list-row"><th id="ep1">1</th><td class="summary">"Wiki Episode One"</td></tr><tr class="vevent module-episode-list-row"><th id="ep220">220</th><td class="summary">"Departure"</td></tr></table>`))
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	enricher := episodeTitleTestEnricher(srv.URL)
	enricher.wikipediaBase = srv.URL
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{
		{Number: "1", Title: "Episode 1"},
		{Number: "220", Title: "Episode 220"},
	})
	if got[0].Title != "Jikan Episode One" || got[1].Title != "Departure" {
		t.Fatalf("known-series merge did not preserve Jikan and fill missing Wiki title: %#v", got)
	}
	if wikiRequests.Load() != 1 {
		t.Fatalf("expected one wiki merge request, got %d", wikiRequests.Load())
	}
}

func TestEpisodeTitleEnricherPreservesRealTitlesAndURLs(t *testing.T) {
	srv, _ := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}},
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}, {MALID: 2, Title: "My Name is Konohamaru!"}}},
		},
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{
		{Number: "1", Title: "Provider Real Title", URL: "https://play.example/one"},
		{Number: "2", Title: "Episode 2", URL: "https://play.example/two"},
	})
	if got[0].Title != "Provider Real Title" || got[0].URL != "https://play.example/one" || got[1].URL != "https://play.example/two" {
		t.Fatalf("existing title or URLs changed: %#v", got)
	}
	if got[1].Title != "My Name is Konohamaru!" {
		t.Fatalf("generic title was not enriched: %#v", got)
	}
}

func TestEpisodeTitleEnricherRejectsWrongSeasonSpinoffAndOVA(t *testing.T) {
	srv, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{
			{MALID: 1735, Title: "Naruto: Shippuuden", Episodes: 500, Type: "TV"},
			{MALID: 34566, Title: "Boruto: Naruto Next Generations", Episodes: 293, Type: "TV"},
			{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"},
			{MALID: 442, Title: "Naruto", Episodes: 1, Type: "OVA"},
		},
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Should Not Be Used"}}},
		},
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 12}, []Episode{{Number: "1", Title: "Episode 1"}})
	if got[0].Title != "Episode 1" {
		t.Fatalf("wrong-season metadata was applied: %#v", got)
	}
	if counts.episodes.Load() != 0 {
		t.Fatalf("episode endpoint should not be called for rejected search match")
	}
}

func TestEpisodeTitleEnricherPaginatesEnoughForNaruto220(t *testing.T) {
	pages := map[int]episodeTitlePage{}
	for page := 1; page <= 3; page++ {
		items := []episodeTitleItem{}
		for i := 1; i <= 100; i++ {
			n := (page-1)*100 + i
			items = append(items, episodeTitleItem{MALID: n, Title: "Episode Title " + strconv.Itoa(n)})
		}
		pages[page] = episodeTitlePage{Items: items, HasNext: page < 3, LastPage: 3}
	}
	srv, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}},
		pages:  pages,
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{
		{Number: "1", Title: "Episode 1"},
		{Number: "150", Title: "Episode 150"},
		{Number: "220", Title: "Episode 220"},
	})
	if got[0].Title != "Episode Title 1" || got[1].Title != "Episode Title 150" || got[2].Title != "Episode Title 220" {
		t.Fatalf("paged titles were not applied: %#v", got)
	}
	if counts.episodes.Load() != 3 {
		t.Fatalf("expected three episode pages, got %d", counts.episodes.Load())
	}
}

func TestEpisodeTitleEnricherCachesPartialOutage(t *testing.T) {
	srv, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}},
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}}, HasNext: true, LastPage: 2},
			2: {Status: http.StatusBadGateway},
		},
		wiki: map[string]string{
			"/List_of_Naruto_episodes": `<table><tr class="vevent module-episode-list-row"><th id="ep2">2</th><td class="summary">"My Name Is Konohamaru!"</td></tr></table>`,
		},
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	enricher.wikipediaBase = srv.URL
	episodes := []Episode{{Number: "1", Title: "Episode 1"}, {Number: "2", Title: "Episode 2"}}
	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if got[0].Title != "Enter: Naruto Uzumaki!" || got[1].Title != "My Name Is Konohamaru!" {
		t.Fatalf("partial metadata handling failed: %#v", got)
	}
	got = enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if got[0].Title != "Enter: Naruto Uzumaki!" || got[1].Title != "My Name Is Konohamaru!" || counts.search.Load() != 1 || counts.episodes.Load() != 2 {
		t.Fatalf("partial success was not cached: %#v search=%d episodes=%d", got, counts.search.Load(), counts.episodes.Load())
	}
}

func TestEpisodeTitleEnricherCanceledContextReturnsOriginal(t *testing.T) {
	enricher := episodeTitleTestEnricher("https://metadata.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	episodes := []Episode{{Number: "1", Title: "Episode 1", URL: "https://play.example/1"}}
	got := enricher.Enrich(ctx, Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if got[0] != episodes[0] {
		t.Fatalf("canceled enrich changed episode: %#v", got)
	}
}

func TestEpisodeTitleEnricherCanceledRequestDoesNotCacheNegative(t *testing.T) {
	var blockSearch atomic.Bool
	var searches atomic.Int64
	blockSearch.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/anime":
			searches.Add(1)
			if blockSearch.Load() {
				select {
				case <-time.After(100 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}}})
		case strings.HasPrefix(r.URL.Path, "/anime/20/episodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}},
				"pagination": map[string]any{
					"has_next_page":     false,
					"last_visible_page": 1,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	enricher := episodeTitleTestEnricher(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	got := enricher.Enrich(ctx, Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{{Number: "1", Title: "Episode 1"}})
	if got[0].Title != "Episode 1" {
		t.Fatalf("canceled request changed title: %#v", got)
	}

	blockSearch.Store(false)
	got = enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{{Number: "1", Title: "Episode 1"}})
	if got[0].Title != "Enter: Naruto Uzumaki!" {
		t.Fatalf("healthy retry after cancellation did not enrich: %#v", got)
	}
	if searches.Load() < 2 {
		t.Fatalf("canceled request appears to have poisoned cache; searches=%d", searches.Load())
	}
}

func TestEpisodeTitleEnricherCoalescesConcurrentCallsAndCachesMisses(t *testing.T) {
	srv, counts := episodeTitleTestServer(t, episodeTitleTestConfig{
		searchDelay: 50 * time.Millisecond,
		search:      []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}},
		pages: map[int]episodeTitlePage{
			1: {Items: []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}}},
		},
	})
	enricher := episodeTitleTestEnricher(srv.URL)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, []Episode{{Number: "1", Title: "Episode 1"}})
			if got[0].Title != "Enter: Naruto Uzumaki!" {
				t.Errorf("title was not enriched: %#v", got)
			}
		}()
	}
	wg.Wait()
	if counts.search.Load() != 1 || counts.episodes.Load() != 1 {
		t.Fatalf("requests were not coalesced: search=%d episodes=%d", counts.search.Load(), counts.episodes.Load())
	}

	missServer, missCounts := episodeTitleTestServer(t, episodeTitleTestConfig{
		search: []episodeTitleSearchItem{{MALID: 1, Title: "Boruto", Episodes: 293, Type: "TV"}},
	})
	miss := episodeTitleTestEnricher(missServer.URL)
	episodes := []Episode{{Number: "1", Title: "Episode 1"}}
	miss.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	miss.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if missCounts.search.Load() != 1 {
		t.Fatalf("negative cache did not suppress repeated search: %d", missCounts.search.Load())
	}
}

func TestEpisodeTitleEnricherPartialPageCacheRetriesSoon(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	var page2OK atomic.Bool
	var searches atomic.Int64
	var episodeRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/anime":
			w.Header().Set("Content-Type", "application/json")
			searches.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []episodeTitleSearchItem{{MALID: 20, Title: "Naruto", Episodes: 220, Type: "TV"}}})
		case strings.HasPrefix(r.URL.Path, "/anime/20/episodes"):
			w.Header().Set("Content-Type", "application/json")
			episodeRequests.Add(1)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page == 2 && !page2OK.Load() {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"message":"temporary failure"}`))
				return
			}
			items := []episodeTitleItem{{MALID: 1, Title: "Enter: Naruto Uzumaki!"}}
			if page == 2 {
				items = []episodeTitleItem{{MALID: 2, Title: "My Name is Konohamaru!"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": items,
				"pagination": map[string]any{
					"has_next_page":     page < 2,
					"last_visible_page": 2,
				},
			})
		case r.URL.Path == "/List_of_Naruto_episodes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<table><tr class="vevent module-episode-list-row"><th id="ep2">2</th><td class="summary">"My Name Is Konohamaru!"</td></tr></table>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	enricher := episodeTitleTestEnricher(srv.URL)
	enricher.wikipediaBase = srv.URL
	enricher.now = func() time.Time { return now }
	episodes := []Episode{{Number: "1", Title: "Episode 1"}, {Number: "2", Title: "Episode 2"}}

	got := enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if got[0].Title != "Enter: Naruto Uzumaki!" || got[1].Title != "My Name Is Konohamaru!" {
		t.Fatalf("partial first response wrong: %#v", got)
	}
	page2OK.Store(true)
	got = enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if got[1].Title != "My Name Is Konohamaru!" || searches.Load() != 1 || episodeRequests.Load() != 2 {
		t.Fatalf("partial cache should be reused briefly: %#v searches=%d episodes=%d", got, searches.Load(), episodeRequests.Load())
	}
	now = now.Add(episodeTitlePartialTTL + time.Second)
	got = enricher.Enrich(context.Background(), Anime{Title: "Naruto", EpisodeCount: 220}, episodes)
	if got[0].Title != "Enter: Naruto Uzumaki!" || got[1].Title != "My Name is Konohamaru!" {
		t.Fatalf("partial cache did not retry after short ttl: %#v", got)
	}
	if searches.Load() != 2 || episodeRequests.Load() != 4 {
		t.Fatalf("unexpected retry counts: searches=%d episodes=%d", searches.Load(), episodeRequests.Load())
	}
}

func TestEpisodeTitleEnricherLiveJikanNarutoAndShippuden(t *testing.T) {
	if os.Getenv("GOANIME_LIVE_JIKAN") == "" {
		t.Skip("set GOANIME_LIVE_JIKAN=1 to verify live Jikan episode-title enrichment")
	}
	enricher := NewEpisodeTitleEnricher()
	for _, test := range []struct {
		title string
		count int
	}{
		{title: "Naruto", count: 220},
		{title: "Naruto: Shippuuden", count: 500},
	} {
		got := enricher.Enrich(context.Background(), Anime{Title: test.title, EpisodeCount: test.count}, []Episode{
			{Number: "1", Title: "Episode 1"},
			{Number: "2", Title: "Episode 2"},
			{Number: strconv.Itoa(test.count), Title: "Episode " + strconv.Itoa(test.count)},
		})
		for _, episode := range got {
			if episodeTitleNeedsEnrichment(episode) {
				t.Fatalf("live %s titles were not enriched: %#v", test.title, got)
			}
		}
		t.Logf("%s: first=%q, last=%q", test.title, got[0].Title, got[len(got)-1].Title)
	}
}

type episodeTitleSearchItem struct {
	MALID    int      `json:"mal_id"`
	Title    string   `json:"title"`
	Episodes int      `json:"episodes"`
	Type     string   `json:"type"`
	Synonyms []string `json:"title_synonyms,omitempty"`
}

type episodeTitleItem struct {
	MALID int    `json:"mal_id"`
	Title string `json:"title"`
}

type episodeTitlePage struct {
	Items    []episodeTitleItem
	HasNext  bool
	LastPage int
	Status   int
}

type episodeTitleTestConfig struct {
	search       []episodeTitleSearchItem
	searchStatus int
	pages        map[int]episodeTitlePage
	wiki         map[string]string
	searchDelay  time.Duration
}

type episodeTitleTestCounts struct {
	search   atomic.Int64
	episodes atomic.Int64
}

func episodeTitleTestServer(t *testing.T, cfg episodeTitleTestConfig) (*httptest.Server, *episodeTitleTestCounts) {
	t.Helper()
	counts := &episodeTitleTestCounts{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/anime":
			counts.search.Add(1)
			if cfg.searchDelay > 0 {
				time.Sleep(cfg.searchDelay)
			}
			if cfg.searchStatus != 0 {
				w.WriteHeader(cfg.searchStatus)
				_, _ = w.Write([]byte(`{"message":"search failed"}`))
				return
			}
			if r.URL.Query().Get("q") == "" || r.URL.Query().Get("limit") == "" {
				t.Errorf("search query missing expected parameters: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": cfg.search})
		case strings.HasPrefix(r.URL.Path, "/anime/20/episodes"):
			counts.episodes.Add(1)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page == 0 {
				page = 1
			}
			data := cfg.pages[page]
			if data.Status != 0 {
				w.WriteHeader(data.Status)
				_, _ = w.Write([]byte(`{"message":"failure"}`))
				return
			}
			lastPage := data.LastPage
			if lastPage == 0 {
				lastPage = page
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": data.Items,
				"pagination": map[string]any{
					"has_next_page":     data.HasNext,
					"last_visible_page": lastPage,
				},
			})
		case cfg.wiki[r.URL.Path] != "":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(cfg.wiki[r.URL.Path]))
		default:
			t.Errorf("unexpected metadata request: %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, counts
}

func episodeTitleTestEnricher(baseURL string) *EpisodeTitleEnricher {
	return &EpisodeTitleEnricher{
		client:      &http.Client{Timeout: time.Second},
		baseURL:     baseURL,
		rateLimiter: &episodeTitleRateLimiter{},
		now:         time.Now,
		cache:       map[string]episodeTitleCacheEntry{},
		inflight:    map[string]*episodeTitleCall{},
	}
}
