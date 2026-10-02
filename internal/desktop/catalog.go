package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/alvarorichard/Goanime/internal/api/providers"
	"github.com/alvarorichard/Goanime/internal/api/source"
	"github.com/alvarorichard/Goanime/internal/models"
	"github.com/alvarorichard/Goanime/internal/scraper"
	"github.com/alvarorichard/Goanime/internal/scraper/netx"
	"github.com/alvarorichard/Goanime/internal/util"
	"golang.org/x/net/html"
)

// Stream contains everything needed by playback and downloads. Metadata stays
// with this stream instead of leaking into a later provider's request.
type Stream struct {
	URL       string
	Headers   map[string]string
	Subtitles []models.Subtitle
}

type Catalog struct {
	search     func(context.Context, string, ...source.SourceKind) ([]*models.Anime, error)
	episodes   func(context.Context, *models.Anime) ([]models.Episode, error)
	newAdapter func(scraper.ScraperType) (scraper.UnifiedScraper, error)
	client     *http.Client
	hiAnime    catalogProvider
}

type catalogProvider interface {
	Search(context.Context, string) ([]Anime, error)
	Episodes(context.Context, Anime) ([]Episode, error)
	Resolve(context.Context, Anime, Episode, Settings) (Stream, error)
}

// Providers still expose some playback metadata and AniDB's audio preference
// through process globals. Keep their mutation and snapshot in one critical
// section, including calls that outlive a GUI cancellation.
var catalogStreamMu sync.Mutex

var catalogNumberRE = regexp.MustCompile(`\d+(?:\.\d+)?`)

func NewCatalog() *Catalog {
	return &Catalog{
		search:     providers.SearchAll,
		episodes:   providers.FetchEpisodes,
		newAdapter: scraper.NewAdapter,
		client:     util.NewFastClient(),
		hiAnime:    newHiAnimeClient(),
	}
}

func (c *Catalog) Sources() []Source {
	out := []Source{}
	if c.hiAnime != nil {
		out = append(out, Source{ID: "hianime", Name: "HiAnime", Language: "EN"})
	}
	for _, item := range []Source{
		{ID: "anidb", Name: "AniDB", Language: "EN"},
		{ID: "animefire", Name: "AnimeFire", Language: "PT-BR"},
	} {
		kind, _, err := catalogSource(item.ID)
		if err == nil {
			if _, enabled := source.Enabled(kind); enabled {
				out = append(out, item)
			}
		}
	}
	return out
}

func (c *Catalog) Search(ctx context.Context, query, sourceID string) ([]Anime, error) {
	result, err := c.SearchWithStatus(ctx, query, sourceID)
	return result.Animes, err
}

