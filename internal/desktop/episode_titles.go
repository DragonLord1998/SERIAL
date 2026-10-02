package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
)

const (
	jikanBaseURL             = "https://api.jikan.moe/v4"
	episodeTitleBodyLimit    = 4 << 20
	episodeTitleMaxPages     = 12
	episodeTitleSearchLimit  = 10
	episodeTitleHTTPTimeout  = 8 * time.Second
	episodeTitleCacheTTL     = 24 * time.Hour
	episodeTitleNegativeTTL  = 30 * time.Minute
	episodeTitlePartialTTL   = 5 * time.Minute
	episodeTitleHiAnimeLimit = 1 << 20
	episodeTitleWikiLimit    = 8 << 20
	wikipediaBaseURL         = "https://en.wikipedia.org/wiki"
)

var (
	episodeTitleGenericRE = regexp.MustCompile(`(?i)^\s*(?:ep(?:isode)?\.?\s*)?([0-9]+(?:\.[0-9]+)?)\s*$`)
	episodeTitleMALPathRE = regexp.MustCompile(`^/stream/mal/([1-9][0-9]*)/[0-9]+(?:\.[0-9]+)?/(sub|dub)$`)
	episodeTitleQuoteRE   = regexp.MustCompile(`^"([^"]+)"`)
	jikanGlobalLimiter    = &episodeTitleRateLimiter{}
)

// EpisodeTitleEnricher fills generic provider labels such as "Episode 1" with
// public episode titles. Metadata failures never block playback.
type EpisodeTitleEnricher struct {
	client        *http.Client
	baseURL       string
	wikipediaBase string
	hiAnimeBase   string
	rateLimiter   *episodeTitleRateLimiter
	now           func() time.Time
	mu            sync.Mutex
	cache         map[string]episodeTitleCacheEntry
	inflight      map[string]*episodeTitleCall
	hiAnimeClient *http.Client
}

type episodeTitleCacheEntry struct {
	titles  map[string]string
	expires time.Time
}

type episodeTitleCall struct {
	done   chan struct{}
	result episodeTitleFetchResult
}

type episodeTitleRateLimiter struct {
	mu     sync.Mutex
	second []time.Time
	minute []time.Time
}

type episodeTitleName struct {
	Title string `json:"title"`
}

type episodeTitleFetchResult struct {
	titles map[string]string
	ttl    time.Duration
	cache  bool
}

// NewEpisodeTitleEnricher returns an isolated metadata enricher. It shares a
// package-wide limiter so multiple desktop services respect Jikan's public
// 3/sec and 60/minute limits.
func NewEpisodeTitleEnricher() *EpisodeTitleEnricher {
	return &EpisodeTitleEnricher{
		client:        &http.Client{Timeout: episodeTitleHTTPTimeout},
		baseURL:       jikanBaseURL,
		wikipediaBase: wikipediaBaseURL,
		hiAnimeBase:   hiAnimeBaseURL,
		rateLimiter:   jikanGlobalLimiter,
		now:           time.Now,
		cache:         map[string]episodeTitleCacheEntry{},
		inflight:      map[string]*episodeTitleCall{},
		hiAnimeClient: newHiAnimeClient().client,
	}
}

func (e *EpisodeTitleEnricher) Enrich(ctx context.Context, anime Anime, episodes []Episode) []Episode {
	out := append([]Episode(nil), episodes...)
	if e == nil || ctx == nil || len(out) == 0 || ctx.Err() != nil {
		return out
	}
	needed := false
	for _, episode := range out {
		if episodeTitleNeedsEnrichment(episode) {
			needed = true
			break
		}
	}
	if !needed {
		return out
	}

	e.init()
	key := episodeTitleAnimeKey(anime)
	titles, ok := e.cached(key)
	if !ok {
		titles = e.coalesced(ctx, key, func() episodeTitleFetchResult {
			return e.fetchTitles(ctx, anime, out)
		})
	}
	if len(titles) == 0 {
		return out
	}
	for i := range out {
		if !episodeTitleNeedsEnrichment(out[i]) {
			continue
		}
		if title := catalogPlainText(titles[episodeTitleNumberKey(out[i].Number)]); title != "" {
			out[i].Title = title
		}
	}
	return out
}

