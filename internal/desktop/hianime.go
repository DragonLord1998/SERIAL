package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/alvarorichard/Goanime/internal/models"
	"github.com/alvarorichard/Goanime/internal/scraper/netx"
)

const (
	hiAnimeBaseURL   = "https://hianime.at"
	hiAnimeEmbedHost = "zokoanime.video"
	hiAnimeUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36"
)

var (
	hiAnimeSlugRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-([1-9][0-9]*)$`)
	hiAnimeIDRE         = regexp.MustCompile(`^[1-9][0-9]*$`)
	hiAnimeNumberRE     = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
	hiAnimeEmbedPathRE  = regexp.MustCompile(`^/stream/mal/[1-9][0-9]*/[0-9]+(?:\.[0-9]+)?/(sub|dub)$`)
	hiAnimePayloadRE    = regexp.MustCompile(`window\.__P\s*=\s*["']([A-Za-z0-9+/=]+)["']`)
	hiAnimeResolutionRE = regexp.MustCompile(`(?:^|,)RESOLUTION=([0-9]+)x([0-9]+)(?:,|$)`)
	hiAnimeBandwidthRE  = regexp.MustCompile(`(?:^|,)BANDWIDTH=([0-9]+)(?:,|$)`)
)

// hiAnimeClient keeps all stream headers local to the episode being resolved.
// It uses HiAnime's current theme API and its supported ZokoAnime embed, rather
// than the retired AJAX endpoints used by older provider implementations.
type hiAnimeClient struct {
	client *http.Client
}

func newHiAnimeClient() *hiAnimeClient {
	return &hiAnimeClient{client: &http.Client{
		Transport: netx.SafeScraperTransport(8 * time.Second),
		Timeout:   20 * time.Second,
	}}
}

func (c *hiAnimeClient) Search(ctx context.Context, query string) ([]Anime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []Anime{}, nil
	}
	body, err := c.get(ctx, hiAnimeBaseURL+"/search?"+url.Values{"keyword": {query}}.Encode(), hiAnimeBaseURL+"/", 4<<20)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("read HiAnime search: %w", err)
	}
	list := doc.Find("#main-content .film_list-wrap")
	if list.Length() == 0 {
		return nil, errors.New("HiAnime search page is unavailable or its layout changed; try again later")
	}
	out := hiAnimeCards(list)
	if list.Find(".flw-item").Length() > 0 && len(out) == 0 {
		return nil, errors.New("HiAnime returned an invalid search result format")
	}
	return out, nil
}

func (c *hiAnimeClient) Episodes(ctx context.Context, anime Anime) ([]Episode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, animeID, err := hiAnimeIdentity(anime)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, hiAnimeBaseURL+"/api/theme/episode/list/"+animeID, anime.URL, 8<<20)
	if err != nil {
		return nil, err
	}
	doc, err := hiAnimeAPIHTML(body, "episode list")
	if err != nil {
		return nil, err
	}
	out := []Episode{}
	seen := map[string]bool{}
	doc.Find(".ep-item").Each(func(_ int, link *goquery.Selection) {
		number, _ := link.Attr("data-number")
		id, _ := link.Attr("data-id")
		rawURL, _ := link.Attr("href")
		episodeURL, err := hiAnimeReference(hiAnimeBaseURL+"/", rawURL)
		if err != nil {
			return
		}
		item := Episode{Number: strings.TrimSpace(number), URL: episodeURL}
		parsedID, err := hiAnimeEpisodeIdentity(anime, item)
		if err != nil || id != parsedID || seen[parsedID] {
			return
		}
		item.Title = catalogPlainText(link.Find(".ep-name").First().Text())
		if item.Title == "" {
			item.Title, _ = link.Attr("title")
			item.Title = catalogPlainText(item.Title)
		}
		if item.Title == "" {
			item.Title = "Episode " + item.Number
		}
		seen[parsedID] = true
		out = append(out, item)
	})
	if len(out) == 0 {
		return nil, errors.New("HiAnime returned no valid episodes for this show")
	}
	sort.SliceStable(out, func(i, j int) bool { return catalogEpisodeLess(out[i].Number, out[j].Number) })
	return out, nil
}

