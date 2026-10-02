package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	malMetadataCacheTTL        = 24 * time.Hour
	malMetadataPartialCacheTTL = 5 * time.Minute
	malMetadataNegativeTTL     = 30 * time.Minute
	malMetadataBodyLimit       = episodeTitleBodyLimit
	malMetadataHTMLLimit       = 8 << 20
	malMetadataMaxCandidates   = 48
	malMetadataUserAgent       = "GoanimeDesktop/1.0 mal-metadata (https://github.com/alvarorichard/Goanime)"
	malHTMLBaseURL             = "https://myanimelist.net"
)

var (
	malYouTubeIDRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	malAnimeIDRE     = regexp.MustCompile(`^mal:([1-9][0-9]*)$`)
	malURLIDRE       = regexp.MustCompile(`^/anime/([1-9][0-9]*)(?:/|$)`)
	malTagCountRE    = regexp.MustCompile(`\s+\(([0-9][0-9,]*)\)$`)
	malCoverResizeRE = regexp.MustCompile(`^/r/[1-9][0-9]*x[1-9][0-9]*(/images/anime/.+)$`)
)

type MALMetadata struct {
	enricher *EpisodeTitleEnricher
	client   *http.Client
	baseURL  string
	htmlBase string
	now      func() time.Time
	cacheDir string

	mu       sync.Mutex
	cache    map[string]malMetadataCacheEntry
	flights  map[string]*malMetadataFlight
	failures map[string]time.Time
	loaded   bool
	modified bool
}

type malMetadataCacheEntry struct {
	Body    []byte    `json:"body"`
	Expires time.Time `json:"expires"`
}

type malMetadataDiskCache struct {
	Version int                              `json:"version"`
	Entries map[string]malMetadataCacheEntry `json:"entries"`
}

type malMetadataFlight struct {
	done chan struct{}
	body []byte
	err  error
}

type jikanAnime struct {
	MALID         int          `json:"mal_id"`
	URL           string       `json:"url"`
	Title         string       `json:"title"`
	TitleEnglish  string       `json:"title_english"`
	TitleJapanese string       `json:"title_japanese"`
	TitleSynonyms []string     `json:"title_synonyms"`
	Titles        []jikanTitle `json:"titles"`
	Images        struct {
		JPG struct {
			ImageURL      string `json:"image_url"`
			LargeImageURL string `json:"large_image_url"`
		} `json:"jpg"`
		WebP struct {
			ImageURL      string `json:"image_url"`
			LargeImageURL string `json:"large_image_url"`
		} `json:"webp"`
	} `json:"images"`
	Trailer struct {
		YouTubeID string `json:"youtube_id"`
		URL       string `json:"url"`
		EmbedURL  string `json:"embed_url"`
	} `json:"trailer"`
	Type         string       `json:"type"`
	Episodes     int          `json:"episodes"`
	Score        float64      `json:"score"`
	Synopsis     string       `json:"synopsis"`
	Background   string       `json:"background"`
	Genres       []jikanNamed `json:"genres"`
	Themes       []jikanNamed `json:"themes"`
	Demographics []jikanNamed `json:"demographics"`
}

type jikanTitle struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