func (e *EpisodeTitleEnricher) init() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		e.client = &http.Client{Timeout: episodeTitleHTTPTimeout}
	}
	if e.baseURL == "" {
		e.baseURL = jikanBaseURL
	}
	if e.wikipediaBase == "" {
		e.wikipediaBase = wikipediaBaseURL
	}
	if e.hiAnimeBase == "" {
		e.hiAnimeBase = hiAnimeBaseURL
	}
	if e.rateLimiter == nil {
		e.rateLimiter = jikanGlobalLimiter
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.cache == nil {
		e.cache = map[string]episodeTitleCacheEntry{}
	}
	if e.inflight == nil {
		e.inflight = map[string]*episodeTitleCall{}
	}
	if e.hiAnimeClient == nil {
		e.hiAnimeClient = e.client
	}
}

func (e *EpisodeTitleEnricher) cached(key string) (map[string]string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.cache[key]
	if !ok || !e.now().Before(entry.expires) {
		if ok {
			delete(e.cache, key)
		}
		return nil, false
	}
	return episodeTitleCloneMap(entry.titles), true
}

func (e *EpisodeTitleEnricher) coalesced(ctx context.Context, key string, fn func() episodeTitleFetchResult) map[string]string {
	e.mu.Lock()
	if call := e.inflight[key]; call != nil {
		done := call.done
		e.mu.Unlock()
		select {
		case <-done:
			return episodeTitleCloneMap(call.result.titles)
		case <-ctx.Done():
			return nil
		}
	}
	call := &episodeTitleCall{done: make(chan struct{})}
	e.inflight[key] = call
	e.mu.Unlock()

	call.result = fn()

	e.mu.Lock()
	delete(e.inflight, key)
	if call.result.cache {
		e.cache[key] = episodeTitleCacheEntry{titles: episodeTitleCloneMap(call.result.titles), expires: e.now().Add(call.result.ttl)}
	}
	close(call.done)
	e.mu.Unlock()

	return episodeTitleCloneMap(call.result.titles)
}

func (e *EpisodeTitleEnricher) fetchTitles(ctx context.Context, anime Anime, episodes []Episode) episodeTitleFetchResult {
	if id := e.hiAnimeMALID(ctx, anime, episodes); id > 0 {
		result := e.fetchJikanEpisodes(ctx, id)
		return e.mergeKnownSeriesTitles(ctx, anime, result)
	}
	id := e.searchMALID(ctx, anime)
	if id <= 0 {
		return episodeTitleFetchResult{ttl: episodeTitleNegativeTTL, cache: ctx.Err() == nil}
	}
	result := e.fetchJikanEpisodes(ctx, id)
	return e.mergeKnownSeriesTitles(ctx, anime, result)
}

func (e *EpisodeTitleEnricher) mergeKnownSeriesTitles(ctx context.Context, anime Anime, result episodeTitleFetchResult) episodeTitleFetchResult {
	known, ok := episodeTitleKnown(anime)
	if !ok || ctx.Err() != nil {
		return result
	}
	if len(result.titles) >= known.episodes {
		result.ttl = episodeTitleCacheTTL
		result.cache = true
		return result
	}
	wiki := e.fetchWikipediaEpisodes(ctx, anime)
	if len(wiki.titles) == 0 {
		return result
	}
	if result.titles == nil {
		result.titles = map[string]string{}
	}
	for number, title := range wiki.titles {
		if result.titles[number] == "" {
			result.titles[number] = title
		}
	}
	result.cache = true
	result.ttl = episodeTitlePartialTTL
	if len(result.titles) >= known.episodes {
		result.ttl = episodeTitleCacheTTL
	}
	return result
}

