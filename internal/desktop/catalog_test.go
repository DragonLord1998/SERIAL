package desktop

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alvarorichard/Goanime/internal/api/source"
	"github.com/alvarorichard/Goanime/internal/models"
	"github.com/alvarorichard/Goanime/internal/scraper"
	"github.com/alvarorichard/Goanime/internal/util"
)

func TestCatalogSourceMapping(t *testing.T) {
	for _, item := range []struct {
		id   string
		kind source.SourceKind
	}{
		{"anidb", source.AniDB}, {"AniDB", source.AniDB}, {"anidb.app", source.AniDB},
		{"animefire", source.AnimeFire}, {"Animefire.io", source.AnimeFire},
	} {
		kind, _, err := catalogSource(item.id)
		if err != nil || kind != item.kind {
			t.Fatalf("source %q: kind=%q err=%v", item.id, kind, err)
		}
	}
	for _, id := range []string{"", "allanime", "superflix", "goyabu"} {
		if _, _, err := catalogSource(id); err == nil {
			t.Fatalf("unsafe or unknown source %q was accepted", id)
		}
	}
}

func TestCatalogSearchMetadataAndStableIdentity(t *testing.T) {
	c := NewCatalog()
	model := &models.Anime{
		Name: "[English] <b>Cowboy &amp; Bebop</b>", URL: "https://anidb.app/anime/bebop-1?season=one", Source: "AniDB",
		ImageURL: "/covers/bebop.jpg", Details: models.AniListDetails{
			Description: "<p>Space &amp; music.</p><script>bad()</script><br>New adventures.",
			Genres:      []string{"Action"}, AverageScore: 86, Episodes: 26,
		},
	}
	c.search = func(_ context.Context, query string, kinds ...source.SourceKind) ([]*models.Anime, error) {
		if query != "cowboy" || !reflect.DeepEqual(kinds, []source.SourceKind{source.AniDB}) {
			t.Fatalf("query=%q kinds=%v", query, kinds)
		}
		return []*models.Anime{model, model, nil, {Name: "Unsupported", Source: "SuperFlix"}}, nil
	}
	items, err := c.Search(context.Background(), " cowboy ", "anidb")
	if err != nil || len(items) != 1 {
		t.Fatalf("search: items=%v err=%v", items, err)
	}
	got := items[0]
	if got.Title != "Cowboy & Bebop" || got.Description != "Space & music. New adventures." {
		t.Fatalf("unfiltered HTML or lost text: %#v", got)
	}
	if got.URL != model.URL || got.ImageURL != "https://anidb.app/covers/bebop.jpg" || got.Source != "anidb" || got.Language != "EN" {
		t.Fatalf("identity or metadata changed: %#v", got)
	}
	if got.Score != 8.6 || got.EpisodeCount != 26 || !reflect.DeepEqual(got.Genres, []string{"Action"}) {
		t.Fatalf("metadata missing: %#v", got)
	}
	model.Name = "Renamed title"
	second, err := catalogAnime(model)
	if err != nil || second.ID != got.ID {
		t.Fatalf("ID depends on mutable title: %q != %q (%v)", second.ID, got.ID, err)
	}
	model.Source = "Animefire.io"
	model.URL = "https://animefire.io/animes/bebop"
	other, err := catalogAnime(model)
	if err != nil || other.ID == got.ID {
		t.Fatalf("ID is not source-specific: %q (%v)", other.ID, err)
	}
}

func TestCatalogEpisodesNumericSpecialsAndExactURLs(t *testing.T) {
	c := NewCatalog()
	c.episodes = func(_ context.Context, anime *models.Anime) ([]models.Episode, error) {
		if anime.Source != "Animefire.io" {
			t.Fatalf("canonical provider source lost: %q", anime.Source)
		}
		return []models.Episode{
			{Number: "10", URL: "https://animefire.io/ep/10?token=a"},
			{Number: "2", Title: models.TitleDetails{English: "<b>Second</b>"}},
			{Number: "Special 1"}, {Number: "1.5"}, {Number: "OVA"},
		}, nil
	}
	items, err := c.Episodes(context.Background(), Anime{Source: "animefire", URL: "https://animefire.io/animes/show"})
	if err != nil {
		t.Fatal(err)
	}
	numbers := []string{}
	for _, item := range items {
		numbers = append(numbers, item.Number)
	}
	if !reflect.DeepEqual(numbers, []string{"Special 1", "1.5", "2", "10", "OVA"}) {
		t.Fatalf("episode order: %v", numbers)
	}
	if items[2].Title != "Second" || items[3].URL != "https://animefire.io/ep/10?token=a" {
		t.Fatalf("episode mapping: %#v", items)
	}
}