type jikanNamed struct {
	MALID int    `json:"mal_id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	URL   string `json:"url"`
}

type jikanPagination struct {
	LastVisiblePage int  `json:"last_visible_page"`
	HasNextPage     bool `json:"has_next_page"`
}

// NewMALMetadata returns a public MyAnimeList/Jikan metadata helper. The cache
// path may be either a directory or a JSON file path.
func NewMALMetadata(enricher *EpisodeTitleEnricher, cachePath string) *MALMetadata {
	if enricher == nil {
		enricher = NewEpisodeTitleEnricher()
	}
	return &MALMetadata{
		enricher: enricher,
		client:   &http.Client{Timeout: episodeTitleHTTPTimeout},
		baseURL:  jikanBaseURL,
		htmlBase: malHTMLBaseURL,
		now:      time.Now,
		cacheDir: cachePath,
		cache:    map[string]malMetadataCacheEntry{},
		flights:  map[string]*malMetadataFlight{},
		failures: map[string]time.Time{},
	}
}

func (m *MALMetadata) init() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enricher == nil {
		m.enricher = NewEpisodeTitleEnricher()
	}
	if m.client == nil {
		m.client = &http.Client{Timeout: episodeTitleHTTPTimeout}
	}
	if m.baseURL == "" {
		m.baseURL = jikanBaseURL
	}
	if m.htmlBase == "" {
		m.htmlBase = malHTMLBaseURL
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.cache == nil {
		m.cache = map[string]malMetadataCacheEntry{}
	}
	if m.flights == nil {
		m.flights = map[string]*malMetadataFlight{}
	}
	if m.failures == nil {
		m.failures = map[string]time.Time{}
	}
	if !m.loaded {
		m.loadLocked()
		m.loaded = true
	}
}

// Details enriches an existing provider anime with public MAL tags, trailer,
// score, image, synopsis, and alternate titles without changing provider
// identity fields.
func (m *MALMetadata) Details(ctx context.Context, anime Anime) (Anime, error) {
	out := anime
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	m.init()
	malid := anime.MALID
	if malid <= 0 {
		malid = m.enricher.searchMALID(ctx, anime)
	}
	if malid <= 0 {
		malid = m.htmlSearchMALID(ctx, anime)
	}
	if malid <= 0 {
		return out, errors.New("no exact MyAnimeList match found")
	}
	var response struct {
		Data jikanAnime `json:"data"`
	}
	if err := m.getJSON(ctx, m.cacheKey("/anime/"+strconv.Itoa(malid)+"/full", nil), "/anime/"+strconv.Itoa(malid)+"/full", nil, &response, malMetadataCacheTTL); err != nil {
		if response.Data.MALID == malid {
			enriched := malApplyDetails(out, response.Data, false)
			if enriched.Trailer == nil && m.usesPublicMALHTML() {
				enriched.Trailer = m.htmlTrailer(ctx, malid)
			}
			return enriched, nil
		}
		if htmlAnime, htmlErr := m.htmlDetails(ctx, malid); htmlErr == nil {
			return malApplyDetails(out, htmlAnime, false), nil
		}
		return out, err
	}
	if response.Data.MALID != malid {
		return out, errors.New("MyAnimeList details returned a mismatched anime")
	}
	enriched := malApplyDetails(out, response.Data, false)
	if enriched.Trailer == nil && m.usesPublicMALHTML() {
		enriched.Trailer = m.htmlTrailer(ctx, malid)
	}
	return enriched, nil
}

// Tags returns public Jikan genre, theme, and demographic tags suitable for
// Explore filters.
func (m *MALMetadata) Tags(ctx context.Context) ([]AnimeTag, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.init()
	tags := []AnimeTag{}
	for _, filter := range []string{"genres", "themes", "demographics"} {
		var response struct {
			Data []jikanNamed `json:"data"`
		}
		values := url.Values{"filter": {filter}}
		if err := m.getJSON(ctx, m.cacheKey("/genres/anime", values), "/genres/anime", values, &response, malMetadataCacheTTL); err != nil {
			if len(tags) > 0 && !errors.Is(ctx.Err(), context.Canceled) {
				if htmlTags, htmlErr := m.htmlTags(ctx); htmlErr == nil && len(htmlTags) > 0 {
					return malMergeTags(tags, htmlTags), nil
				}
				return tags, nil
			}
			if htmlTags, htmlErr := m.htmlTags(ctx); htmlErr == nil && len(htmlTags) > 0 {
				return htmlTags, nil
			}
			return nil, err
		}
		kind := strings.TrimSuffix(filter, "s")
		for _, item := range response.Data {
			if item.MALID > 0 && strings.TrimSpace(item.Name) != "" {
				tags = append(tags, AnimeTag{ID: item.MALID, Name: malCleanTagName(item.Name), Kind: kind})
			}
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Kind != tags[j].Kind {
			return tags[i].Kind < tags[j].Kind
		}
		return strings.ToLower(tags[i].Name) < strings.ToLower(tags[j].Name)
	})
	return tags, nil
}

// Explore combines public MAL recommendations, tag-filtered discovery, and
// local likes/dislikes. Rated, saved, watched, and disliked anime are excluded.
func (m *MALMetadata) Explore(ctx context.Context, feedback []AnimeFeedback, known []Anime, tagIDs []int, refresh bool) (ExploreResult, error) {
	result := ExploreResult{Items: []ExploreItem{}, Warnings: []string{}, Message: "Popular on MyAnimeList. Like or dislike anime to shape this feed."}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return result, err
	}
	m.init()
	feedback = recommendationFeedback(feedback)
	result.Personalized = len(feedback) > 0 || len(tagIDs) > 0
	excluded := map[string]bool{}
	for _, anime := range known {
		for _, key := range malAnimeKeys(anime) {
			excluded[key] = true
		}
	}
	type seed struct {
		feedback AnimeFeedback
		anime    Anime
	}
	likes := []seed{}
	dislikes := []seed{}
	for _, item := range feedback {
		for _, key := range malAnimeKeys(item.Anime) {
			excluded[key] = true
		}
		seedAnime := item.Anime
		missingDetails := seedAnime.MALID <= 0 || (len(seedAnime.Tags) == 0 && len(seedAnime.Genres) == 0)
		shouldFetchDetails := missingDetails && ((item.Value == "like" && len(likes) < 3) || (item.Value == "dislike" && len(dislikes) < 2))
		if shouldFetchDetails {
			enriched, err := m.Details(ctx, item.Anime)
			if err != nil {
				if !errors.Is(ctx.Err(), context.Canceled) {
					result.Warnings = append(result.Warnings, item.Anime.Title+": "+err.Error())
				}
			} else {
				seedAnime = enriched
			}
		}
		if item.Value == "like" {
			likes = append(likes, seed{feedback: item, anime: seedAnime})
		}
		if item.Value == "dislike" {
			dislikes = append(dislikes, seed{feedback: item, anime: seedAnime})
		}
	}
	candidates := map[string]*malCandidate{}
	add := func(anime Anime, base float64, reason string) {
		if strings.TrimSpace(anime.Title) == "" {
			return
		}
		keys := malAnimeKeys(anime)
		for _, key := range keys {
			if excluded[key] {
				return
			}
		}
		key := ""
		if anime.MALID > 0 {
			key = "mal:" + strconv.Itoa(anime.MALID)
		} else if len(keys) > 0 {
			key = keys[0]
		}
		if key == "" {
			return
		}
		candidate, ok := candidates[key]
		if !ok {
			copy := anime
			copy.Tags = append([]AnimeTag{}, anime.Tags...)
			copy.Genres = append([]string{}, anime.Genres...)
			copy.AlternateTitles = append([]string{}, anime.AlternateTitles...)
			candidates[key] = &malCandidate{anime: copy, score: base, best: base, reason: reason}
			return
		}
		candidate.score += base * 0.35
		if base > candidate.best {
			candidate.best = base
			candidate.reason = reason
		}
		candidate.anime.Tags = malMergeTags(candidate.anime.Tags, anime.Tags)
		candidate.anime.Genres = recommendationMergeGenres(candidate.anime.Genres, anime.Genres)
		if candidate.anime.Trailer == nil {
			candidate.anime.Trailer = anime.Trailer
		}
	}
	available := 0
	if len(tagIDs) == 0 {
		for i, item := range likes {
			if i >= 3 {
				break
			}
			if item.anime.MALID <= 0 {
				continue
			}
			recs, err := m.recommendations(ctx, item.anime.MALID, refresh)
			if err != nil {
				if !errors.Is(ctx.Err(), context.Canceled) {
					result.Warnings = append(result.Warnings, item.anime.Title+": "+err.Error())
				}
				if len(recs) == 0 {
					continue
				}
			}
			available += len(recs)
			for rank, rec := range recs {
				reason := "MyAnimeList recommendations for " + item.anime.Title
				add(rec.anime, 45+float64(rec.votes)*2-float64(rank)*0.2, reason)
			}
		}
	}
	discovery, err := m.discovery(ctx, tagIDs, refresh)
	if err != nil {
		if !errors.Is(ctx.Err(), context.Canceled) {
			result.Warnings = append(result.Warnings, "MyAnimeList discovery: "+err.Error())
		}
	}
	if err == nil && len(tagIDs) > 0 && len(discovery) == 0 {
		result.Message = "No anime match these tags yet. Try fewer tags or a broader genre."
		return result, nil
	}
	if len(discovery) > 0 {
		available += len(discovery)
		for rank, anime := range discovery {
			reason := "Popular on MyAnimeList"
			if len(tagIDs) > 0 {
				reason = "Popular in your selected tags"
			}
			add(anime, 18-float64(rank)*0.08, reason)
		}
	}
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return result, err
	}
	out := make([]malCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		for _, like := range likes {
			candidate.score += float64(malTagOverlap(candidate.anime.Tags, like.anime.Tags)) * 5
			candidate.score += float64(recommendationGenreOverlap(candidate.anime.Genres, like.anime.Genres)) * 3
		}
		for _, dislike := range dislikes {
			candidate.score -= float64(malTagOverlap(candidate.anime.Tags, dislike.anime.Tags)) * 7
			candidate.score -= float64(recommendationGenreOverlap(candidate.anime.Genres, dislike.anime.Genres)) * 4
			if recommendationTextSimilarity(candidate.anime, dislike.anime) > 0.65 {
				candidate.score -= 20
			}
		}
		out = append(out, *candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].anime.MALID != out[j].anime.MALID {
			return out[i].anime.MALID < out[j].anime.MALID
		}
		return recommendationTitle(out[i].anime.Title) < recommendationTitle(out[j].anime.Title)
	})
	if len(out) > malMetadataMaxCandidates {
		out = out[:malMetadataMaxCandidates]
	}
	for _, candidate := range out {
		result.Items = append(result.Items, ExploreItem{Anime: candidate.anime, Reason: candidate.reason})
	}
	if result.Personalized {
		result.Message = "Recommendations shaped by your likes, dislikes, and selected MyAnimeList tags."
	}
	if available == 0 {
		return result, errors.New("Explore is temporarily unavailable; MyAnimeList metadata returned no usable recommendations")
	}
	return result, nil
}

type malCandidate struct {
	anime  Anime
	score  float64
	best   float64
	reason string
}

type malRecommendation struct {
	anime Anime
	votes int
}

func (m *MALMetadata) recommendations(ctx context.Context, malID int, refresh bool) ([]malRecommendation, error) {
	values := url.Values{}
	var response struct {
		Data []struct {
			Entry jikanAnime `json:"entry"`
			Votes int        `json:"votes"`
		} `json:"data"`
	}
	key := m.cacheKey("/anime/"+strconv.Itoa(malID)+"/recommendations", values)
	if refresh {
		m.stale(key)
	}
	err := m.getJSON(ctx, key, "/anime/"+strconv.Itoa(malID)+"/recommendations", values, &response, malMetadataCacheTTL)
	if err != nil && len(response.Data) == 0 {
		if htmlRecs, htmlErr := m.htmlRecommendations(ctx, malID); htmlErr == nil && len(htmlRecs) > 0 {
			return htmlRecs, nil
		}
	}
	out := []malRecommendation{}
	for _, item := range response.Data {
		anime := malAnimeFromJikan(item.Entry)
		if anime.MALID > 0 {
			out = append(out, malRecommendation{anime: anime, votes: item.Votes})
		}
		if len(out) >= malMetadataMaxCandidates {
			break
		}
	}
	return out, err
}

func (m *MALMetadata) discovery(ctx context.Context, tagIDs []int, refresh bool) ([]Anime, error) {
	path := "/top/anime"
	values := url.Values{"filter": {"bypopularity"}}
	if len(tagIDs) > 0 {
		path = "/anime"
		values = url.Values{
			"genres":   {malJoinInts(tagIDs)},
			"order_by": {"members"},
			"sort":     {"desc"},
			"sfw":      {"true"},
			"limit":    {"25"},
		}
	}
	key := m.cacheKey(path, values)
	if refresh {
		m.stale(key)
	}
	var response struct {
		Data []jikanAnime `json:"data"`
	}
	err := m.getJSON(ctx, key, path, values, &response, malMetadataCacheTTL)
	if err != nil && len(response.Data) == 0 {
		if len(tagIDs) > 0 {
			if htmlItems, htmlErr := m.htmlGenre(ctx, tagIDs); htmlErr == nil {
				return htmlItems, nil
			}
		} else if htmlItems, htmlErr := m.htmlTopAnime(ctx); htmlErr == nil && len(htmlItems) > 0 {
			return htmlItems, nil
		}
	}
	out := []Anime{}
	for _, item := range response.Data {
		anime := malAnimeFromJikan(item)
		if anime.MALID > 0 {
			out = append(out, anime)
		}
	}
	return out, err
}

func (m *MALMetadata) getJSON(ctx context.Context, key, path string, values url.Values, target any, ttl time.Duration) error {
	body, err := m.getCached(ctx, key, path, values, ttl)
	if len(body) > 0 {
		if decodeErr := json.Unmarshal(body, target); decodeErr != nil {
			return decodeErr
		}
	}
	return err
}

func (m *MALMetadata) getCached(ctx context.Context, key, path string, values url.Values, ttl time.Duration) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := m.now()
	m.mu.Lock()
	if entry, ok := m.cache[key]; ok && now.Before(entry.Expires) {
		body := append([]byte(nil), entry.Body...)
		m.mu.Unlock()
		return body, nil
	}
	if until, failed := m.failures[key]; failed && now.Before(until) {
		if entry, ok := m.cache[key]; ok && !errors.Is(ctx.Err(), context.Canceled) {
			body := append([]byte(nil), entry.Body...)
			m.mu.Unlock()
			return body, fmt.Errorf("using cached MyAnimeList metadata: recent Jikan failure")
		}
		m.mu.Unlock()
		return nil, errors.New("recent Jikan failure")
	}
	if call := m.flights[key]; call != nil {
		done := call.done
		m.mu.Unlock()
		select {
		case <-done:
			return append([]byte(nil), call.body...), call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &malMetadataFlight{done: make(chan struct{})}
	m.flights[key] = call
	m.mu.Unlock()

	body, err := m.fetch(ctx, path, values)
	if err == nil {
		call.body = append([]byte(nil), body...)
		m.mu.Lock()
		m.cache[key] = malMetadataCacheEntry{Body: append([]byte(nil), body...), Expires: m.now().Add(ttl)}
		m.modified = true
		m.pruneLocked()
		delete(m.flights, key)
		close(call.done)
		m.saveLocked()
		m.mu.Unlock()
		return body, nil
	}
	m.mu.Lock()
	delete(m.flights, key)
	if malTransientMetadataError(err) && !errors.Is(ctx.Err(), context.Canceled) {
		m.failures[key] = m.now().Add(malMetadataPartialCacheTTL)
	}
	if entry, ok := m.cache[key]; ok && !errors.Is(ctx.Err(), context.Canceled) {
		call.body = append([]byte(nil), entry.Body...)
		call.err = fmt.Errorf("using cached MyAnimeList metadata: %w", err)
		close(call.done)
		body := append([]byte(nil), entry.Body...)
		m.mu.Unlock()
		return body, call.err
	}
	call.err = err
	close(call.done)
	m.mu.Unlock()
	return nil, err
}

func (m *MALMetadata) fetch(ctx context.Context, path string, values url.Values) ([]byte, error) {
	if err := jikanGlobalLimiter.wait(ctx, m.now); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimRight(m.baseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", malMetadataUserAgent)
	client := *m.client
	client.CheckRedirect = malSameHostRedirect(req.URL)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := episodeTitleReadBody(resp, malMetadataBodyLimit)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Jikan HTTP %d", resp.StatusCode)
	}
	if !json.Valid(body) {
		return nil, errors.New("Jikan returned invalid JSON")
	}
	return body, nil
}

func malSameHostRedirect(origin *url.URL) func(*http.Request, []*http.Request) error {
	return func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 5 {
			return errors.New("metadata request returned too many redirects")
		}
		if origin == nil || next == nil || next.URL == nil {
			return errors.New("metadata request returned an invalid redirect")
		}
		if next.URL.Scheme != origin.Scheme || !strings.EqualFold(next.URL.Hostname(), origin.Hostname()) {
			return errors.New("metadata request redirected outside the expected host")
		}
		return nil
	}
}

func malTransientMetadataError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "timeout") ||
		strings.Contains(message, "deadline") ||
		strings.Contains(message, "jikan http 502") ||
		strings.Contains(message, "jikan http 503") ||
		strings.Contains(message, "jikan http 504") ||
		strings.Contains(message, "connection reset")
}

func (m *MALMetadata) htmlDocument(ctx context.Context, rawURL string) (*goquery.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := m.getHTMLCached(ctx, rawURL, malMetadataCacheTTL)
	if err != nil && len(body) == 0 {
		return nil, err
	}
	return goquery.NewDocumentFromReader(bytes.NewReader(body))
}

func (m *MALMetadata) usesPublicMALHTML() bool {
	u, err := url.Parse(strings.TrimSpace(m.htmlBase))
	return err == nil && strings.EqualFold(u.Hostname(), "myanimelist.net")
}

func (m *MALMetadata) getHTMLCached(ctx context.Context, rawURL string, ttl time.Duration) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := "html:" + rawURL
	now := m.now()
	m.mu.Lock()
	if entry, ok := m.cache[key]; ok && now.Before(entry.Expires) {
		body := append([]byte(nil), entry.Body...)
		m.mu.Unlock()
		return body, nil
	}
	if call := m.flights[key]; call != nil {
		done := call.done
		m.mu.Unlock()
		select {
		case <-done:
			return append([]byte(nil), call.body...), call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &malMetadataFlight{done: make(chan struct{})}
	m.flights[key] = call
	m.mu.Unlock()

	body, err := m.fetchHTML(ctx, rawURL)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.flights, key)
	if err == nil {
		call.body = append([]byte(nil), body...)
		m.cache[key] = malMetadataCacheEntry{Body: append([]byte(nil), body...), Expires: m.now().Add(ttl)}
		m.modified = true
		m.pruneLocked()
		close(call.done)
		m.saveLocked()
		return body, nil
	}
	if entry, ok := m.cache[key]; ok && !errors.Is(ctx.Err(), context.Canceled) {
		call.body = append([]byte(nil), entry.Body...)
		call.err = fmt.Errorf("using cached MyAnimeList page: %w", err)
		close(call.done)
		return append([]byte(nil), entry.Body...), call.err
	}
	call.err = err
	close(call.done)
	return nil, err
}

func (m *MALMetadata) fetchHTML(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	req.Header.Set("User-Agent", malMetadataUserAgent)
	client := *m.client
	client.CheckRedirect = malSameHostRedirect(req.URL)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := episodeTitleReadBody(resp, malMetadataHTMLLimit)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MyAnimeList HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (m *MALMetadata) htmlDetails(ctx context.Context, malID int) (jikanAnime, error) {
	doc, err := m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/anime/"+strconv.Itoa(malID))
	if err != nil {
		return jikanAnime{}, err
	}
	item := jikanAnime{MALID: malID, URL: "https://myanimelist.net/anime/" + strconv.Itoa(malID)}
	item.Title = catalogPlainText(firstNonBlank(
		doc.Find("meta[property='og:title']").AttrOr("content", ""),
		doc.Find("h1.title-name strong").First().Text(),
		doc.Find("h1.title-name").First().Text(),
		doc.Find(".h1-title .title-name").First().Text(),
	))
	item.Synopsis = catalogPlainText(firstNonBlank(
		doc.Find("meta[property='og:description']").AttrOr("content", ""),
		doc.Find("p[itemprop='description']").First().Text(),
		doc.Find(".js-scrollfix-bottom-rel .pb16").First().Text(),
	))
	item.Images.JPG.ImageURL = htmlAbsolute(doc.Find("meta[property='og:image']").AttrOr("content", ""))
	if score, err := strconv.ParseFloat(strings.TrimSpace(doc.Find("[itemprop='ratingValue']").First().Text()), 64); err == nil {
		item.Score = score
	}
	item.Genres, item.Themes, item.Demographics = malHTMLTags(doc)
	item.TitleSynonyms = malHTMLAlternativeTitles(doc)
	if trailer := m.htmlTrailer(ctx, malID); trailer != nil {
		item.Trailer.YouTubeID = trailer.YouTubeID
		item.Trailer.URL = trailer.URL
	}
	for _, text := range []string{"Episodes:"} {
		doc.Find(".spaceit_pad").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			raw := catalogPlainText(sel.Text())
			if strings.HasPrefix(raw, text) {
				count, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(raw, text)))
				item.Episodes = count
				return false
			}
			return true
		})
	}
	if item.Title == "" {
		return jikanAnime{}, errors.New("MyAnimeList details page did not expose a title")
	}
	return item, nil
}

func (m *MALMetadata) htmlSearchMALID(ctx context.Context, anime Anime) int {
	title := strings.TrimSpace(anime.Title)
	if title == "" || ctx.Err() != nil {
		return 0
	}
	u, err := url.Parse(strings.TrimRight(m.htmlBase, "/") + "/anime.php")
	if err != nil {
		return 0
	}
	u.RawQuery = url.Values{"q": {title}, "cat": {"anime"}}.Encode()
	doc, err := m.htmlDocument(ctx, u.String())
	if err != nil {
		return 0
	}
	target := episodeTitleNormalize(title)
	candidates := map[int]bool{}
	doc.Find("a[href^='https://myanimelist.net/anime/'], a[href^='/anime/']").Each(func(_ int, link *goquery.Selection) {
		raw, _ := link.Attr("href")
		id := malURLID(htmlAbsolute(raw))
		if id <= 0 {
			return
		}
		linkTitle := catalogPlainText(firstNonBlank(link.AttrOr("title", ""), link.Find("strong").First().Text(), link.Text()))
		if episodeTitleNormalize(linkTitle) != target {
			return
		}
		if anime.EpisodeCount > 0 {
			contextText := strings.ToLower(catalogPlainText(link.ParentsFiltered("tr, .js-categories-seasonal, .seasonal-anime, .picSurround").First().Text()))
			if eps := malHTMLEpisodeCount(contextText); eps > 0 && eps != anime.EpisodeCount {
				return
			}
		}
		candidates[id] = true
	})
	if len(candidates) != 1 {
		return 0
	}
	for id := range candidates {
		return id
	}
	return 0
}

func malHTMLEpisodeCount(text string) int {
	for _, marker := range []string{"episodes:", "eps:", "eps"} {
		index := strings.Index(text, marker)
		if index < 0 {
			continue
		}
		rest := strings.TrimSpace(text[index+len(marker):])
		fields := strings.FieldsFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
		if len(fields) > 0 {
			count, _ := strconv.Atoi(fields[0])
			return count
		}
	}
	return 0
}

func (m *MALMetadata) htmlTrailer(ctx context.Context, malID int) *AnimeTrailer {
	doc, err := m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/anime/"+strconv.Itoa(malID)+"/_/video")
	if err != nil {
		doc, err = m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/anime/"+strconv.Itoa(malID)+"/video")
		if err != nil {
			return nil
		}
	}
	var trailer *AnimeTrailer
	doc.Find("a[href*='youtube.com'], a[href*='youtube-nocookie.com'], a[href*='youtu.be'], iframe[src*='youtube.com'], iframe[src*='youtube-nocookie.com'], iframe[src*='youtu.be'], div[data-video-id], a[data-video-id]").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		contextText := strings.ToLower(catalogPlainText(sel.ParentsFiltered("div, li, td, tr, section").First().Text() + " " + sel.Text() + " " + sel.AttrOr("title", "") + " " + sel.AttrOr("class", "") + " " + sel.Parent().AttrOr("class", "")))
		if strings.Contains(contextText, "oped") || strings.Contains(contextText, "theme song") || strings.Contains(contextText, "opening") || strings.Contains(contextText, "ending") || strings.Contains(contextText, "music") {
			return true
		}
		if !strings.Contains(contextText, "promo") && !strings.Contains(contextText, "pv") && !strings.Contains(contextText, "trailer") {
			return true
		}
		for _, attr := range []string{"data-video-id", "href", "src", "data-src"} {
			raw, ok := sel.Attr(attr)
			if !ok {
				continue
			}
			if item := malTrailer(raw, raw, raw); item != nil {
				trailer = item
				return false
			}
		}
		return true
	})
	return trailer
}

func (m *MALMetadata) htmlRecommendations(ctx context.Context, malID int) ([]malRecommendation, error) {
	doc, err := m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/anime/"+strconv.Itoa(malID)+"/_/userrecs")
	if err != nil {
		return nil, err
	}
	out := []malRecommendation{}
	seen := map[int]bool{}
	doc.Find("#anime_recommendation li.btn-anime, .anime-slide-block li.btn-anime, a[href*='/recommendations/anime/']").Each(func(_ int, sel *goquery.Selection) {
		target := sel
		if goquery.NodeName(sel) != "a" {
			target = sel.Find("a[href*='/recommendations/anime/']").First()
		}
		raw, _ := target.Attr("href")
		id := malRecommendationTargetID(raw, malID)
		if id <= 0 || seen[id] {
			return
		}
		title := catalogPlainText(firstNonBlank(target.AttrOr("title", ""), sel.AttrOr("title", ""), target.Text(), sel.Text()))
		if title == "" {
			return
		}
		anime := Anime{ID: "mal:" + strconv.Itoa(id), Title: title, URL: "https://myanimelist.net/anime/" + strconv.Itoa(id), Source: "mal", MALID: id, MetadataSource: "myanimelist"}
		if image := malBackgroundImage(firstNonBlank(target.AttrOr("style", ""), sel.AttrOr("style", ""))); image != "" {
			anime.ImageURL = image
		}
		out = append(out, malRecommendation{anime: anime, votes: malRecommendationVotes(sel.Text())})
		seen[id] = true
	})
	if len(out) == 0 {
		return nil, errors.New("MyAnimeList recommendation page returned no usable anime")
	}
	return out, nil
}

func (m *MALMetadata) htmlTopAnime(ctx context.Context) ([]Anime, error) {
	doc, err := m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/topanime.php")
	if err != nil {
		return nil, err
	}
	items := malHTMLAnimeCards(doc)
	if len(items) == 0 {
		return nil, errors.New("MyAnimeList top anime page returned no usable anime")
	}
	return items, nil
}

func (m *MALMetadata) htmlGenre(ctx context.Context, tagIDs []int) ([]Anime, error) {
	if len(tagIDs) == 0 {
		return nil, errors.New("missing MyAnimeList genre id")
	}
	var items []Anime
	seenEverywhere := map[int]Anime{}
	for i, tagID := range tagIDs {
		if tagID <= 0 {
			continue
		}
		doc, err := m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/anime/genre/"+strconv.Itoa(tagID))
		if err != nil {
			return nil, err
		}
		pageItems := malHTMLAnimeCards(doc)
		if i == 0 {
			for _, anime := range pageItems {
				if anime.MALID > 0 {
					seenEverywhere[anime.MALID] = anime
				}
			}
			continue
		}
		current := map[int]bool{}
		for _, anime := range pageItems {
			if anime.MALID > 0 {
				current[anime.MALID] = true
			}
		}
		for id := range seenEverywhere {
			if !current[id] {
				delete(seenEverywhere, id)
			}
		}
	}
	for _, anime := range seenEverywhere {
		items = append(items, anime)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return recommendationTitle(items[i].Title) < recommendationTitle(items[j].Title)
	})
	if len(items) == 0 {
		return []Anime{}, nil
	}
	return items, nil
}

func (m *MALMetadata) htmlTags(ctx context.Context) ([]AnimeTag, error) {
	doc, err := m.htmlDocument(ctx, strings.TrimRight(m.htmlBase, "/")+"/anime.php")
	if err != nil {
		return nil, err
	}
	tags := []AnimeTag{}
	seen := map[int]bool{}
	doc.Find("a[href^='/anime/genre/'], a[href*='myanimelist.net/anime/genre/']").Each(func(_ int, link *goquery.Selection) {
		raw, _ := link.Attr("href")
		id := malGenreURLID(raw)
		name := malCleanTagName(link.Text())
		if id <= 0 || name == "" {
			return
		}
		if !seen[id] {
			tags = append(tags, AnimeTag{ID: id, Name: name, Kind: malHTMLTagKind(link)})
			seen[id] = true
		}
	})
	if len(tags) == 0 {
		return nil, errors.New("MyAnimeList genre index returned no tags")
	}
	return tags, nil
}

func malHTMLTagKind(link *goquery.Selection) string {
	for node := link; node != nil && node.Length() > 0; node = node.Parent() {
		text := catalogPlainText(node.Find("h2, h3, h4, .genre-name, .category-name, .normal_header").First().Text())
		if kind := malTagKindFromText(text); kind != "" {
			return kind
		}
		if prev := catalogPlainText(node.PrevAllFiltered("h2, h3, h4, .genre-name, .category-name, .normal_header").First().Text()); prev != "" {
			if kind := malTagKindFromText(prev); kind != "" {
				return kind
			}
		}
	}
	return "genre"
}

func malTagKindFromText(text string) string {
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "theme"):
		return "theme"
	case strings.Contains(text, "demographic"):
		return "demographic"
	case strings.Contains(text, "genre"):
		return "genre"
	default:
		return ""
	}
}

func (m *MALMetadata) cacheKey(path string, values url.Values) string {
	query := ""
	if values != nil {
		query = values.Encode()
	}
	return path + "?" + query
}

func (m *MALMetadata) stale(key string) {
	m.mu.Lock()
	if entry, ok := m.cache[key]; ok {
		entry.Expires = m.now().Add(-time.Second)
		m.cache[key] = entry
		m.modified = true
	}
	m.mu.Unlock()
}

func (m *MALMetadata) cacheFile() string {
	path := strings.TrimSpace(m.cacheDir)
	if path == "" {
		return ""
	}
	if strings.HasSuffix(path, ".json") {
		return path
	}
	return filepath.Join(path, "mal_metadata_cache.json")
}

func (m *MALMetadata) loadLocked() {
	file := m.cacheFile()
	if file == "" {
		return
	}
	body, err := os.ReadFile(file)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return
	}
	var disk malMetadataDiskCache
	if json.Unmarshal(body, &disk) != nil {
		return
	}
	now := m.now()
	for key, entry := range disk.Entries {
		if len(entry.Body) > 0 {
			if entry.Expires.IsZero() {
				entry.Expires = now.Add(-time.Second)
			}
			m.cache[key] = entry
		}
	}
}

func (m *MALMetadata) saveLocked() {
	file := m.cacheFile()
	if file == "" || !m.modified {
		return
	}
	disk := malMetadataDiskCache{Version: 1, Entries: map[string]malMetadataCacheEntry{}}
	for key, entry := range m.cache {
		if len(entry.Body) > 0 {
			disk.Entries[key] = entry
		}
	}
	body, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return
	}
	if os.Rename(tmp, file) == nil {
		m.modified = false
	}
}

func (m *MALMetadata) pruneLocked() {
	if len(m.cache) <= 256 {
		return
	}
	type item struct {
		key     string
		expires time.Time
	}
	items := make([]item, 0, len(m.cache))
	for key, entry := range m.cache {
		items = append(items, item{key: key, expires: entry.Expires})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].expires.Equal(items[j].expires) {
			return items[i].key < items[j].key
		}
		return items[i].expires.Before(items[j].expires)
	})
	for _, item := range items[:len(items)-256] {
		delete(m.cache, item.key)
	}
}

func malAnimeFromJikan(item jikanAnime) Anime {
	anime := Anime{
		ID:              "mal:" + strconv.Itoa(item.MALID),
		Title:           firstNonBlank(item.TitleEnglish, item.Title),
		URL:             "https://myanimelist.net/anime/" + strconv.Itoa(item.MALID),
		ImageURL:        malCoverURL(firstNonBlank(item.Images.WebP.LargeImageURL, item.Images.JPG.LargeImageURL, item.Images.WebP.ImageURL, item.Images.JPG.ImageURL)),
		Source:          "mal",
		Language:        "",
		Description:     catalogPlainText(firstNonBlank(item.Synopsis, item.Background)),
		Genres:          []string{},
		Score:           item.Score,
		EpisodeCount:    item.Episodes,
		MALID:           item.MALID,
		Tags:            malTags(item),
		Trailer:         malTrailer(item.Trailer.YouTubeID, item.Trailer.URL, item.Trailer.EmbedURL),
		MetadataSource:  "myanimelist",
		AlternateTitles: malAlternateTitles(item),
	}
	for _, tag := range anime.Tags {
		if tag.Kind == "genre" {
			anime.Genres = append(anime.Genres, tag.Name)
		}
	}
	if anime.Title == "" {
		anime.Title = firstNonBlank(item.Title, item.TitleJapanese)
	}
	return anime
}

func malHTMLAnimeCards(doc *goquery.Document) []Anime {
	out := []Anime{}
	seen := map[int]bool{}
	doc.Find("a[href^='https://myanimelist.net/anime/'], a[href^='/anime/']").Each(func(_ int, link *goquery.Selection) {
		raw, _ := link.Attr("href")
		id := malURLID(htmlAbsolute(raw))
		if id <= 0 || seen[id] {
			return
		}
		title := catalogPlainText(firstNonBlank(link.AttrOr("title", ""), link.Find("strong").First().Text(), link.Text()))
		if title == "" || strings.EqualFold(title, "add detailed info") || strings.Contains(strings.ToLower(title), "more") {
			return
		}
		anime := Anime{ID: "mal:" + strconv.Itoa(id), Title: title, URL: "https://myanimelist.net/anime/" + strconv.Itoa(id), Source: "mal", MALID: id, MetadataSource: "myanimelist"}
		container := link.ParentsFiltered("tr, .seasonal-anime, .js-anime-category-producer, .ranking-list").First()
		if container.Length() == 0 {
			container = link.Parent()
		}
		anime.ImageURL = malHTMLImageURL(link)
		if anime.ImageURL == "" {
			anime.ImageURL = malHTMLImageURL(container)
		}
		if score, err := strconv.ParseFloat(strings.TrimSpace(container.Find(".score, .js-top-ranking-score-col").First().Text()), 64); err == nil {
			anime.Score = score
		}
		out = append(out, anime)
		seen[id] = true
		if len(out) >= malMetadataMaxCandidates {
			return
		}
	})
	if len(out) > malMetadataMaxCandidates {
		return out[:malMetadataMaxCandidates]
	}
	return out
}

func malHTMLTags(doc *goquery.Document) (genres, themes, demographics []jikanNamed) {
	add := func(kind string, raw string, name string) {
		id := malGenreURLID(raw)
		name = malCleanTagName(name)
		if id <= 0 || name == "" {
			return
		}
		item := jikanNamed{MALID: id, Name: name}
		switch kind {
		case "genre":
			genres = append(genres, item)
		case "theme":
			themes = append(themes, item)
		case "demographic":
			demographics = append(demographics, item)
		}
	}
	doc.Find("span.dark_text").Each(func(_ int, label *goquery.Selection) {
		kind := ""
		switch strings.TrimSpace(strings.TrimSuffix(label.Text(), ":")) {
		case "Genres", "Genre":
			kind = "genre"
		case "Themes", "Theme":
			kind = "theme"
		case "Demographic", "Demographics":
			kind = "demographic"
		default:
			return
		}
		parent := label.Parent()
		parent.Find("a[href*='/anime/genre/']").Each(func(_ int, link *goquery.Selection) {
			raw, _ := link.Attr("href")
			add(kind, raw, link.Text())
		})
	})
	return genres, themes, demographics
}

func malHTMLImageURL(sel *goquery.Selection) string {
	if sel == nil || sel.Length() == 0 {
		return ""
	}
	img := sel.Find("img").First()
	for _, attr := range []string{"data-src", "data-original", "src"} {
		if image, ok := img.Attr(attr); ok {
			if absolute := htmlAbsolute(image); absolute != "" && !strings.Contains(absolute, "/spacer.gif") {
				return malCoverURL(absolute)
			}
		}
	}
	if set, ok := img.Attr("data-srcset"); ok {
		for _, candidate := range strings.Split(set, ",") {
			fields := strings.Fields(strings.TrimSpace(candidate))
			if len(fields) > 0 {
				if absolute := htmlAbsolute(fields[0]); absolute != "" && !strings.Contains(absolute, "/spacer.gif") {
					return malCoverURL(absolute)
				}
			}
		}
	}
	return ""
}

func malCoverURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "cdn.myanimelist.net") {
		return raw
	}
	match := malCoverResizeRE.FindStringSubmatch(u.Path)
	if len(match) != 2 {
		return raw
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = match[1], "", "", ""
	return u.String()
}

func malCleanTagName(name string) string {
	name = catalogPlainText(name)
	return strings.TrimSpace(malTagCountRE.ReplaceAllString(name, ""))
}

func malHTMLAlternativeTitles(doc *goquery.Document) []string {
	out := []string{}
	doc.Find("h2").Each(func(_ int, heading *goquery.Selection) {
		if !strings.EqualFold(catalogPlainText(heading.Text()), "Alternative Titles") {
			return
		}
		for node := heading.Next(); node.Length() > 0; node = node.Next() {
			if goquery.NodeName(node) == "h2" {
				break
			}
			node.Find(".spaceit_pad").AddBackFiltered(".spaceit_pad").Each(func(_ int, row *goquery.Selection) {
				label := row.Find(".dark_text").First()
				if label.Length() == 0 {
					return
				}
				raw := catalogPlainText(row.Text())
				labelText := catalogPlainText(label.Text())
				raw = strings.TrimSpace(strings.TrimPrefix(raw, labelText))
				for _, part := range strings.Split(raw, ",") {
					out = malAppendString(out, part)
				}
			})
		}
	})
	return out
}

func malRecommendationTargetID(raw string, sourceID int) int {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "recommendations" || parts[1] != "anime" {
		return 0
	}
	ids := strings.Split(parts[2], "-")
	if len(ids) != 2 {
		return 0
	}
	first, _ := strconv.Atoi(ids[0])
	second, _ := strconv.Atoi(ids[1])
	if first == sourceID {
		return second
	}
	if second == sourceID {
		return first
	}
	return 0
}

func malRecommendationVotes(text string) int {
	fields := strings.FieldsFunc(text, func(r rune) bool { return r < '0' || r > '9' })
	for i := len(fields) - 1; i >= 0; i-- {
		if value, err := strconv.Atoi(fields[i]); err == nil {
			return value
		}
	}
	return 0
}

func malBackgroundImage(style string) string {
	lower := strings.ToLower(style)
	start := strings.Index(lower, "url(")
	if start < 0 {
		return ""
	}
	rest := style[start+4:]
	end := strings.Index(rest, ")")
	if end < 0 {
		return ""
	}
	return malCoverURL(htmlAbsolute(strings.Trim(rest[:end], `"' `)))
}

