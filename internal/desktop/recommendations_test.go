package desktop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func recommendationTestAnime(title string, number string) Anime {
	return Anime{ID: "show-" + number, Title: title, URL: hiAnimeBaseURL + "/" + strings.ToLower(strings.ReplaceAll(title, " ", "-")) + "-" + number, Source: "hianime", Language: "EN", Genres: []string{}}
}

type recommendationFixture struct {
	mu      sync.Mutex
	pages   map[string][]Anime
	details map[string]hiAnimeDetail
	failure bool
	calls   map[string]int
}

func newRecommendationFixture() (*Recommender, *recommendationFixture) {
	r := NewRecommender()
	f := &recommendationFixture{pages: map[string][]Anime{}, details: map[string]hiAnimeDetail{}, calls: map[string]int{}}
	r.browse = func(ctx context.Context, path string) ([]Anime, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls[path]++
		if f.failure {
			return nil, errors.New("provider unavailable")
		}
		return f.pages[path], nil
	}
	r.detail = func(ctx context.Context, anime Anime) (hiAnimeDetail, error) {
		if err := ctx.Err(); err != nil {
			return hiAnimeDetail{}, err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls[anime.URL]++
		if f.failure {
			return hiAnimeDetail{}, errors.New("provider unavailable")
		}
		if detail, ok := f.details[anime.URL]; ok {
			return detail, nil
		}
		return hiAnimeDetail{Anime: anime}, nil
	}
	return r, f
}

func recommendationItemIndex(result ExploreResult, title string) int {
	for i, item := range result.Items {
		if item.Anime.Title == title {
			return i
		}
	}
	return -1
}

func TestExploreColdStartDeduplicatesAndHidesKnownTitles(t *testing.T) {
	r, f := newRecommendationFixture()
	a, b, c := recommendationTestAnime("Alpha", "1"), recommendationTestAnime("Beta", "2"), recommendationTestAnime("Gamma", "3")
	copyA := recommendationTestAnime("ALPHA", "4")
	f.pages["/most-popular"] = []Anime{a, b}
	f.pages["/top-airing"] = []Anime{copyA, c}
	result, err := r.Explore(context.Background(), nil, []Anime{{ID: "other-provider", Title: " beta! "}}, false)
	if err != nil || result.Personalized || len(result.Items) != 2 || result.Items[0].Anime.Title != "Alpha" || result.Items[0].Reason != "Popular on HiAnime" || recommendationItemIndex(result, "Gamma") == -1 {
		t.Fatalf("cold discovery = %#v, %v", result, err)
	}
}

func TestExploreLikeChangesRankingAndCandidatePool(t *testing.T) {
	r, f := newRecommendationFixture()
	a, b, seed, related, genreShow := recommendationTestAnime("Alpha", "1"), recommendationTestAnime("Beta", "2"), recommendationTestAnime("Favorite", "3"), recommendationTestAnime("Related", "4"), recommendationTestAnime("Adventure", "5")
	seed.Genres = []string{"Action"}
	f.pages["/most-popular"] = []Anime{a, b, seed}
	f.pages["/top-airing"] = []Anime{a}
	f.pages["/genres/action"] = []Anime{genreShow}
	f.details[seed.URL] = hiAnimeDetail{Anime: seed, Genres: []hiAnimeGenre{{Name: "Action", Slug: "action"}}, Related: []Anime{b, related}}
	cold, err := r.Explore(context.Background(), nil, nil, false)
	if err != nil || cold.Items[0].Anime.Title != "Alpha" {
		t.Fatalf("cold = %#v, %v", cold, err)
	}
	personal, err := r.Explore(context.Background(), []AnimeFeedback{{Anime: seed, Value: "like"}}, nil, false)
	if err != nil || !personal.Personalized || recommendationItemIndex(personal, "Favorite") != -1 || recommendationItemIndex(personal, "Related") == -1 || recommendationItemIndex(personal, "Adventure") == -1 || recommendationItemIndex(personal, "Beta") >= recommendationItemIndex(personal, "Alpha") || personal.Items[0].Reason != "Because you liked Favorite" {
		t.Fatalf("personalized = %#v, %v", personal, err)
	}
	if f.calls["/most-popular"] != 1 || f.calls["/top-airing"] != 1 {
		t.Fatalf("feedback unnecessarily reloaded base catalogs: %#v", f.calls)
	}
	_, _ = r.Explore(context.Background(), []AnimeFeedback{{Anime: seed, Value: "like"}}, nil, false)
	if f.calls[seed.URL] != 1 || f.calls["/genres/action"] != 1 {
		t.Fatalf("cached feedback details reloaded: %#v", f.calls)
	}
}

func TestExploreDislikeExcludesTitleAndDownranksSimilarThenUndo(t *testing.T) {
	r, f := newRecommendationFixture()
	a, b, seed := recommendationTestAnime("Alpha", "1"), recommendationTestAnime("Beta", "2"), recommendationTestAnime("Unwanted", "3")
	f.pages["/most-popular"] = []Anime{a, b, seed}
	f.pages["/top-airing"] = []Anime{a}
	f.details[seed.URL] = hiAnimeDetail{Anime: seed, Related: []Anime{a}}
	negative, err := r.Explore(context.Background(), []AnimeFeedback{{Anime: seed, Value: "dislike"}}, nil, false)
	if err != nil || !negative.Personalized || recommendationItemIndex(negative, "Unwanted") != -1 || negative.Items[0].Anime.Title != "Beta" {
		t.Fatalf("negative feedback = %#v, %v", negative, err)
	}
	undo, err := r.Explore(context.Background(), nil, nil, false)
	if err != nil || undo.Personalized || undo.Items[0].Anime.Title != "Alpha" || recommendationItemIndex(undo, "Unwanted") == -1 {
		t.Fatalf("undo did not restore cold ordering: %#v, %v", undo, err)
	}
}

func TestExploreUsesLatestFeedbackAndExcludesCrossSourceCopies(t *testing.T) {
	r, f := newRecommendationFixture()
	seed, other := recommendationTestAnime("Favorite", "1"), recommendationTestAnime("Other", "2")
	f.pages["/most-popular"] = []Anime{seed, other, recommendationTestAnime("FAVORITE!", "3")}
	f.pages["/top-airing"] = []Anime{}
	feedback := []AnimeFeedback{{Anime: seed, Value: "like", UpdatedAt: "2026-10-01T12:00:00Z"}, {Anime: seed, Value: "dislike", UpdatedAt: "2026-10-02T12:00:00Z"}, {Anime: other, Value: "unsupported"}}
	canonical := recommendationFeedback(feedback)
	if len(canonical) != 1 || canonical[0].Value != "dislike" {
		t.Fatalf("old feedback overrode latest: %#v", canonical)
	}
	result, err := r.Explore(context.Background(), feedback, nil, false)
	if err != nil || len(result.Items) != 1 || result.Items[0].Anime.Title != "Other" {
		t.Fatalf("rated duplicates leaked into discovery: %#v, %v", result, err)
	}
}

func TestRecommendationFeedbackOrdersSubsecondDatesChronologically(t *testing.T) {
	feedback := []AnimeFeedback{
		{Anime: recommendationTestAnime("Whole second", "1"), Value: "like", UpdatedAt: "2026-10-02T12:00:00Z"},
		{Anime: recommendationTestAnime("Nine tenths", "2"), Value: "like", UpdatedAt: "2026-10-02T12:00:00.9Z"},
		{Anime: recommendationTestAnime("Ninety one", "3"), Value: "like", UpdatedAt: "2026-10-02T12:00:00.91Z"},
		{Anime: recommendationTestAnime("Nine thousand one", "4"), Value: "like", UpdatedAt: "2026-10-02T12:00:00.9001Z"},
	}
	ordered := recommendationFeedback(feedback)
	for i, expected := range []string{"Ninety one", "Nine thousand one", "Nine tenths", "Whole second"} {
		if ordered[i].Anime.Title != expected {
			t.Fatalf("chronological feedback[%d] = %q, want %q", i, ordered[i].Anime.Title, expected)
		}
	}
	seed := recommendationTestAnime("Duplicate", "5")
	latest := recommendationFeedback([]AnimeFeedback{
		{Anime: seed, Value: "like", UpdatedAt: "2026-10-02T12:00:00Z"},
		{Anime: seed, Value: "like", UpdatedAt: "2026-10-02T12:00:00.9Z"},
		{Anime: seed, Value: "dislike", UpdatedAt: "2026-10-02T12:00:00.91Z"},
	})
	if len(latest) != 1 || latest[0].Value != "dislike" {
		t.Fatalf("subsecond older feedback won duplicate priority: %#v", latest)
	}
	r, f := newRecommendationFixture()
	f.pages["/most-popular"] = []Anime{recommendationTestAnime("Unrated", "6")}
	if _, err := r.Explore(context.Background(), feedback, nil, false); err != nil {
		t.Fatal(err)
	}
	if f.calls[feedback[0].Anime.URL] != 0 || f.calls[feedback[1].Anime.URL] != 1 || f.calls[feedback[2].Anime.URL] != 1 || f.calls[feedback[3].Anime.URL] != 1 {
		t.Fatalf("recent seed cap used lexical dates: %#v", f.calls)
	}
}

func TestRecommendationFeedbackMalformedDateFallback(t *testing.T) {
	valid := recommendationTestAnime("Valid", "1")
	invalidFirst, invalidLast := recommendationTestAnime("Invalid first", "2"), recommendationTestAnime("Invalid last", "3")
	feedback := []AnimeFeedback{
		{Anime: valid, Value: "dislike", UpdatedAt: "zzzz-not-a-date"},
		{Anime: invalidFirst, Value: "like", UpdatedAt: "invalid-a"},
		{Anime: valid, Value: "like", UpdatedAt: "2020-01-01T00:00:00Z"},
		{Anime: invalidLast, Value: "like", UpdatedAt: "invalid-z"},
	}
	ordered := recommendationFeedback(feedback)
	if len(ordered) != 3 || ordered[0].Anime.Title != "Valid" || ordered[0].Value != "like" || ordered[1].Anime.Title != "Invalid last" || ordered[2].Anime.Title != "Invalid first" {
		t.Fatalf("malformed date fallback = %#v", ordered)
	}
	for left, right := 0, len(feedback)-1; left < right; left, right = left+1, right-1 {
		feedback[left], feedback[right] = feedback[right], feedback[left]
	}
	again := recommendationFeedback(feedback)
	for i := range ordered {
		if again[i].Anime.Title != ordered[i].Anime.Title || again[i].Value != ordered[i].Value {
			t.Fatalf("invalid date fallback changed with input order: %#v / %#v", ordered, again)
		}
	}
}

func TestExploreOutageRetainsCachedFeedAndReranks(t *testing.T) {
	r, f := newRecommendationFixture()
	a, b, seed := recommendationTestAnime("Alpha", "1"), recommendationTestAnime("Beta", "2"), recommendationTestAnime("Favorite", "3")
	f.pages["/most-popular"] = []Anime{a, b}
	f.pages["/top-airing"] = []Anime{a}
	f.details[seed.URL] = hiAnimeDetail{Anime: seed, Related: []Anime{b}}
	feedback := []AnimeFeedback{{Anime: seed, Value: "like"}}
	if _, err := r.Explore(context.Background(), feedback, nil, false); err != nil {
		t.Fatal(err)
	}
	f.failure = true
	result, err := r.Explore(context.Background(), feedback, nil, true)
	if err != nil || len(result.Items) != 2 || result.Items[0].Anime.Title != "Beta" || len(result.Warnings) != 3 || !strings.Contains(result.Warnings[0], "cached results") {
		t.Fatalf("cached outage = %#v, %v", result, err)
	}
	uncached, failing := newRecommendationFixture()
	failing.failure = true
	result, err = uncached.Explore(context.Background(), nil, nil, false)
	if err == nil || len(result.Items) != 0 || len(result.Warnings) != 2 {
		t.Fatalf("uncached outage invented feed: %#v, %v", result, err)
	}
}

func TestExploreCancellationAndDeadlineCachedResults(t *testing.T) {
	r, f := newRecommendationFixture()
	f.pages["/most-popular"] = []Anime{recommendationTestAnime("Alpha", "1")}
	f.pages["/top-airing"] = []Anime{recommendationTestAnime("Beta", "2")}
	if _, err := r.Explore(context.Background(), nil, nil, false); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Explore(canceled, nil, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	r.browse = func(ctx context.Context, _ string) ([]Anime, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, end := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer end()
	result, err := r.Explore(ctx, nil, nil, true)
	if err != nil || len(result.Items) != 2 || len(result.Warnings) != 2 {
		t.Fatalf("deadline erased cached feed: %#v, %v", result, err)
	}
}

func TestExploreBoundsConcurrentRequestsAndRecentDetailSeeds(t *testing.T) {
	r := NewRecommender()
	var active, maximum, details atomic.Int32
	work := func(ctx context.Context) error {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
		}
		select {
		case <-time.After(5 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.browse = func(ctx context.Context, path string) ([]Anime, error) {
		return []Anime{recommendationTestAnime("Unrated", "100")}, work(ctx)
	}
	r.detail = func(ctx context.Context, anime Anime) (hiAnimeDetail, error) {
		details.Add(1)
		return hiAnimeDetail{Anime: anime}, work(ctx)
	}
	feedback := []AnimeFeedback{}
	for i := 0; i < 10; i++ {
		value := "like"
		if i >= 5 {
			value = "dislike"
		}
		feedback = append(feedback, AnimeFeedback{Anime: recommendationTestAnime("Show "+strconv.Itoa(i+1), strconv.Itoa(i+1)), Value: value})
	}
	if _, err := r.Explore(context.Background(), feedback, nil, false); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() > 3 || details.Load() != 5 {
		t.Fatalf("unbounded requests: %d concurrent, %d details", maximum.Load(), details.Load())
	}
}

func TestExploreCacheExpiryAndConcurrentCoalescing(t *testing.T) {
	r := NewRecommender()
	clock := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return clock }
	var calls atomic.Int32
	r.browse = func(ctx context.Context, path string) ([]Anime, error) {
		calls.Add(1)
		select {
		case <-time.After(10 * time.Millisecond):
			return []Anime{recommendationTestAnime("Alpha", "1")}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result, err := r.Explore(context.Background(), nil, nil, false); err != nil || len(result.Items) != 1 {
				t.Errorf("coalesced request = %#v, %v", result, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("concurrent requests repeated provider fetches: %d", calls.Load())
	}
	clock = clock.Add(recommendationCacheTTL + time.Second)
	if _, err := r.Explore(context.Background(), nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("expired catalog was never refreshed: %d", calls.Load())
	}
}
