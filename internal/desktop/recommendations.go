package desktop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const recommendationCacheTTL = 20 * time.Minute

type recommendationPage struct {
	items  []Anime
	detail hiAnimeDetail
}

type recommendationCacheEntry struct {
	page recommendationPage
	at   time.Time
}

type recommendationFlight struct {
	done chan struct{}
	page recommendationPage
	err  error
}

// Recommender keeps only real provider responses in memory. Preferences remain
// in the caller's local store; reranking does not require uploading feedback.
type Recommender struct {
	mu      sync.Mutex
	cache   map[string]recommendationCacheEntry
	flights map[string]*recommendationFlight
	slots   chan struct{}
	browse  func(context.Context, string) ([]Anime, error)
	detail  func(context.Context, Anime) (hiAnimeDetail, error)
	now     func() time.Time
}

func NewRecommender() *Recommender {
	client := newHiAnimeClient()
	return &Recommender{
		cache: map[string]recommendationCacheEntry{}, flights: map[string]*recommendationFlight{},
		slots: make(chan struct{}, 3), browse: client.Browse, detail: client.Detail, now: time.Now,
	}
}

type recommendationRequest struct {
	key   string
	label string
	load  func(context.Context) (recommendationPage, error)
}

type recommendationResponse struct {
	page recommendationPage
	err  error
}

func (r *Recommender) fetch(ctx context.Context, request recommendationRequest, refresh bool) (recommendationPage, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.mu.Lock()
			old, cached := r.cache[request.key]
			r.mu.Unlock()
			if cached {
				return old.page, fmt.Errorf("using cached results: %w", err)
			}
		}
		return recommendationPage{}, err
	}
	r.mu.Lock()
	old, cached := r.cache[request.key]
	if cached && !refresh && r.now().Sub(old.at) < recommendationCacheTTL {
		r.mu.Unlock()
		return old.page, nil
	}
	if flight := r.flights[request.key]; flight != nil {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			if cached && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return old.page, fmt.Errorf("using cached results: %w", ctx.Err())
			}
			return recommendationPage{}, ctx.Err()
		case <-flight.done:
			return flight.page, flight.err
		}
	}
	flight := &recommendationFlight{done: make(chan struct{})}
	r.flights[request.key] = flight
	r.mu.Unlock()
	select {
	case r.slots <- struct{}{}:
		flight.page, flight.err = request.load(ctx)
		<-r.slots
	case <-ctx.Done():
		flight.err = ctx.Err()
	}
	r.mu.Lock()
	if flight.err == nil {
		r.cache[request.key] = recommendationCacheEntry{page: flight.page, at: r.now()}
		if len(r.cache) > 128 {
			oldestKey := ""
			var oldest time.Time
			for key, entry := range r.cache {
				if oldestKey == "" || entry.at.Before(oldest) || (entry.at.Equal(oldest) && key < oldestKey) {
					oldestKey, oldest = key, entry.at
				}
			}
			delete(r.cache, oldestKey)
		}
	} else if cached && !errors.Is(ctx.Err(), context.Canceled) {
		flight.page = old.page
		flight.err = fmt.Errorf("using cached results: %w", flight.err)
	}
	delete(r.flights, request.key)
	close(flight.done)
	r.mu.Unlock()
	return flight.page, flight.err
}

func (r *Recommender) batch(ctx context.Context, requests []recommendationRequest, refresh bool) []recommendationResponse {
	out := make([]recommendationResponse, len(requests))
	var wg sync.WaitGroup
	for i, request := range requests {
		wg.Add(1)
		go func(i int, request recommendationRequest) {
			defer wg.Done()
			out[i].page, out[i].err = r.fetch(ctx, request, refresh)
		}(i, request)
	}
	wg.Wait()
	return out
}

func (r *Recommender) browseRequest(path, label string) recommendationRequest {
	return recommendationRequest{key: "browse:" + path, label: label, load: func(ctx context.Context) (recommendationPage, error) {
		items, err := r.browse(ctx, path)
		return recommendationPage{items: items}, err
	}}
}

type recommendationSeed struct {
	feedback AnimeFeedback
	detail   hiAnimeDetail
	related  map[string]bool
}

type recommendationCandidate struct {
	anime  Anime
	base   float64
	score  float64
	reason string
}