func malGenreURLID(raw string) int {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "anime" && parts[i+1] == "genre" {
			id, _ := strconv.Atoi(parts[i+2])
			return id
		}
	}
	return 0
}

func htmlAbsolute(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(raw, "/") {
		return malHTMLBaseURL + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return ""
	}
	return u.String()
}

func malApplyDetails(anime Anime, item jikanAnime, replaceIdentity bool) Anime {
	meta := malAnimeFromJikan(item)
	if replaceIdentity {
		return meta
	}
	anime.MALID = meta.MALID
	anime.Tags = malMergeTags(anime.Tags, meta.Tags)
	anime.AlternateTitles = malMergeStrings(anime.AlternateTitles, meta.AlternateTitles)
	anime.MetadataSource = meta.MetadataSource
	anime.Genres = recommendationMergeGenres(anime.Genres, meta.Genres)
	if anime.Trailer == nil {
		anime.Trailer = meta.Trailer
	}
	if strings.TrimSpace(anime.ImageURL) == "" {
		anime.ImageURL = meta.ImageURL
	}
	if strings.TrimSpace(anime.Description) == "" {
		anime.Description = meta.Description
	}
	if anime.Score == 0 {
		anime.Score = meta.Score
	}
	if anime.EpisodeCount == 0 {
		anime.EpisodeCount = meta.EpisodeCount
	}
	return anime
}