// Keep usable sources visible even when another source is offline. Each search
// owns its warnings so concurrent requests cannot overwrite one another.
func (c *Catalog) SearchWithStatus(ctx context.Context, query, sourceID string) (SearchResult, error) {
	out := SearchResult{Animes: []Anime{}, Warnings: []SourceWarning{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return out, nil
	}
	selected := strings.ToLower(strings.TrimSpace(sourceID))
	sources := c.Sources()
	if selected != "" && selected != "all" {
		if selected != "hianime" {
			kind, _, err := catalogSource(selected)
			if err != nil {
				return out, err
			}
			selected = strings.ToLower(string(kind))
		}
		var match *Source
		for _, item := range sources {
			if item.ID == selected {
				item := item
				match = &item
				break
			}
		}
		if match == nil {
			return out, fmt.Errorf("the selected source is not available")
		}
		sources = []Source{*match}
	}
	if len(sources) == 0 {
		return out, fmt.Errorf("no anime sources are enabled")
	}
	type outcome struct {
		source Source
		animes []Anime
		err    error
	}
	done := make(chan outcome, len(sources))
	for _, item := range sources {
		go func(item Source) {
			var animes []Anime
			var err error
			if item.ID == "hianime" {
				animes, err = c.hiAnime.Search(ctx, query)
			} else {
				animes, err = c.searchLegacy(ctx, query, item.ID)
				// The upstream dispatcher reports a normal empty result as an
				// error. Preserve it as an empty search, not a provider outage.
				if err != nil && strings.HasPrefix(err.Error(), "no results found for:") {
					err = nil
				}
			}
			done <- outcome{item, animes, err}
		}(item)
	}
	var failures []error
	succeeded := 0
	seen := map[string]bool{}
	completed := map[string]bool{}
	finish := func() (SearchResult, error) {
		sort.SliceStable(out.Animes, func(i, j int) bool {
			if strings.EqualFold(out.Animes[i].Title, out.Animes[j].Title) {
				return out.Animes[i].ID < out.Animes[j].ID
			}
			return strings.ToLower(out.Animes[i].Title) < strings.ToLower(out.Animes[j].Title)
		})
		sort.Slice(out.Warnings, func(i, j int) bool { return out.Warnings[i].Source < out.Warnings[j].Source })
		if succeeded == 0 {
			return out, errors.Join(failures...)
		}
		return out, nil
	}
	for range sources {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && succeeded > 0 {
				for _, item := range sources {
					if !completed[item.ID] {
						out.Warnings = append(out.Warnings, SourceWarning{Source: item.ID, Message: "Search timed out; showing results from the sources that responded."})
					}
				}
				return finish()
			}
			return out, ctx.Err()
		case result := <-done:
			completed[result.source.ID] = true
			if result.err != nil {
				message := catalogSourceFailure(result.err)
				out.Warnings = append(out.Warnings, SourceWarning{Source: result.source.ID, Message: message})
				failures = append(failures, fmt.Errorf("%s: %w", result.source.Name, result.err))
				continue
			}
			succeeded++
			for _, anime := range result.animes {
				if !seen[anime.ID] {
					seen[anime.ID] = true
					out.Animes = append(out.Animes, anime)
				}
			}
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return out, ctx.Err()
	}
	return finish()
}

func catalogSourceFailure(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "503"):
		return "Temporarily unavailable (HTTP 503)."
	case strings.Contains(message, "403"):
		return "Blocked requests from this connection (HTTP 403)."
	case strings.Contains(message, "no searchable source"):
		return "Paused after repeated failures; try again later."
	default:
		return message
	}
}