// Explore combines popularity and airing lists with related shows and genres
// from recent explicit likes. Dislikes suppress their title and reduce similar
// candidates. All ordering is deterministic for a given catalog and feedback.
func (r *Recommender) Explore(ctx context.Context, feedback []AnimeFeedback, known []Anime, refresh bool) (ExploreResult, error) {
	result := ExploreResult{Items: []ExploreItem{}, Warnings: []string{}, Message: "Popular and currently airing anime. Like or dislike titles to make this yours."}
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return result, err
	}
	feedback = recommendationFeedback(feedback)
	result.Personalized = len(feedback) > 0
	excluded := map[string]bool{}
	for _, anime := range known {
		for _, key := range recommendationKeys(anime) {
			excluded[key] = true
		}
	}
	seeds := make([]recommendationSeed, len(feedback))
	requests := []recommendationRequest{r.browseRequest("/most-popular", "Popular"), r.browseRequest("/top-airing", "Currently airing")}
	detailIndexes := []int{}
	likes, dislikes := 0, 0
	for i, item := range feedback {
		for _, key := range recommendationKeys(item.Anime) {
			excluded[key] = true
		}
		seeds[i] = recommendationSeed{feedback: item, detail: hiAnimeDetail{Anime: item.Anime}, related: map[string]bool{}}
		if _, _, err := hiAnimeIdentity(item.Anime); err != nil {
			continue
		}
		if item.Value == "like" {
			if likes >= 3 {
				continue
			}
			likes++
		} else {
			if dislikes >= 2 {
				continue
			}
			dislikes++
		}
		anime := item.Anime
		requests = append(requests, recommendationRequest{key: "detail:" + anime.URL, label: "Similar to " + anime.Title, load: func(ctx context.Context) (recommendationPage, error) {
			detail, err := r.detail(ctx, anime)
			return recommendationPage{detail: detail}, err
		}})
		detailIndexes = append(detailIndexes, i)
	}
	responses := r.batch(ctx, requests, refresh)
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return result, err
	}
	for i, response := range responses {
		if response.err != nil {
			result.Warnings = append(result.Warnings, requests[i].label+": "+response.err.Error())
		}
		if i >= 2 && response.page.detail.Anime.Title != "" {
			seed := &seeds[detailIndexes[i-2]]
			seed.detail = response.page.detail
			for _, anime := range seed.detail.Related {
				for _, key := range recommendationKeys(anime) {
					seed.related[key] = true
				}
			}
		}
	}
	// Genre browse pages provide verified genre membership without fetching every
	// candidate's detail page. Favor genres shared by several positive signals.
	type genreVote struct {
		genre hiAnimeGenre
		votes int
	}
	votes := map[string]genreVote{}
	for _, seed := range seeds {
		if seed.feedback.Value != "like" {
			continue
		}
		for _, genre := range seed.detail.Genres {
			vote := votes[genre.Slug]
			vote.genre = genre
			vote.votes++
			votes[genre.Slug] = vote
		}
	}
	genres := make([]genreVote, 0, len(votes))
	for _, vote := range votes {
		genres = append(genres, vote)
	}
	sort.Slice(genres, func(i, j int) bool {
		if genres[i].votes != genres[j].votes {
			return genres[i].votes > genres[j].votes
		}
		return genres[i].genre.Slug < genres[j].genre.Slug
	})
	if len(genres) > 2 {
		genres = genres[:2]
	}
	genreRequests := make([]recommendationRequest, len(genres))
	for i, vote := range genres {
		genreRequests[i] = r.browseRequest("/genres/"+vote.genre.Slug, vote.genre.Name)
	}
	genreResponses := r.batch(ctx, genreRequests, refresh)
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return result, err
	}
	candidates := []recommendationCandidate{}
	aliases := map[string]int{}
	add := func(anime Anime, base float64, reason string) {
		keys := recommendationKeys(anime)
		if strings.TrimSpace(anime.Title) == "" || len(keys) == 0 {
			return
		}
		for _, key := range keys {
			if excluded[key] {
				return
			}
		}
		index := len(candidates)
		for _, key := range keys {
			if existing, ok := aliases[key]; ok {
				index = existing
				break
			}
		}
		if index == len(candidates) {
			anime.Genres = append([]string{}, anime.Genres...)
			candidates = append(candidates, recommendationCandidate{anime: anime, base: base, reason: reason})
		} else {
			candidate := &candidates[index]
			candidate.anime.Genres = recommendationMergeGenres(candidate.anime.Genres, anime.Genres)
			if candidate.anime.Description == "" {
				candidate.anime.Description = anime.Description
			}
			if base > candidate.base {
				candidate.base, candidate.reason = base, reason
			}
		}
		for _, key := range keys {
			aliases[key] = index
		}
	}
	available := 0
	for i := 0; i < 2; i++ {
		available += len(responses[i].page.items)
		for rank, anime := range responses[i].page.items {
			reason, base := "Popular on HiAnime", 10.0
			if i == 1 {
				reason, base = "Currently airing", 8
			}
			add(anime, base-float64(rank)*0.12, reason)
		}
	}
	for _, seed := range seeds {
		if seed.feedback.Value == "like" {
			available += len(seed.detail.Related)
			for _, anime := range seed.detail.Related {
				add(anime, 5, "Because you liked "+seed.feedback.Anime.Title)
			}
		}
	}
	for i, response := range genreResponses {
		if response.err != nil {
			result.Warnings = append(result.Warnings, genreRequests[i].label+": "+response.err.Error())
		}
		available += len(response.page.items)
		for _, anime := range response.page.items {
			anime.Genres = recommendationMergeGenres(anime.Genres, []string{genres[i].genre.Name})
			add(anime, 5, genres[i].genre.Name+" from your likes")
		}
	}
	for i := range candidates {
		candidate := &candidates[i]
		candidate.score = candidate.base
		bestPositive := 0.0
		for _, seed := range seeds {
			contribution := 0.0
			for _, key := range recommendationKeys(candidate.anime) {
				if seed.related[key] {
					contribution = 24
					break
				}
			}
			contribution += 2 * float64(recommendationGenreOverlap(candidate.anime.Genres, seed.detail.Anime.Genres))
			contribution += 6 * recommendationTextSimilarity(candidate.anime, seed.detail.Anime)
			if seed.feedback.Value == "dislike" {
				candidate.score -= contribution * 1.15
			} else {
				candidate.score += contribution
				if contribution > bestPositive && contribution >= 2 {
					bestPositive = contribution
					candidate.reason = "Because you liked " + seed.feedback.Anime.Title
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return recommendationTitle(candidates[i].anime.Title) < recommendationTitle(candidates[j].anime.Title)
	})
	if len(candidates) > 48 {
		candidates = candidates[:48]
	}
	for _, candidate := range candidates {
		result.Items = append(result.Items, ExploreItem{Anime: candidate.anime, Reason: candidate.reason})
	}
	if result.Personalized {
		result.Message = "Recommendations shaped by your likes and dislikes. Rated and saved anime stay out of this feed."
	}
	if len(candidates) == 0 && available > 0 {
		result.Message = "You've explored these recommendations. Search for more anime, or refresh later."
	}
	if available == 0 {
		return result, errors.New("Explore is temporarily unavailable; HiAnime returned no usable recommendations")
	}
	return result, nil
}

func recommendationKeys(anime Anime) []string {
	keys := []string{}
	if anime.MALID > 0 {
		keys = append(keys, fmt.Sprintf("mal:%d", anime.MALID))
	}
	if anime.ID != "" {
		keys = append(keys, "id:"+anime.ID)
	}
	if anime.URL != "" {
		keys = append(keys, "url:"+anime.URL)
	}
	if title := recommendationTitle(anime.Title); title != "" {
		keys = append(keys, "title:"+title)
	}
	for _, alias := range anime.AlternateTitles {
		if title := recommendationTitle(alias); title != "" {
			keys = append(keys, "title:"+title)
		}
	}
	return keys
}

// Some source catalogues append the television format to the series title.
// Season, year, movie, and other distinguishing title text remains intact.
func animeIdentityTitle(title string) string {
	title = strings.TrimSpace(title)
	if len(title) >= 4 && strings.EqualFold(title[len(title)-4:], "(TV)") {
		title = strings.TrimSpace(title[:len(title)-4])
	}
	return title
}

func recommendationTitle(title string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(animeIdentityTitle(title)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }), " ")
}