func (c *hiAnimeClient) Resolve(ctx context.Context, anime Anime, episode Episode, settings Settings) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return Stream{}, err
	}
	episodeID, err := hiAnimeEpisodeIdentity(anime, episode)
	if err != nil {
		return Stream{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(settings.Mode))
	if mode == "" {
		mode = "sub"
	}
	if mode != "sub" && mode != "dub" {
		return Stream{}, fmt.Errorf("unsupported HiAnime audio mode %q; choose sub or dub", settings.Mode)
	}
	quality := strings.ToLower(strings.TrimSpace(settings.Quality))
	if quality == "" {
		quality = "best"
	}
	if _, err := hiAnimeQuality(quality); err != nil {
		return Stream{}, err
	}
	// The frontend sends episode objects back to Go. Check membership rather than
	// trusting an arbitrary numeric ID paired with a plausible show URL.
	episodes, err := c.Episodes(ctx, anime)
	if err != nil {
		return Stream{}, err
	}
	found := false
	for _, available := range episodes {
		if available.URL == episode.URL && available.Number == episode.Number {
			found = true
			break
		}
	}
	if !found {
		return Stream{}, errors.New("the selected HiAnime episode does not belong to this show; refresh the episode list")
	}
	body, err := c.get(ctx, hiAnimeBaseURL+"/api/theme/episode/servers?"+url.Values{"episodeId": {episodeID}}.Encode(), episode.URL, 1<<20)
	if err != nil {
		return Stream{}, err
	}
	doc, err := hiAnimeAPIHTML(body, "episode servers")
	if err != nil {
		return Stream{}, err
	}
	var embeds []string
	var invalidServer bool
	doc.Find(".server-item").Each(func(_ int, server *goquery.Selection) {
		serverMode, _ := server.Attr("data-type")
		if serverMode != mode {
			return
		}
		hash, _ := server.Attr("data-hash")
		if len(hash) > 4096 {
			invalidServer = true
			return
		}
		decoded, decodeErr := base64.StdEncoding.Strict().DecodeString(hash)
		if decodeErr != nil {
			invalidServer = true
			return
		}
		embed, parseErr := hiAnimeURL(string(decoded), hiAnimeEmbedHost)
		if parseErr != nil || !hiAnimeEmbedPathRE.MatchString(embed.Path) || !strings.HasSuffix(embed.Path, "/"+mode) || embed.RawQuery != "" || embed.Fragment != "" || embed.RawPath != "" {
			// Other legitimate servers can coexist, but only this verified embed
			// implementation is supported. Never fetch arbitrary decoded URLs.
			invalidServer = true
			return
		}
		embeds = append(embeds, embed.String())
	})
	if len(embeds) == 0 {
		if invalidServer {
			return Stream{}, fmt.Errorf("HiAnime has no supported %s stream for this episode; its available server format may have changed", mode)
		}
		return Stream{}, fmt.Errorf("HiAnime does not offer a %s stream for this episode; choose another audio mode", mode)
	}
	var failures []string
	for _, embed := range embeds {
		stream, err := c.resolveEmbed(ctx, embed, episode.URL, quality)
		if err == nil {
			return stream, nil
		}
		if ctx.Err() != nil {
			return Stream{}, ctx.Err()
		}
		failures = append(failures, err.Error())
	}
	return Stream{}, fmt.Errorf("resolve HiAnime %s stream: %s", mode, strings.Join(failures, "; "))
}