func (e *EpisodeTitleEnricher) hiAnimeMALID(ctx context.Context, anime Anime, episodes []Episode) int {
	if strings.ToLower(strings.TrimSpace(anime.Source)) != "hianime" {
		return 0
	}
	for _, episode := range episodes {
		episodeID, err := hiAnimeEpisodeIdentity(anime, episode)
		if err != nil {
			continue
		}
		endpoint := strings.TrimRight(e.hiAnimeBase, "/") + "/api/theme/episode/servers?" + url.Values{"episodeId": {episodeID}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0
		}
		req.Header.Set("User-Agent", hiAnimeUserAgent)
		req.Header.Set("Referer", episode.URL)
		client := *e.hiAnimeClient
		client.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 {
				return errors.New("HiAnime returned too many redirects")
			}
			_, err := hiAnimeURL(next.URL.String(), req.URL.Hostname())
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		body, readErr := episodeTitleReadBody(resp, episodeTitleHiAnimeLimit)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			return 0
		}
		doc, err := hiAnimeAPIHTML(body, "episode servers")
		if err != nil {
			return 0
		}
		found, ambiguous := 0, false
		doc.Find(".server-item").Each(func(_ int, server *goquery.Selection) {
			hash, _ := server.Attr("data-hash")
			decoded, err := base64.StdEncoding.Strict().DecodeString(hash)
			if err != nil {
				return
			}
			embed, err := hiAnimeURL(string(decoded), hiAnimeEmbedHost)
			if err != nil {
				return
			}
			match := episodeTitleMALPathRE.FindStringSubmatch(embed.Path)
			if len(match) != 3 {
				return
			}
			id, _ := strconv.Atoi(match[1])
			if found != 0 && id != found {
				ambiguous = true
				return
			}
			found = id
		})
		if ambiguous {
			return 0
		}
		return found
	}
	return 0
}

func (e *EpisodeTitleEnricher) searchMALID(ctx context.Context, anime Anime) int {
	title := strings.TrimSpace(anime.Title)
	if title == "" {
		return 0
	}
	var response struct {
		Data []struct {
			MALID         int                `json:"mal_id"`
			Title         string             `json:"title"`
			TitleEnglish  string             `json:"title_english"`
			TitleJapanese string             `json:"title_japanese"`
			TitleSynonyms []string           `json:"title_synonyms"`
			Titles        []episodeTitleName `json:"titles"`
			Episodes      int                `json:"episodes"`
			Type          string             `json:"type"`
			Season        string             `json:"season"`
		} `json:"data"`
	}
	values := url.Values{
		"q":        {title},
		"limit":    {strconv.Itoa(episodeTitleSearchLimit)},
		"sfw":      {"true"},
		"order_by": {"members"},
		"sort":     {"desc"},
	}
	if err := e.getJSON(ctx, "/anime", values, &response); err != nil {
		return episodeTitleKnownMALID(anime)
	}
	target := episodeTitleNormalize(title)
	candidates := []int{}
	for _, item := range response.Data {
		if item.MALID <= 0 || !episodeTitleCandidateTitleMatch(target, item.Title, item.TitleEnglish, item.TitleJapanese, item.TitleSynonyms, item.Titles) {
			continue
		}
		if anime.EpisodeCount > 0 && item.Episodes > 0 && anime.EpisodeCount != item.Episodes {
			continue
		}
		if item.Type != "" && !strings.EqualFold(item.Type, "TV") && anime.EpisodeCount > 1 {
			continue
		}
		candidates = append(candidates, item.MALID)
	}
	if len(candidates) != 1 {
		return episodeTitleKnownMALID(anime)
	}
	return candidates[0]
}

func episodeTitleKnownMALID(anime Anime) int {
	item, ok := episodeTitleKnown(anime)
	if !ok {
		return 0
	}
	return item.id
}

type episodeTitleKnownSeries struct {
	id       int
	episodes int
	wikiPath string
}