func (c *Catalog) searchLegacy(ctx context.Context, query, sourceID string) ([]Anime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []Anime{}, nil
	}
	kinds := []source.SourceKind{source.AniDB, source.AnimeFire}
	if sourceID != "" && !strings.EqualFold(sourceID, "all") {
		kind, _, err := catalogSource(sourceID)
		if err != nil {
			return nil, err
		}
		kinds = []source.SourceKind{kind}
	}
	results, err := c.search(ctx, query, kinds...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	out := []Anime{}
	seen := make(map[string]bool)
	for _, result := range results {
		if result == nil {
			continue
		}
		item, err := catalogAnime(result)
		if err != nil || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	// SearchAll collects sources concurrently. Keep cards stable across searches.
	sort.SliceStable(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Title, out[j].Title) {
			return out[i].ID < out[j].ID
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

func (c *Catalog) Episodes(ctx context.Context, anime Anime) ([]Episode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if anime.Source == "hianime" && c.hiAnime != nil {
		return c.hiAnime.Episodes(ctx, anime)
	}
	model, _, _, err := catalogModel(anime)
	if err != nil {
		return nil, err
	}
	items, err := catalogCall(ctx, func() ([]models.Episode, error) {
		return c.episodes(ctx, model)
	})
	if err != nil {
		return nil, err
	}
	out := make([]Episode, 0, len(items))
	for _, item := range items {
		number := providers.EpisodeNumber(&item)
		title := item.Title.English
		if title == "" {
			title = item.Title.Romaji
		}
		if title == "" {
			title = item.Title.Japanese
		}
		out = append(out, Episode{Number: number, Title: catalogPlainText(title), URL: item.URL})
	}
	sort.SliceStable(out, func(i, j int) bool { return catalogEpisodeLess(out[i].Number, out[j].Number) })
	return out, nil
}

func (c *Catalog) Resolve(ctx context.Context, anime Anime, episode Episode, settings Settings) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return Stream{}, err
	}
	if anime.Source == "hianime" && c.hiAnime != nil {
		return c.hiAnime.Resolve(ctx, anime, episode, settings)
	}
	model, kind, adapterType, err := catalogModel(anime)
	if err != nil {
		return Stream{}, err
	}
	if err := catalogSourceURL(episode.URL, kind); err != nil {
		return Stream{}, fmt.Errorf("invalid episode URL: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(settings.Mode))
	if mode == "" {
		mode = "sub"
	}
	if mode != "sub" && mode != "dub" {
		return Stream{}, fmt.Errorf("unsupported audio mode %q; choose sub or dub", settings.Mode)
	}
	if kind == source.AnimeFire {
		dubbed := strings.Contains(strings.ToLower(anime.URL+" "+anime.Title), "dublado")
		if mode == "dub" && !dubbed {
			return Stream{}, fmt.Errorf("AnimeFire keeps dubbed shows in separate results; choose a Dublado result to use dub mode")
		}
		if mode == "sub" && dubbed {
			return Stream{}, fmt.Errorf("this AnimeFire result is dubbed; choose dub mode or select a Legendado result")
		}
	}
	quality := strings.ToLower(strings.TrimSpace(settings.Quality))
	if quality == "" {
		quality = "best"
	}
	return catalogCall(ctx, func() (Stream, error) {
		catalogStreamMu.Lock()
		defer catalogStreamMu.Unlock()
		if err := ctx.Err(); err != nil {
			return Stream{}, err
		}
		adapter, err := c.newAdapter(adapterType)
		if err != nil {
			return Stream{}, err
		}
		util.ClearGlobalSubtitles()
		util.ClearGlobalReferer()
		util.ClearGlobalUserAgent()
		util.SetGlobalAnimeSource(model.Source)
		if kind == source.AniDB {
			previous, existed := os.LookupEnv("GOANIME_ANIDB_LANG")
			if err := os.Setenv("GOANIME_ANIDB_LANG", mode); err != nil {
				return Stream{}, err
			}
			defer func() {
				if existed {
					_ = os.Setenv("GOANIME_ANIDB_LANG", previous)
				} else {
					_ = os.Unsetenv("GOANIME_ANIDB_LANG")
				}
			}()
		}
		var streamURL string
		var metadata map[string]string
		if contextual, ok := adapter.(scraper.ContextualScraper); ok {
			streamURL, metadata, err = contextual.GetStreamURLContext(ctx, episode.URL, quality)
		} else {
			streamURL, metadata, err = adapter.GetStreamURL(episode.URL, quality)
		}
		if err != nil {
			return Stream{}, fmt.Errorf("resolve %s stream: %w", model.Source, err)
		}
		if err := ctx.Err(); err != nil {
			return Stream{}, err
		}
		if kind == source.AniDB {
			want := "jpn"
			if mode == "dub" {
				want = "eng"
			}
			if metadata["audio_lang"] != want {
				return Stream{}, fmt.Errorf("AniDB does not offer %s audio for this episode; choose the other audio mode", mode)
			}
		}
		headers := map[string]string{}
		for key, name := range map[string]string{"referer": "Referer", "user_agent": "User-Agent", "origin": "Origin", "cookie": "Cookie"} {
			if value := metadata[key]; value != "" {
				headers[name] = value
			}
		}
		if headers["Referer"] == "" {
			headers["Referer"] = util.GetGlobalReferer()
		}
		if headers["User-Agent"] == "" {
			headers["User-Agent"] = util.GetGlobalUserAgent()
		}
		if headers["User-Agent"] == "" {
			headers["User-Agent"] = netx.UserAgent
		}
		if kind == source.AnimeFire {
			headers["Referer"] = "https://animefire.io"
			streamURL, err = c.resolveAnimeFire(ctx, streamURL, quality, headers)
			if err != nil {
				return Stream{}, err
			}
		}
		if err := catalogHTTPURL(streamURL); err != nil {
			return Stream{}, fmt.Errorf("provider did not return a playable URL: %w", err)
		}
		if strings.Contains(strings.ToLower(streamURL), "blogger.com") || strings.Contains(strings.ToLower(streamURL), "blogspot.com") {
			return Stream{}, fmt.Errorf("this episode uses a Blogger embed that the desktop player cannot resolve yet; choose another result")
		}
		subtitles := []models.Subtitle{}
		for _, item := range util.GetGlobalSubtitles() {
			subtitles = append(subtitles, models.Subtitle{URL: item.URL, Language: item.Language, Label: item.Label})
		}
		if headers["Referer"] == "" {
			delete(headers, "Referer")
		}
		return Stream{URL: streamURL, Headers: headers, Subtitles: subtitles}, nil
	})
}

// AnimeFire's adapter can return its JSON quality endpoint. Reuse that endpoint
// contract without entering the CLI player's interactive quality picker.
func (c *Catalog) resolveAnimeFire(ctx context.Context, streamURL, quality string, headers map[string]string) (string, error) {
	u, err := url.Parse(streamURL)
	if err != nil {
		return "", err
	}
	if (u.Hostname() != "animefire.io" && u.Hostname() != "animefire.plus") || !strings.HasPrefix(u.Path, "/video/") {
		return streamURL, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return "", err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("User-Agent", netx.UserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("load AnimeFire video sources: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("AnimeFire video sources returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data  []catalogVideoSource `json:"data"`
		Token string               `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("AnimeFire video sources could not be read: %w", err)
	}
	if len(payload.Data) == 0 {
		if payload.Token != "" {
			return "", fmt.Errorf("this AnimeFire episode uses an embedded player that is not supported in the desktop app yet")
		}
		return "", fmt.Errorf("AnimeFire returned no playable video sources")
	}
	return catalogSelectVideo(payload.Data, quality)
}

type catalogVideoSource struct {
	Src   string `json:"src"`
	Label string `json:"label"`
}

func catalogSelectVideo(items []catalogVideoSource, quality string) (string, error) {
	valid := make([]catalogVideoSource, 0, len(items))
	for _, item := range items {
		if catalogHTTPURL(item.Src) == nil {
			valid = append(valid, item)
		}
	}
	if len(valid) == 0 {
		return "", fmt.Errorf("AnimeFire returned no valid HTTP video source")
	}
	sort.SliceStable(valid, func(i, j int) bool { return catalogResolution(valid[i].Label) > catalogResolution(valid[j].Label) })
	if quality == "" || quality == "best" || quality == "auto" {
		return valid[0].Src, nil
	}
	if quality == "worst" {
		return valid[len(valid)-1].Src, nil
	}
	want := catalogResolution(quality)
	for _, item := range valid {
		if strings.EqualFold(item.Label, quality) || (want > 0 && want == catalogResolution(item.Label)) {
			return item.Src, nil
		}
	}
	return "", fmt.Errorf("AnimeFire does not offer %s for this episode; choose best or another quality", quality)
}

func catalogResolution(label string) int {
	value := catalogNumberRE.FindString(label)
	n, _ := strconv.Atoi(value)
	return n
}

func catalogSource(id string) (source.SourceKind, scraper.ScraperType, error) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "anidb", "anidb.app":
		return source.AniDB, scraper.AniDBType, nil
	case "animefire", "animefire.io", "animefire.plus":
		return source.AnimeFire, scraper.AnimefireType, nil
	default:
		return "", 0, fmt.Errorf("source %q is not supported by the desktop app; choose AniDB or AnimeFire", id)
	}
}

func catalogModel(anime Anime) (*models.Anime, source.SourceKind, scraper.ScraperType, error) {
	kind, adapterType, err := catalogSource(anime.Source)
	if err != nil {
		return nil, "", 0, err
	}
	if _, enabled := source.Enabled(kind); !enabled {
		return nil, "", 0, fmt.Errorf("%s is disabled; enable this source before using this result", kind)
	}
	if err := catalogSourceURL(anime.URL, kind); err != nil {
		return nil, "", 0, fmt.Errorf("invalid anime URL: %w", err)
	}
	name := string(kind)
	if kind == source.AnimeFire {
		name = "Animefire.io"
	}
	return &models.Anime{Name: anime.Title, URL: anime.URL, Source: name, MediaType: models.MediaTypeAnime}, kind, adapterType, nil
}

func catalogAnime(model *models.Anime) (Anime, error) {
	kind, _, err := catalogSource(model.Source)
	if err != nil {
		return Anime{}, err
	}
	if err := catalogSourceURL(model.URL, kind); err != nil {
		return Anime{}, err
	}
	sourceID, language := "anidb", "EN"
	if kind == source.AnimeFire {
		sourceID, language = "animefire", "PT-BR"
	}
	digest := sha256.Sum256([]byte(sourceID + "\x00" + model.URL))
	title := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(model.Name, "[English]"), "[PT-BR]"))
	description := model.Details.Description
	if description == "" {
		description = model.Overview
	}
	genres := model.Details.Genres
	if len(genres) == 0 {
		genres = model.Genres
	}
	if genres == nil {
		genres = []string{}
	}
	image := model.ImageURL
	if image == "" {
		image = model.Details.CoverImage.Large
	}
	if image == "" {
		image = model.Details.CoverImage.Medium
	}
	if image != "" {
		if base, err := url.Parse(model.URL); err == nil {
			if ref, err := url.Parse(image); err == nil {
				image = base.ResolveReference(ref).String()
			}
		}
		if catalogHTTPURL(image) != nil {
			image = ""
		}
	}
	count := model.Details.Episodes
	if count == 0 {
		count = len(model.Episodes)
	}
	score := model.Rating
	if model.Details.AverageScore > 0 {
		score = float64(model.Details.AverageScore) / 10
	}
	return Anime{
		ID: sourceID + "-" + hex.EncodeToString(digest[:12]), Title: catalogPlainText(title), URL: model.URL,
		ImageURL: image, Source: sourceID, Language: language, Description: catalogPlainText(description),
		Genres: append([]string{}, genres...), Score: score, EpisodeCount: count,
	}, nil
}

func catalogPlainText(value string) string {
	z := html.NewTokenizer(strings.NewReader(value))
	var out strings.Builder
	ignored := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(out.String()), " ")
		case html.StartTagToken:
			tag, _ := z.TagName()
			if string(tag) == "script" || string(tag) == "style" {
				ignored++
			}
			out.WriteByte(' ')
		case html.EndTagToken:
			tag, _ := z.TagName()
			if (string(tag) == "script" || string(tag) == "style") && ignored > 0 {
				ignored--
			}
			out.WriteByte(' ')
		case html.SelfClosingTagToken:
			out.WriteByte(' ')
		case html.TextToken:
			if ignored == 0 {
				out.Write(z.Text())
			}
		}
	}
}

func catalogHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u != nil && u.Scheme != "http" && u.Scheme != "https") || u == nil || u.Hostname() == "" {
		return fmt.Errorf("an HTTP or HTTPS URL is required")
	}
	return nil
}

// A frontend result is not authority to fetch arbitrary hosts. Only the
// original provider's own catalog/episode URLs can enter its adapter.
func catalogSourceURL(raw string, kind source.SourceKind) error {
	if err := catalogHTTPURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	valid := kind == source.AniDB && host == "anidb.app"
	valid = valid || (kind == source.AnimeFire && (host == "animefire.io" || host == "animefire.plus"))
	if !valid || u.User != nil || (u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return fmt.Errorf("URL must belong to the selected %s source", kind)
	}
	return nil
}

func catalogEpisodeLess(a, b string) bool {
	left, leftErr := strconv.ParseFloat(catalogNumberRE.FindString(a), 64)
	right, rightErr := strconv.ParseFloat(catalogNumberRE.FindString(b), 64)
	if leftErr == nil && rightErr == nil {
		return left < right
	}
	if leftErr == nil {
		return true
	}
	if rightErr == nil {
		return false
	}
	return strings.ToLower(a) < strings.ToLower(b)
}

func catalogCall[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := fn()
		done <- result{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case item := <-done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return item.value, item.err
	}
}