func recommendationFeedback(feedback []AnimeFeedback) []AnimeFeedback {
	items := append([]AnimeFeedback{}, feedback...)
	sort.SliceStable(items, func(i, j int) bool {
		first, firstErr := time.Parse(time.RFC3339Nano, items[i].UpdatedAt)
		second, secondErr := time.Parse(time.RFC3339Nano, items[j].UpdatedAt)
		if firstErr == nil && secondErr == nil {
			return first.After(second)
		}
		// Legacy or malformed dates cannot establish recency. Keep valid dates
		// ahead of them, then use a deterministic string fallback for invalid
		// dates. Stable sorting preserves input order when dates are equal.
		if (firstErr == nil) != (secondErr == nil) {
			return firstErr == nil
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	out := []AnimeFeedback{}
	for _, item := range items {
		if item.Value != "like" && item.Value != "dislike" {
			continue
		}
		duplicate := false
		for _, prior := range out {
			duplicate = duplicate || sameStoredAnime(prior.Anime, item.Anime)
		}
		if duplicate {
			continue
		}
		out = append(out, item)
	}
	return out
}

func recommendationMergeGenres(first, second []string) []string {
	out := append([]string{}, first...)
	seen := map[string]bool{}
	for _, genre := range first {
		seen[strings.ToLower(genre)] = true
	}
	for _, genre := range second {
		if genre != "" && !seen[strings.ToLower(genre)] {
			out = append(out, genre)
			seen[strings.ToLower(genre)] = true
		}
	}
	return out
}

func recommendationGenreOverlap(first, second []string) int {
	seen := map[string]bool{}
	for _, genre := range first {
		seen[strings.ToLower(genre)] = true
	}
	count := 0
	for _, genre := range second {
		if seen[strings.ToLower(genre)] {
			count++
			delete(seen, strings.ToLower(genre))
		}
	}
	return count
}

func recommendationTextSimilarity(first, second Anime) float64 {
	tokens := func(anime Anime) map[string]bool {
		out := map[string]bool{}
		for _, token := range strings.Fields(recommendationTitle(anime.Title + " " + anime.Description)) {
			if len(token) > 2 && !strings.Contains("|the|and|for|with|from|that|this|their|they|who|his|her|has|was|are|into|but|when|its|", "|"+token+"|") {
				out[token] = true
			}
		}
		return out
	}
	a, b := tokens(first), tokens(second)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for token := range a {
		if b[token] {
			shared++
		}
	}
	return float64(shared) / math.Sqrt(float64(len(a)*len(b)))
}
