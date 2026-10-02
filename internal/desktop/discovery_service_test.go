package desktop

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMALAnimeRequiresOneExactSourceMatch(t *testing.T) {
	mal := Anime{ID: "mal:16498", Source: "mal", Title: "Shingeki no Kyojin", URL: "https://myanimelist.net/anime/16498", MALID: 16498, EpisodeCount: 25}
	srv := malMetadataTestServer(t, map[string]any{
		"/anime/16498/full": map[string]any{"data": jikanAnime{MALID: 16498, Title: "Shingeki no Kyojin", TitleEnglish: "Attack on Titan", Episodes: 25, Genres: []jikanNamed{{MALID: 1, Name: "Action"}}}},
	})
	for _, test := range []struct {
		name       string
		candidates []Anime
		want       bool
	}{
		{"verified alias", []Anime{{ID: "hianime-aot", Source: "hianime", Title: "Attack on Titan", URL: "https://hianime.at/attack-on-titan-112", EpisodeCount: 25}}, true},
		{"different season", []Anime{{ID: "hianime-aot2", Source: "hianime", Title: "Attack on Titan Season 2", EpisodeCount: 12}}, false},
		{"wrong episode count", []Anime{{ID: "hianime-aot", Source: "hianime", Title: "Attack on Titan", EpisodeCount: 12}}, false},
		{"ambiguous sources", []Anime{{ID: "hianime-aot", Source: "hianime", Title: "Attack on Titan", EpisodeCount: 25}, {ID: "hianime-other", Source: "hianime", Title: "Shingeki no Kyojin", EpisodeCount: 25}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{ctx: context.Background(), catalog: NewCatalog(), metadata: malMetadataTestClient(t, srv.URL, t.TempDir())}
			service.catalog.hiAnime = catalogProviderStub{search: func(context.Context, string) ([]Anime, error) { return test.candidates, nil }}
			got, err := service.ResolveAnime(mal)
			if (err == nil) != test.want {
				t.Fatalf("resolve = %+v, %v", got, err)
			}
			if test.want && (got.Source != "hianime" || got.ID != "hianime-aot" || got.MALID != 16498 || len(got.Tags) == 0 || !strings.Contains(got.URL, "hianime.at")) {
				t.Fatalf("source identity or metadata lost: %+v", got)
			}
		})
	}
}

func TestResolveMALAnimeChecksAllAliasesAndSourceFailures(t *testing.T) {
	mal := Anime{ID: "mal:16498", Source: "mal", Title: "Shingeki no Kyojin", URL: "https://myanimelist.net/anime/16498", MALID: 16498}
	srv := malMetadataTestServer(t, map[string]any{
		"/anime/16498/full": map[string]any{"data": jikanAnime{MALID: 16498, Title: "Shingeki no Kyojin", TitleEnglish: "Attack on Titan"}},
	})
	service := &Service{ctx: context.Background(), catalog: NewCatalog(), metadata: malMetadataTestClient(t, srv.URL, t.TempDir())}
	service.catalog.hiAnime = catalogProviderStub{search: func(_ context.Context, query string) ([]Anime, error) {
		return []Anime{{ID: "hianime-" + query, Source: "hianime", Title: query}}, nil
	}}
	if _, err := service.ResolveAnime(mal); err == nil {
		t.Fatal("alias search conflict was ignored")
	}
	service.catalog.hiAnime = catalogProviderStub{search: func(context.Context, string) ([]Anime, error) { return nil, errors.New("HTTP 503") }}
	if _, err := service.ResolveAnime(mal); err == nil || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatalf("source outage misreported: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	service.ctx = canceled
	if _, err := service.ResolveAnime(mal); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestStoredAnimeDistinctMALIdentitiesDoNotShareFeedback(t *testing.T) {
	left := Anime{ID: "mal:1", MALID: 1, Title: "A Remake"}
	right := Anime{ID: "mal:2", MALID: 2, Title: "A Remake"}
	if sameStoredAnime(left, right) {
		t.Fatal("distinct MAL identities collapsed by title")
	}
}

func TestCatalogueTelevisionLabelAndKnownAliasesMatch(t *testing.T) {
	mal := Anime{ID: "mal:40748", MALID: 40748, Title: "Jujutsu Kaisen", AlternateTitles: []string{"Sorcery Fight"}}
	provider := Anime{ID: "hianime-jujutsu", Title: "Jujutsu Kaisen (TV)"}
	if !sameStoredAnime(mal, provider) || episodeTitleNormalize(mal.Title) != episodeTitleNormalize(provider.Title) {
		t.Fatal("TV format suffix prevented catalogue identity matching")
	}
	keys := recommendationKeys(mal)
	found := false
	for _, key := range keys {
		if key == "title:sorcery fight" {
			found = true
		}
	}
	if !found {
		t.Fatal("known alternate title missing from recommendation exclusion")
	}
	if episodeTitleNormalize("Jujutsu Kaisen Season 2") == episodeTitleNormalize(mal.Title) || episodeTitleNormalize("Hunter x Hunter (2011)") == episodeTitleNormalize("Hunter x Hunter") {
		t.Fatal("a distinguishing season or year was removed")
	}
}

func TestMALFeedbackAndSavedAliasPersistWithoutDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := Anime{ID: "hianime-test", Title: "Attack on Titan", URL: "https://hianime.at/attack-on-titan-112", Source: "hianime", MALID: 16498, Tags: []AnimeTag{{ID: 1, Name: "Action", Kind: "genre"}}, Trailer: &AnimeTrailer{YouTubeID: "MGRm4IzK1SQ", URL: "https://www.youtube.com/watch?v=MGRm4IzK1SQ"}}
	mal := Anime{ID: "mal:16498", Title: "Shingeki no Kyojin", URL: "https://myanimelist.net/anime/16498", Source: "mal", MALID: 16498}
	if _, err := service.SaveAnime(provider, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetFeedback(provider, "like"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetFeedback(mal, "dislike"); err != nil {
		t.Fatal(err)
	}
	if got := service.Snapshot(); len(got.Feedback) != 1 || got.Feedback[0].Value != "dislike" {
		t.Fatalf("alias duplicated feedback: %+v", got.Feedback)
	}
	if _, err := service.SaveAnime(mal, false); err != nil {
		t.Fatal(err)
	}
	if len(service.Snapshot().Saved) != 0 {
		t.Fatal("MAL alias could not remove saved provider title")
	}
	if _, err := service.SaveAnime(provider, true); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	snapshot.Saved[0].Tags[0].Name = "Mutated"
	snapshot.Saved[0].Trailer.YouTubeID = "invalid"
	if got := service.Snapshot().Saved[0]; got.Tags[0].Name != "Action" || got.Trailer.YouTubeID != "MGRm4IzK1SQ" {
		t.Fatal("metadata snapshot leaked mutable state")
	}
	service.Close()
	reopened, err := NewService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Snapshot(); len(got.Feedback) != 1 || got.Feedback[0].Anime.MALID != 16498 || len(got.Saved) != 1 || got.Saved[0].Tags[0].Name != "Action" {
		t.Fatal("MAL metadata and feedback did not survive restart")
	}
}