func episodeTitleKnown(anime Anime) (episodeTitleKnownSeries, bool) {
	knownIDs := map[string]episodeTitleKnownSeries{
		"naruto":            {id: 20, episodes: 220, wikiPath: "List_of_Naruto_episodes"},
		"naruto shippuden":  {id: 1735, episodes: 500, wikiPath: "List_of_Naruto:_Shippuden_episodes"},
		"naruto shippuuden": {id: 1735, episodes: 500, wikiPath: "List_of_Naruto:_Shippuden_episodes"},
	}
	item, ok := knownIDs[episodeTitleNormalize(anime.Title)]
	if !ok || anime.EpisodeCount != item.episodes {
		return episodeTitleKnownSeries{}, false
	}
	return item, true
}

func (e *EpisodeTitleEnricher) fetchJikanEpisodes(ctx context.Context, malID int) episodeTitleFetchResult {
	titles := map[string]string{}
	for page := 1; page <= episodeTitleMaxPages; page++ {
		var response struct {
			Data []struct {
				MALID         int    `json:"mal_id"`
				Title         string `json:"title"`
				TitleRomanji  string `json:"title_romanji"`
				TitleEnglish  string `json:"title_english"`
				TitleJapanese string `json:"title_japanese"`
			} `json:"data"`
			Pagination struct {
				LastVisiblePage int  `json:"last_visible_page"`
				HasNextPage     bool `json:"has_next_page"`
			} `json:"pagination"`
		}
		err := e.getJSON(ctx, fmt.Sprintf("/anime/%d/episodes", malID), url.Values{"page": {strconv.Itoa(page)}}, &response)
		if err != nil {
			if ctx.Err() != nil {
				return episodeTitleFetchResult{titles: titles}
			}
			ttl := episodeTitleNegativeTTL
			if len(titles) > 0 {
				ttl = episodeTitlePartialTTL
			}
			return episodeTitleFetchResult{titles: titles, ttl: ttl, cache: true}
		}
		for _, item := range response.Data {
			if item.MALID <= 0 {
				continue
			}
			title := firstNonBlank(item.Title, item.TitleEnglish, item.TitleRomanji, item.TitleJapanese)
			if title == "" || episodeTitleGenericText(title, strconv.Itoa(item.MALID)) {
				continue
			}
			titles[strconv.Itoa(item.MALID)] = catalogPlainText(title)
		}
		if !response.Pagination.HasNextPage {
			return episodeTitleFetchResult{titles: titles, ttl: episodeTitleCacheTTL, cache: true}
		}
		if response.Pagination.LastVisiblePage > 0 && page >= response.Pagination.LastVisiblePage {
			return episodeTitleFetchResult{titles: titles, ttl: episodeTitleCacheTTL, cache: true}
		}
	}
	return episodeTitleFetchResult{titles: titles, ttl: episodeTitlePartialTTL, cache: true}
}

func (e *EpisodeTitleEnricher) fetchWikipediaEpisodes(ctx context.Context, anime Anime) episodeTitleFetchResult {
	known, ok := episodeTitleKnown(anime)
	if !ok || known.wikiPath == "" || ctx.Err() != nil {
		return episodeTitleFetchResult{}
	}
	endpoint := strings.TrimRight(e.wikipediaBase, "/") + "/" + known.wikiPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return episodeTitleFetchResult{}
	}
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	req.Header.Set("User-Agent", "GoanimeDesktop/1.0 episode-title-enrichment (https://github.com/alvarorichard/Goanime)")
	resp, err := e.client.Do(req)
	if err != nil {
		return episodeTitleFetchResult{}
	}
	body, readErr := episodeTitleReadBody(resp, episodeTitleWikiLimit)
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusOK {
		return episodeTitleFetchResult{}
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return episodeTitleFetchResult{}
	}
	titles := map[string]string{}
	doc.Find("tr.vevent.module-episode-list-row").Each(func(_ int, row *goquery.Selection) {
		number := catalogPlainText(row.Find("th[id^='ep']").First().Text())
		if number == "" {
			number = catalogPlainText(row.Find("th").First().Text())
		}
		title := episodeTitleWikipediaSummary(row.Find("td.summary").First().Text())
		if number == "" || title == "" {
			return
		}
		titles[episodeTitleNumberKey(number)] = title
	})
	if len(titles) == 0 {
		return episodeTitleFetchResult{}
	}
	ttl := episodeTitlePartialTTL
	if len(titles) >= known.episodes {
		ttl = episodeTitleCacheTTL
	}
	return episodeTitleFetchResult{titles: titles, ttl: ttl, cache: true}
}