func (c *hiAnimeClient) resolveEmbed(ctx context.Context, embedURL, episodeURL, quality string) (Stream, error) {
	body, err := c.get(ctx, embedURL, episodeURL, 1<<20)
	if err != nil {
		return Stream{}, err
	}
	match := hiAnimePayloadRE.FindSubmatch(body)
	if len(match) != 2 {
		return Stream{}, errors.New("ZokoAnime did not return a supported video payload")
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(string(match[1]))
	if err != nil {
		return Stream{}, errors.New("ZokoAnime returned an invalid video payload")
	}
	key := []byte("otaku-embed-v1")
	for i := range payload {
		payload[i] ^= key[i%len(key)]
	}
	var decoded struct {
		Src       string `json:"src"`
		Subtitles []struct {
			Language string `json:"lang"`
			Label    string `json:"label"`
			Default  bool   `json:"default"`
			Src      string `json:"src"`
		} `json:"subtitles"`
	}
	if !utf8.Valid(payload) || json.Unmarshal(payload, &decoded) != nil {
		return Stream{}, errors.New("ZokoAnime returned an invalid video payload")
	}
	if _, err := hiAnimeURL(decoded.Src, ""); err != nil {
		return Stream{}, errors.New("ZokoAnime returned an invalid video URL")
	}
	referer := "https://" + hiAnimeEmbedHost + "/"
	playlist, err := c.get(ctx, decoded.Src, referer, 1<<20)
	if err != nil {
		return Stream{}, err
	}
	selected, err := hiAnimeSelectPlaylist(decoded.Src, playlist, quality)
	if err != nil {
		return Stream{}, err
	}
	if selected != decoded.Src {
		media, err := c.get(ctx, selected, referer, 1<<20)
		if err != nil {
			return Stream{}, err
		}
		if !hiAnimeIsPlaylist(media) || !bytes.Contains(media, []byte("#EXTINF:")) {
			return Stream{}, errors.New("HiAnime's selected video is unavailable or is not an HLS media playlist")
		}
	}
	stream := Stream{URL: selected, Headers: map[string]string{"Referer": referer, "User-Agent": hiAnimeUserAgent}, Subtitles: []models.Subtitle{}}
	// Prefer the provider's default English track. A missing subtitle is not a
	// reason to replace the selected audio mode or refuse an otherwise valid video.
	for pass := 0; pass < 2 && len(stream.Subtitles) == 0; pass++ {
		for _, subtitle := range decoded.Subtitles {
			english := strings.EqualFold(subtitle.Language, "en") || strings.EqualFold(subtitle.Language, "eng") || strings.EqualFold(subtitle.Label, "English")
			if !english || (pass == 0 && !subtitle.Default) {
				continue
			}
			if _, err := hiAnimeURL(subtitle.Src, ""); err != nil {
				continue
			}
			stream.Subtitles = append(stream.Subtitles, models.Subtitle{URL: subtitle.Src, Language: "en", Label: "English"})
			break
		}
	}
	return stream, nil
}

func (c *hiAnimeClient) get(ctx context.Context, rawURL, referer string, limit int64) ([]byte, error) {
	u, err := hiAnimeURL(rawURL, "")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", hiAnimeUserAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "text/html,application/json,application/vnd.apple.mpegurl,*/*;q=0.8")
	client := *c.client
	client.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 5 {
			return errors.New("HiAnime returned too many redirects")
		}
		// Keep each stage on its requested host. A site redirect must not turn a
		// trusted embed or playlist request into a request to an unrelated target.
		_, err := hiAnimeURL(next.URL.String(), u.Hostname())
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("HiAnime request to %s failed: %w", u.Hostname(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HiAnime request to %s failed (HTTP %d); the provider may be temporarily unavailable", u.Hostname(), response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read HiAnime response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, errors.New("HiAnime returned an unexpectedly large response")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("HiAnime returned an empty response")
	}
	lower := bytes.ToLower(body)
	for _, challenge := range [][]byte{[]byte("<title>just a moment"), []byte("id=\"challenge-form\""), []byte("cf-chl-widget"), []byte("<title>access denied")} {
		if bytes.Contains(lower, challenge) {
			return nil, errors.New("HiAnime requires a browser verification; try again later")
		}
	}
	return body, nil
}

func hiAnimeAPIHTML(body []byte, name string) (*goquery.Document, error) {
	var response struct {
		Status bool   `json:"status"`
		HTML   string `json:"html"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("HiAnime returned an invalid %s response", name)
	}
	if !response.Status || strings.TrimSpace(response.HTML) == "" {
		return nil, fmt.Errorf("HiAnime's %s is currently unavailable", name)
	}
	return goquery.NewDocumentFromReader(strings.NewReader(response.HTML))
}

func hiAnimeIdentity(anime Anime) (slug, id string, err error) {
	if anime.Source != "hianime" {
		return "", "", errors.New("the selected show is not a HiAnime result")
	}
	u, err := hiAnimeURL(anime.URL, "hianime.at")
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", "", errors.New("invalid HiAnime show URL")
	}
	slug = strings.TrimPrefix(u.Path, "/")
	match := hiAnimeSlugRE.FindStringSubmatch(slug)
	if len(match) != 2 || len(slug) > 250 {
		return "", "", errors.New("invalid HiAnime show URL")
	}
	return slug, match[1], nil
}

func hiAnimeEpisodeIdentity(anime Anime, episode Episode) (string, error) {
	slug, _, err := hiAnimeIdentity(anime)
	if err != nil {
		return "", err
	}
	u, err := hiAnimeURL(episode.URL, "hianime.at")
	if err != nil || u.Path != "/watch/"+slug || u.RawPath != "" || u.Fragment != "" || !hiAnimeNumberRE.MatchString(episode.Number) {
		return "", errors.New("invalid HiAnime episode URL or episode number")
	}
	values, err := url.ParseQuery(u.RawQuery)
	ids := values["ep"]
	if err != nil || len(values) != 1 || len(ids) != 1 || !hiAnimeIDRE.MatchString(ids[0]) || len(ids[0]) > 12 {
		return "", errors.New("invalid HiAnime episode ID")
	}
	return ids[0], nil
}

func hiAnimeURL(rawURL, requiredHost string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") || strings.ContainsAny(rawURL, "\r\n\x00") {
		return nil, errors.New("HiAnime returned an invalid HTTPS URL")
	}
	host := strings.ToLower(u.Hostname())
	if requiredHost != "" && host != requiredHost {
		return nil, errors.New("HiAnime returned a URL on an unsupported host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return nil, errors.New("HiAnime returned a non-public URL")
	}
	if ip := net.ParseIP(host); ip != nil && netx.IsDisallowedIP(host) {
		return nil, errors.New("HiAnime returned a non-public URL")
	}
	return u, nil
}

func hiAnimeReference(base, rawReference string) (string, error) {
	if strings.TrimSpace(rawReference) == "" {
		return "", errors.New("HiAnime returned an empty URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(rawReference)
	if err != nil {
		return "", err
	}
	resolved := u.ResolveReference(reference).String()
	_, err = hiAnimeURL(resolved, "")
	return resolved, err
}

func hiAnimeQuality(quality string) (int, error) {
	if quality == "best" || quality == "auto" || quality == "worst" {
		return 0, nil
	}
	value := strings.TrimSuffix(quality, "p")
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 || n > 4320 {
		return 0, fmt.Errorf("unsupported HiAnime video quality %q", quality)
	}
	return n, nil
}

func hiAnimeIsPlaylist(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})), []byte("#EXTM3U"))
}

func hiAnimeSelectPlaylist(baseURL string, body []byte, quality string) (string, error) {
	wanted, err := hiAnimeQuality(quality)
	if err != nil {
		return "", err
	}
	if !hiAnimeIsPlaylist(body) {
		return "", errors.New("HiAnime returned an unavailable video or an invalid HLS playlist")
	}
	type variant struct {
		url       string
		height    int
		bandwidth int64
	}
	var variants []variant
	var pending *variant
	separateAudio := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, "TYPE=AUDIO") && strings.Contains(line, "URI=") {
			separateAudio = true
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			attributes := strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")
			pending = &variant{}
			if match := hiAnimeResolutionRE.FindStringSubmatch(attributes); len(match) == 3 {
				pending.height, _ = strconv.Atoi(match[2])
			}
			if match := hiAnimeBandwidthRE.FindStringSubmatch(attributes); len(match) == 2 {
				pending.bandwidth, _ = strconv.ParseInt(match[1], 10, 64)
			}
			continue
		}
		if pending != nil && line != "" && !strings.HasPrefix(line, "#") {
			resolved, err := hiAnimeReference(baseURL, line)
			if err != nil {
				return "", errors.New("HiAnime returned an invalid HLS variant URL")
			}
			pending.url = resolved
			variants = append(variants, *pending)
			pending = nil
		}
	}
	if scanner.Err() != nil || pending != nil {
		return "", errors.New("HiAnime returned an incomplete HLS playlist")
	}
	if len(variants) == 0 {
		if !bytes.Contains(body, []byte("#EXTINF:")) {
			return "", errors.New("HiAnime returned an empty HLS playlist")
		}
		return baseURL, nil
	}
	// Selecting a bare video variant would discard separate audio. Preserve the
	// master for automatic selection, and make the fixed-quality limitation clear.
	if separateAudio {
		if quality == "best" || quality == "auto" {
			return baseURL, nil
		}
		return "", errors.New("this HiAnime stream uses separate audio tracks; choose best quality")
	}
	sort.SliceStable(variants, func(i, j int) bool {
		if variants[i].height == variants[j].height {
			return variants[i].bandwidth > variants[j].bandwidth
		}
		return variants[i].height > variants[j].height
	})
	if quality == "best" || quality == "auto" {
		return variants[0].url, nil
	}
	if quality != "worst" {
		for _, variant := range variants {
			if variant.height > 0 && variant.height <= wanted {
				return variant.url, nil
			}
		}
	}
	// Some episodes have a single 800p rendition. Use the smallest available
	// rendition when no rendition fits the requested height instead of failing.
	return variants[len(variants)-1].url, nil
}