func TestCatalogCancellation(t *testing.T) {
	c := NewCatalog()
	c.search = func(context.Context, string, ...source.SourceKind) ([]*models.Anime, error) {
		t.Fatal("pre-canceled search invoked a provider")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, "show", "anidb"); !errors.Is(err, context.Canceled) {
		t.Fatalf("search cancellation: %v", err)
	}
	if _, err := c.Episodes(ctx, Anime{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("episodes cancellation: %v", err)
	}
	if _, err := c.Resolve(ctx, Anime{}, Episode{}, Settings{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolve cancellation: %v", err)
	}

	started, finish := make(chan struct{}), make(chan struct{})
	c.episodes = func(context.Context, *models.Anime) ([]models.Episode, error) {
		close(started)
		<-finish
		return nil, nil
	}
	defer close(finish)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Episodes(ctx, Anime{Source: "animefire", URL: "https://animefire.io/animes/show"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("GUI waited for non-contextual provider after cancellation")
	}
}

type catalogFakeAdapter struct {
	getStream func(string, ...any) (string, map[string]string, error)
}

func (*catalogFakeAdapter) SearchAnime(string, ...any) ([]*models.Anime, error) { return nil, nil }
func (*catalogFakeAdapter) GetAnimeEpisodes(string) ([]models.Episode, error)   { return nil, nil }
func (a *catalogFakeAdapter) GetStreamURL(raw string, options ...any) (string, map[string]string, error) {
	return a.getStream(raw, options...)
}
func (*catalogFakeAdapter) GetType() scraper.ScraperType { return scraper.AniDBType }

func TestCatalogResolvePreservesHeadersAndAudioMode(t *testing.T) {
	t.Setenv("GOANIME_ANIDB_LANG", "original")
	c := NewCatalog()
	c.newAdapter = func(kind scraper.ScraperType) (scraper.UnifiedScraper, error) {
		if kind != scraper.AniDBType {
			t.Fatalf("adapter kind=%v", kind)
		}
		return &catalogFakeAdapter{getStream: func(raw string, options ...any) (string, map[string]string, error) {
			if raw != "https://anidb.app/episode/42" || options[0] != "720p" || os.Getenv("GOANIME_ANIDB_LANG") != "dub" {
				t.Errorf("stream identity/preferences lost: %q %v lang=%q", raw, options, os.Getenv("GOANIME_ANIDB_LANG"))
			}
			util.SetGlobalSubtitles([]util.SubtitleInfo{{URL: "https://cdn.test/en.vtt", Language: "en", Label: "English"}})
			return "https://cdn.test/master.m3u8?token=keep", map[string]string{"referer": "https://anidb.app/", "audio_lang": "eng", "user_agent": "provider-agent"}, nil
		}}, nil
	}
	stream, err := c.Resolve(context.Background(), Anime{Source: "anidb", URL: "https://anidb.app/anime/show-1"},
		Episode{URL: "https://anidb.app/episode/42"}, Settings{Mode: "dub", Quality: "720p"})
	if err != nil {
		t.Fatal(err)
	}
	if stream.URL != "https://cdn.test/master.m3u8?token=keep" || stream.Headers["Referer"] != "https://anidb.app/" || stream.Headers["User-Agent"] != "provider-agent" || len(stream.Subtitles) != 1 {
		t.Fatalf("stream metadata: %#v", stream)
	}
	if os.Getenv("GOANIME_ANIDB_LANG") != "original" {
		t.Fatal("audio environment override leaked after resolution")
	}
}

func TestCatalogRejectsUnavailableDub(t *testing.T) {
	c := NewCatalog()
	c.newAdapter = func(scraper.ScraperType) (scraper.UnifiedScraper, error) {
		return &catalogFakeAdapter{getStream: func(string, ...any) (string, map[string]string, error) {
			return "https://cdn.test/video.m3u8", map[string]string{"audio_lang": "jpn"}, nil
		}}, nil
	}
	_, err := c.Resolve(context.Background(), Anime{Source: "anidb", URL: "https://anidb.app/anime/show-1"}, Episode{URL: "https://anidb.app/episode/1"}, Settings{Mode: "dub"})
	if err == nil || !strings.Contains(err.Error(), "does not offer dub") {
		t.Fatalf("provider's silent sub fallback accepted: %v", err)
	}
	_, err = c.Resolve(context.Background(), Anime{Source: "animefire", URL: "https://animefire.io/animes/show"}, Episode{URL: "https://animefire.io/episode/1"}, Settings{Mode: "dub"})
	if err == nil || !strings.Contains(err.Error(), "Dublado") {
		t.Fatalf("dub on unlabeled AnimeFire result accepted: %v", err)
	}
}

type catalogRoundTrip func(*http.Request) (*http.Response, error)

func (f catalogRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAnimeFireJSONResolutionWithoutNetworkOrPrompts(t *testing.T) {
	c := NewCatalog()
	c.client = &http.Client{Transport: catalogRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://animefire.io/video/show/1?token=keep" || req.Header.Get("Referer") != "https://animefire.io" {
			t.Errorf("JSON request lost URL or authorization headers: %v", req)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"src":"https://cdn.test/360.mp4","label":"360p"},{"src":"https://cdn.test/1080.mp4?token=keep","label":"1080p"},{"src":"https://cdn.test/720.mp4","label":"720p"}]}`))}, nil
	})}
	url, err := c.resolveAnimeFire(context.Background(), "https://animefire.io/video/show/1?token=keep", "best", map[string]string{"Referer": "https://animefire.io"})
	if err != nil || url != "https://cdn.test/1080.mp4?token=keep" {
		t.Fatalf("best quality: %q %v", url, err)
	}
	url, err = c.resolveAnimeFire(context.Background(), "https://animefire.io/video/show/1?token=keep", "720p", map[string]string{"Referer": "https://animefire.io"})
	if err != nil || url != "https://cdn.test/720.mp4" {
		t.Fatalf("explicit quality: %q %v", url, err)
	}
	if _, err := catalogSelectVideo([]catalogVideoSource{{Src: "https://cdn.test/720.mp4", Label: "720p"}}, "1080p"); err == nil {
		t.Fatal("unavailable quality silently ignored")
	}
}

func TestCatalogProviderErrorsReachGUI(t *testing.T) {
	c := NewCatalog()
	c.hiAnime = nil
	want := errors.New("provider is unavailable")
	c.search = func(context.Context, string, ...source.SourceKind) ([]*models.Anime, error) { return nil, want }
	if _, err := c.Search(context.Background(), "show", "all"); !errors.Is(err, want) {
		t.Fatalf("provider error lost: %v", err)
	}
	if _, err := c.Search(context.Background(), "show", "superflix"); err == nil {
		t.Fatal("interactive source reached search")
	}
}

func TestCatalogRejectsFrontendURLsOutsideSelectedSource(t *testing.T) {
	c := NewCatalog()
	c.episodes = func(context.Context, *models.Anime) ([]models.Episode, error) {
		t.Fatal("untrusted URL reached provider")
		return nil, nil
	}
	for _, raw := range []string{
		"https://evil.test/anime/show", "http://127.0.0.1/show", "https://anidb.app.evil.test/show",
		"https://animefire.io/animes/show", "https://anidb.app:9000/anime/show", "https://user:secret@anidb.app/show",
	} {
		if _, err := c.Episodes(context.Background(), Anime{Source: "anidb", URL: raw}); err == nil {
			t.Fatalf("URL outside selected provider accepted: %q", raw)
		}
	}
	if _, err := c.Resolve(context.Background(), Anime{Source: "anidb", URL: "https://anidb.app/anime/show-1"},
		Episode{URL: "https://animefire.io/episode/1"}, Settings{}); err == nil {
		t.Fatal("episode from another provider accepted")
	}
}

type catalogProviderStub struct {
	search func(context.Context, string) ([]Anime, error)
}

func (p catalogProviderStub) Search(ctx context.Context, query string) ([]Anime, error) {
	return p.search(ctx, query)
}
func (catalogProviderStub) Episodes(context.Context, Anime) ([]Episode, error) {
	return []Episode{{Number: "1"}}, nil
}
func (catalogProviderStub) Resolve(context.Context, Anime, Episode, Settings) (Stream, error) {
	return Stream{URL: "https://media.example/video.m3u8"}, nil
}

func TestCatalogWorkingSourceSurvivesProviderOutages(t *testing.T) {
	c := NewCatalog()
	c.hiAnime = catalogProviderStub{search: func(_ context.Context, query string) ([]Anime, error) {
		if query != "naruto" {
			t.Errorf("query lost: %q", query)
		}
		anime := Anime{ID: "hianime-naruto", Title: "Naruto", Source: "hianime"}
		return []Anime{anime, anime}, nil
	}}
	c.search = func(_ context.Context, _ string, kinds ...source.SourceKind) ([]*models.Anime, error) {
		if len(kinds) != 1 {
			t.Errorf("sources were not isolated: %v", kinds)
		}
		if kinds[0] == source.AniDB {
			return nil, errors.New("upstream unavailable with HTTP 503")
		}
		return nil, errors.New("blocked or challenged with HTTP 403")
	}
	result, err := c.SearchWithStatus(context.Background(), " naruto ", "all")
	if err != nil || len(result.Animes) != 1 || len(result.Warnings) != 2 {
		t.Fatalf("usable results were lost: %+v, %v", result, err)
	}
	if result.Warnings[0].Source != "anidb" || !strings.Contains(result.Warnings[0].Message, "503") || result.Warnings[1].Source != "animefire" {
		t.Fatalf("provider failures were not reported: %+v", result.Warnings)
	}
	result, err = c.SearchWithStatus(context.Background(), "naruto", "hianime")
	if err != nil || len(result.Animes) != 1 || len(result.Warnings) != 0 {
		t.Fatalf("selected source leaked other failures: %+v, %v", result, err)
	}
	anime := result.Animes[0]
	if episodes, err := c.Episodes(context.Background(), anime); err != nil || len(episodes) != 1 {
		t.Fatalf("episodes did not route to new source: %v, %v", episodes, err)
	}
	if stream, err := c.Resolve(context.Background(), anime, Episode{}, Settings{}); err != nil || stream.URL == "" {
		t.Fatalf("playback did not route to new source: %+v, %v", stream, err)
	}
}

func TestCatalogEmptySearchIsNotProviderFailure(t *testing.T) {
	c := NewCatalog()
	c.hiAnime = catalogProviderStub{search: func(context.Context, string) ([]Anime, error) { return []Anime{}, nil }}
	c.search = func(context.Context, string, ...source.SourceKind) ([]*models.Anime, error) {
		return nil, errors.New("no results found for: unknown")
	}
	result, err := c.SearchWithStatus(context.Background(), "unknown", "all")
	if err != nil || len(result.Animes) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("normal empty result became an outage: %+v, %v", result, err)
	}
}

func TestCatalogTimeoutKeepsResultsFromRespondingSource(t *testing.T) {
	c := NewCatalog()
	c.hiAnime = catalogProviderStub{search: func(context.Context, string) ([]Anime, error) {
		return []Anime{{ID: "hianime-show", Title: "Working result", Source: "hianime"}}, nil
	}}
	c.search = func(ctx context.Context, _ string, _ ...source.SourceKind) ([]*models.Anime, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := c.SearchWithStatus(ctx, "show", "all")
	if err != nil || len(result.Animes) != 1 || len(result.Warnings) != 2 {
		t.Fatalf("secondary source timeout discarded usable results: %+v, %v", result, err)
	}
}