func episodeTitleWikipediaSummary(text string) string {
	text = strings.TrimSpace(text)
	match := episodeTitleQuoteRE.FindStringSubmatch(text)
	if len(match) == 2 {
		return catalogPlainText(match[1])
	}
	if before, _, ok := strings.Cut(text, "Transliteration:"); ok {
		return catalogPlainText(strings.Trim(before, " \"\t\r\n"))
	}
	return ""
}

func (e *EpisodeTitleEnricher) getJSON(ctx context.Context, path string, values url.Values, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.rateLimiter.wait(ctx, e.now); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimRight(e.baseURL, "/") + path)
	if err != nil {
		return err
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer resp.Body.Close()
	body, err := episodeTitleReadBody(resp, episodeTitleBodyLimit)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Jikan HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, target)
}

func episodeTitleReadBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("empty response")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("metadata response is too large")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("metadata response is empty")
	}
	return body, nil
}

func (l *episodeTitleRateLimiter) wait(ctx context.Context, nowFn func() time.Time) error {
	if nowFn == nil {
		nowFn = time.Now
	}
	for {
		now := nowFn()
		l.mu.Lock()
		l.second = episodeTitleRecent(l.second, now.Add(-time.Second))
		l.minute = episodeTitleRecent(l.minute, now.Add(-time.Minute))
		if len(l.second) < 3 && len(l.minute) < 60 {
			l.second = append(l.second, now)
			l.minute = append(l.minute, now)
			l.mu.Unlock()
			return nil
		}
		waitUntil := now.Add(time.Second)
		if len(l.second) >= 3 {
			waitUntil = l.second[0].Add(time.Second)
		}
		if len(l.minute) >= 60 {
			minuteWait := l.minute[0].Add(time.Minute)
			if minuteWait.After(waitUntil) {
				waitUntil = minuteWait
			}
		}
		l.mu.Unlock()
		timer := time.NewTimer(time.Until(waitUntil))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func episodeTitleRecent(items []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(items) && !items[i].After(cutoff) {
		i++
	}
	return items[i:]
}

func episodeTitleAnimeKey(anime Anime) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(anime.Source)),
		strings.TrimSpace(anime.URL),
		episodeTitleNormalize(anime.Title),
		strconv.Itoa(anime.EpisodeCount),
	}, "\x00")
}

func episodeTitleNeedsEnrichment(episode Episode) bool {
	return strings.TrimSpace(episode.Title) == "" || episodeTitleGenericText(episode.Title, episode.Number)
}

func episodeTitleGenericText(title, number string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return true
	}
	match := episodeTitleGenericRE.FindStringSubmatch(title)
	if len(match) != 2 {
		return false
	}
	return episodeTitleNumberKey(match[1]) == episodeTitleNumberKey(number)
}

func episodeTitleNumberKey(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		if parsed == float64(int64(parsed)) {
			return strconv.FormatInt(int64(parsed), 10)
		}
		return strconv.FormatFloat(parsed, 'f', -1, 64)
	}
	return strings.ToLower(value)
}

func episodeTitleNormalize(value string) string {
	value = animeIdentityTitle(value)
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func episodeTitleCandidateTitleMatch(target, title, english, japanese string, synonyms []string, titles []episodeTitleName) bool {
	for _, value := range []string{title, english, japanese} {
		if episodeTitleNormalize(value) == target {
			return true
		}
	}
	for _, value := range synonyms {
		if episodeTitleNormalize(value) == target {
			return true
		}
	}
	for _, value := range titles {
		if episodeTitleNormalize(value.Title) == target {
			return true
		}
	}
	return false
}

func episodeTitleCloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
