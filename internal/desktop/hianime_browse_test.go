package desktop

import (
	"context"
	"fmt"
	"html"
	"os"
	"strings"
	"testing"
)

func hiAnimeBrowseTestCards(items ...Anime) string {
	var cards strings.Builder
	for _, anime := range items {
		fmt.Fprintf(&cards, `<div class="flw-item"><h3 class="film-name"><a href="%s">%s</a></h3><img class="film-poster-img" src="https://cdn.anipixcdn.co/poster.webp"><div class="description">%s</div></div>`, html.EscapeString(anime.URL), html.EscapeString(anime.Title), html.EscapeString(anime.Description))
	}
	return cards.String()
}

func TestHiAnimeBrowseRealCatalogCards(t *testing.T) {
	client, fixture := hiAnimeTestClient(t)
	fixture.bodies["hianime.at/most-popular"] = hiAnimeTestSearch
	items, err := client.Browse(context.Background(), "/most-popular")
	if err != nil || len(items) != 1 || items[0].ID == "" || items[0].EpisodeCount != 220 {
		t.Fatalf("browse = %#v, %v", items, err)
	}
	for _, path := range []string{"/genre/action", "/genres/../search", "/genres/action?redirect=http://localhost", "https://evil.example/most-popular"} {
		count := len(fixture.requests)
		if _, err := client.Browse(context.Background(), path); err == nil || len(fixture.requests) != count {
			t.Fatalf("invalid browse path made a request: %q", path)
		}
	}
	fixture.bodies["hianime.at/genres/action"] = `<div id="main-content"><div class="film_list-wrap"></div></div>`
	if _, err := client.Browse(context.Background(), "/genres/action"); err == nil {
		t.Fatal("empty provider page was accepted as a catalog")
	}
}

func TestHiAnimeDetailGenresAndScopedRecommendations(t *testing.T) {
	client, fixture := hiAnimeTestClient(t)
	fixture.bodies["hianime.at/naruto-1335"] = `<div id="ani_detail"><div class="film-description"><div class="text">A <b>ninja</b> adventure.</div></div><div class="anisc-info"><div class="item-list"><a href="/genres/action">Action</a><a href="https://hianime.at/genres/action">Action</a><a href="https://evil.example/genres/fantasy">Untrusted</a><a href="/studios/studio-pierrot">Studio Pierrot</a><a href="/genres/martial-arts">Martial Arts</a></div></div></div><div id="main-content"><section class="block_area"><h2 class="cat-heading">Recommended For You</h2><div class="film_list-wrap">` + hiAnimeBrowseTestCards(Anime{Title: "Bleach", URL: "https://hianime.at/bleach-806"}) + `</div></section><section class="block_area"><h2 class="cat-heading">Most Popular</h2><div class="film_list-wrap">` + hiAnimeBrowseTestCards(Anime{Title: "Unrelated", URL: "https://hianime.at/unrelated-99"}) + `</div></section></div>`
	detail, err := client.Detail(context.Background(), hiAnimeTestAnime)
	if err != nil || len(detail.Genres) != 2 || detail.Genres[1].Slug != "martial-arts" || len(detail.Related) != 1 || detail.Related[0].Title != "Bleach" || detail.Anime.Description != "A ninja adventure." {
		t.Fatalf("details = %#v, %v", detail, err)
	}
	before := len(fixture.requests)
	if _, err := client.Detail(context.Background(), Anime{Source: "hianime", URL: "https://evil.example/naruto-1335"}); err == nil || len(fixture.requests) != before {
		t.Fatal("untrusted detail request was allowed")
	}
}

func TestHiAnimeExploreLive(t *testing.T) {
	if os.Getenv("GOANIME_TEST_EXPLORE_LIVE") != "1" {
		t.Skip("set GOANIME_TEST_EXPLORE_LIVE=1 to verify current provider discovery")
	}
	ctx := context.Background()
	r := NewRecommender()
	cold, err := r.Explore(ctx, nil, nil, false)
	if err != nil || len(cold.Items) < 10 {
		t.Fatalf("live cold start: %d items, %v, warnings %v", len(cold.Items), err, cold.Warnings)
	}
	client := newHiAnimeClient()
	items, err := client.Search(ctx, "Naruto")
	if err != nil || len(items) == 0 {
		t.Fatalf("live Naruto search: %v", err)
	}
	var seed Anime
	for _, anime := range items {
		if anime.Title == "Naruto" {
			seed = anime
			break
		}
	}
	if seed.Title == "" {
		t.Fatal("live Naruto seed unavailable")
	}
	detail, err := client.Detail(ctx, seed)
	if err != nil || len(detail.Genres) == 0 || len(detail.Related) == 0 {
		t.Fatalf("live detail genres/related: %#v, %v", detail, err)
	}
	personal, err := r.Explore(ctx, []AnimeFeedback{{Anime: seed, Value: "like", UpdatedAt: "2026-10-02T12:00:00Z"}}, nil, false)
	if err != nil || !personal.Personalized || len(personal.Items) == 0 || personal.Items[0].Reason != "Because you liked Naruto" {
		t.Fatalf("live personalization: %#v, %v", personal, err)
	}
	t.Logf("live popular/airing = %d real titles; Naruto has %d genres/%d related shows; personalized = %d titles; first = %s (%s); warnings = %v", len(cold.Items), len(detail.Genres), len(detail.Related), len(personal.Items), personal.Items[0].Anime.Title, personal.Items[0].Reason, personal.Warnings)
}