func malTags(item jikanAnime) []AnimeTag {
	tags := []AnimeTag{}
	add := func(kind string, items []jikanNamed) {
		for _, item := range items {
			if item.MALID > 0 && strings.TrimSpace(item.Name) != "" {
				tags = append(tags, AnimeTag{ID: item.MALID, Name: catalogPlainText(item.Name), Kind: kind})
			}
		}
	}
	add("genre", item.Genres)
	add("theme", item.Themes)
	add("demographic", item.Demographics)
	return malMergeTags(nil, tags)
}

func malTrailer(id, rawURL, embedURL string) *AnimeTrailer {
	id = strings.TrimSpace(id)
	if !malYouTubeIDRE.MatchString(id) {
		if parsed := malYouTubeID(rawURL); parsed != "" {
			id = parsed
		} else if parsed := malYouTubeID(embedURL); parsed != "" {
			id = parsed
		}
	}
	if !malYouTubeIDRE.MatchString(id) {
		return nil
	}
	return &AnimeTrailer{YouTubeID: id, URL: "https://www.youtube.com/watch?v=" + id}
}

func malYouTubeID(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "www.youtube.com", "youtube.com", "m.youtube.com", "www.youtube-nocookie.com", "youtube-nocookie.com":
		if id := u.Query().Get("v"); malYouTubeIDRE.MatchString(id) {
			return id
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && (parts[0] == "embed" || parts[0] == "shorts") && malYouTubeIDRE.MatchString(parts[1]) {
			return parts[1]
		}
	case "youtu.be":
		id := strings.Trim(u.Path, "/")
		if malYouTubeIDRE.MatchString(id) {
			return id
		}
	}
	return ""
}

