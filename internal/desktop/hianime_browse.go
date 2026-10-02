package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

type hiAnimeGenre struct {
	Name string
	Slug string
}

type hiAnimeDetail struct {
	Anime   Anime
	Genres  []hiAnimeGenre
	Related []Anime
}

var hiAnimeGenrePathRE = regexp.MustCompile(`^/genres/([a-z][a-z0-9-]{0,79})$`)

// Browse reads an actual catalog page rather than inventing a search query.
func (c *hiAnimeClient) Browse(ctx context.Context, path string) ([]Anime, error) {
	if path != "/most-popular" && path != "/top-airing" && !hiAnimeGenrePathRE.MatchString(path) {
		return nil, errors.New("unsupported HiAnime browse page")
	}
	body, err := c.get(ctx, hiAnimeBaseURL+path, hiAnimeBaseURL+"/", 4<<20)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("read HiAnime browse page: %w", err)
	}
	list := doc.Find("#main-content .film_list-wrap").First()
	if list.Length() == 0 {
		return nil, errors.New("HiAnime's discovery page is unavailable or its layout changed")
	}
	items := hiAnimeCards(list)
	if len(items) == 0 {
		return nil, errors.New("HiAnime's discovery page returned no valid anime")
	}
	return items, nil
}

func (c *hiAnimeClient) Detail(ctx context.Context, anime Anime) (hiAnimeDetail, error) {
	if _, _, err := hiAnimeIdentity(anime); err != nil {
		return hiAnimeDetail{}, err
	}
	body, err := c.get(ctx, anime.URL, hiAnimeBaseURL+"/", 4<<20)
	if err != nil {
		return hiAnimeDetail{}, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return hiAnimeDetail{}, fmt.Errorf("read HiAnime details: %w", err)
	}
	info := doc.Find("#ani_detail .anisc-info")
	if info.Length() == 0 {
		return hiAnimeDetail{}, errors.New("HiAnime's anime details are unavailable or their layout changed")
	}
	detail := hiAnimeDetail{Anime: anime, Genres: []hiAnimeGenre{}, Related: []Anime{}}
	detail.Anime.Genres = []string{}
	seen := map[string]bool{}
	info.Find(".item-list a").Each(func(_ int, link *goquery.Selection) {
		raw, _ := link.Attr("href")
		canonical, err := hiAnimeReference(hiAnimeBaseURL+"/", raw)
		if err != nil {
			return
		}
		u, err := hiAnimeURL(canonical, "hianime.at")
		if err != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return
		}
		match := hiAnimeGenrePathRE.FindStringSubmatch(u.Path)
		name := catalogPlainText(link.Text())
		if len(match) != 2 || name == "" || seen[match[1]] {
			return
		}
		seen[match[1]] = true
		detail.Genres = append(detail.Genres, hiAnimeGenre{Name: name, Slug: match[1]})
		detail.Anime.Genres = append(detail.Anime.Genres, name)
	})
	if description := catalogPlainText(doc.Find("#ani_detail .film-description .text").First().Text()); description != "" {
		detail.Anime.Description = description
	}
	// Recommendations are scoped to their labeled section, not the sidebar's
	// unrelated popularity lists or arbitrary page links.
	doc.Find("#main-content .block_area").Each(func(_ int, section *goquery.Selection) {
		if strings.EqualFold(catalogPlainText(section.Find(".cat-heading").First().Text()), "Recommended For You") {
			detail.Related = append(detail.Related, hiAnimeCards(section.Find(".film_list-wrap").First())...)
		}
	})
	return detail, nil
}

func hiAnimeCards(list *goquery.Selection) []Anime {
	out := []Anime{}
	seen := map[string]bool{}
	list.Find(".flw-item").Each(func(_ int, card *goquery.Selection) {
		link := card.Find(".film-name a").First()
		rawURL, _ := link.Attr("href")
		canonical, err := hiAnimeReference(hiAnimeBaseURL+"/", rawURL)
		if err != nil {
			return
		}
		item := Anime{URL: canonical, Source: "hianime", Language: "EN", Genres: []string{}}
		if _, _, err := hiAnimeIdentity(item); err != nil || seen[canonical] {
			return
		}
		item.Title = catalogPlainText(link.Text())
		if item.Title == "" {
			item.Title, _ = link.Attr("title")
			item.Title = catalogPlainText(item.Title)
		}
		if item.Title == "" {
			return
		}
		digest := sha256.Sum256([]byte("hianime\x00" + canonical))
		item.ID = "hianime-" + hex.EncodeToString(digest[:12])
		item.Description = catalogPlainText(card.Find(".description").First().Text())
		poster := card.Find(".film-poster-img").First()
		image, _ := poster.Attr("data-src")
		if image == "" {
			image, _ = poster.Attr("src")
		}
		if image, err = hiAnimeReference(hiAnimeBaseURL+"/", image); err == nil {
			item.ImageURL = image
		}
		for _, selector := range []string{".tick-eps", ".tick-sub", ".tick-dub"} {
			count, _ := strconv.Atoi(strings.TrimSpace(card.Find(selector).First().Text()))
			if count > item.EpisodeCount {
				item.EpisodeCount = count
			}
		}
		seen[canonical] = true
		out = append(out, item)
	})
	return out
}