func malAlternateTitles(item jikanAnime) []string {
	out := []string{}
	for _, value := range []string{item.Title, item.TitleEnglish, item.TitleJapanese} {
		out = malAppendString(out, catalogPlainText(value))
	}
	for _, value := range item.TitleSynonyms {
		out = malAppendString(out, catalogPlainText(value))
	}
	for _, title := range item.Titles {
		out = malAppendString(out, catalogPlainText(title.Title))
	}
	return out
}

func malAppendString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return items
		}
	}
	return append(items, value)
}

func malMergeStrings(first, second []string) []string {
	out := append([]string{}, first...)
	for _, value := range second {
		out = malAppendString(out, value)
	}
	return out
}

func malMergeTags(first, second []AnimeTag) []AnimeTag {
	out := append([]AnimeTag{}, first...)
	seen := map[int]bool{}
	for _, tag := range out {
		if tag.ID > 0 {
			seen[tag.ID] = true
		}
	}
	for _, tag := range second {
		if tag.ID <= 0 || strings.TrimSpace(tag.Name) == "" || strings.TrimSpace(tag.Kind) == "" {
			continue
		}
		if !seen[tag.ID] {
			tag.Name = catalogPlainText(tag.Name)
			tag.Kind = strings.ToLower(strings.TrimSpace(tag.Kind))
			out = append(out, tag)
			seen[tag.ID] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func malTagKey(tag AnimeTag) string {
	return strings.ToLower(strings.TrimSpace(tag.Kind)) + ":" + strconv.Itoa(tag.ID)
}

func malTagOverlap(first, second []AnimeTag) int {
	seen := map[string]bool{}
	for _, tag := range first {
		seen[malTagKey(tag)] = true
	}
	count := 0
	for _, tag := range second {
		key := malTagKey(tag)
		if seen[key] {
			count++
			delete(seen, key)
		}
	}
	return count
}

func malAnimeKeys(anime Anime) []string {
	keys := recommendationKeys(anime)
	if anime.MALID > 0 {
		keys = append(keys, "mal:"+strconv.Itoa(anime.MALID))
	}
	if match := malAnimeIDRE.FindStringSubmatch(strings.TrimSpace(anime.ID)); len(match) == 2 {
		keys = append(keys, "mal:"+match[1])
	}
	if id := malURLID(anime.URL); id > 0 {
		keys = append(keys, "mal:"+strconv.Itoa(id))
	}
	return malUniqueStrings(keys)
}

func malUniqueStrings(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

func malJoinInts(values []int) string {
	out := []string{}
	seen := map[int]bool{}
	for _, value := range values {
		if value > 0 && !seen[value] {
			out = append(out, strconv.Itoa(value))
			seen[value] = true
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func malURLID(raw string) int {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || strings.ToLower(u.Hostname()) != "myanimelist.net" {
		return 0
	}
	match := malURLIDRE.FindStringSubmatch(u.EscapedPath())
	if len(match) != 2 {
		return 0
	}
	id, _ := strconv.Atoi(match[1])
	return id
}

// ValidateMALAnime verifies MAL-origin anime identities before they are stored
// as local feedback or saved entries. Other provider anime remain valid for
// the existing HiAnime-backed recommendation path.
func ValidateMALAnime(anime Anime) error {
	if strings.ToLower(strings.TrimSpace(anime.Source)) != "mal" && anime.MALID == 0 && !strings.HasPrefix(strings.TrimSpace(anime.ID), "mal:") {
		return nil
	}
	if anime.MALID <= 0 {
		return errors.New("missing MyAnimeList id")
	}
	match := malAnimeIDRE.FindStringSubmatch(strings.TrimSpace(anime.ID))
	if len(match) != 2 {
		return errors.New("invalid MyAnimeList anime id")
	}
	id, _ := strconv.Atoi(match[1])
	if id != anime.MALID {
		return errors.New("MyAnimeList id mismatch")
	}
	u, err := url.Parse(strings.TrimSpace(anime.URL))
	if err != nil || u.Scheme != "https" || strings.ToLower(u.Hostname()) != "myanimelist.net" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid MyAnimeList URL")
	}
	if privateHost(u.Hostname()) {
		return errors.New("invalid MyAnimeList host")
	}
	if malURLID(anime.URL) != anime.MALID {
		return errors.New("MyAnimeList URL id mismatch")
	}
	return nil
}

func privateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
