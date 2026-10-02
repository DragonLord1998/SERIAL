package desktop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

func cloneAnime(anime Anime) Anime {
	anime.Genres = append([]string{}, anime.Genres...)
	anime.Tags = append([]AnimeTag{}, anime.Tags...)
	anime.AlternateTitles = append([]string{}, anime.AlternateTitles...)
	if anime.Trailer != nil {
		trailer := *anime.Trailer
		anime.Trailer = &trailer
	}
	return anime
}

func sameStoredAnime(left, right Anime) bool {
	if left.MALID > 0 && right.MALID > 0 {
		return left.MALID == right.MALID
	}
	if left.ID != "" && left.ID == right.ID {
		return true
	}
	if left.URL != "" && left.URL == right.URL {
		return true
	}
	if left.EpisodeCount > 0 && right.EpisodeCount > 0 && left.EpisodeCount != right.EpisodeCount {
		return false
	}
	for _, leftTitle := range append([]string{left.Title}, left.AlternateTitles...) {
		key := feedbackTitleKey(leftTitle)
		if key == "" {
			continue
		}
		for _, rightTitle := range append([]string{right.Title}, right.AlternateTitles...) {
			if key == feedbackTitleKey(rightTitle) {
				return true
			}
		}
	}
	return false
}

func (s *Service) GetAnimeDetails(anime Anime) (Anime, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	return s.metadata.Details(ctx, anime)
}

func (s *Service) GetAnimeTags() ([]AnimeTag, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	return s.metadata.Tags(ctx)
}

func (s *Service) GetExploreWithTags(tagIDs []int, refresh bool) (ExploreResult, error) {
	if len(tagIDs) > 3 {
		return ExploreResult{}, errors.New("Choose up to three tags.")
	}
	for _, id := range tagIDs {
		if id <= 0 || id > 100000 {
			return ExploreResult{}, errors.New("Invalid anime tag.")
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, 22*time.Second)
	defer cancel()
	state := s.Snapshot()
	known := append([]Anime{}, state.Saved...)
	for _, entry := range state.History {
		known = append(known, entry.Anime)
	}
	if len(tagIDs) > 0 {
		return s.metadata.Explore(ctx, state.Feedback, known, tagIDs, refresh)
	}
	var provider, mal ExploreResult
	var providerErr, malErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		provider, providerErr = s.recommender.Explore(ctx, state.Feedback, known, refresh)
	}()
	go func() { defer wg.Done(); mal, malErr = s.metadata.Explore(ctx, state.Feedback, known, nil, refresh) }()
	wg.Wait()
	if providerErr != nil && malErr != nil {
		return ExploreResult{}, errors.New("Explore is temporarily unavailable. Please try again.")
	}
	out := ExploreResult{Items: []ExploreItem{}, Warnings: []string{}, Personalized: len(state.Feedback) > 0, Message: "Popular anime from MyAnimeList and available streaming sources."}
	if out.Personalized {
		out.Message = "MyAnimeList recommendations and available shows shaped by your likes and dislikes."
	}
	seen := map[string]bool{}
	for _, anime := range known {
		for _, key := range recommendationKeys(anime) {
			seen[key] = true
		}
	}
	for _, item := range state.Feedback {
		for _, key := range recommendationKeys(item.Anime) {
			seen[key] = true
		}
	}
	// Alternate sources so the initial feed includes both catalogue discovery and
	// ready-to-watch titles, without treating metadata as a streaming provider.
	for i := 0; i < len(mal.Items) || i < len(provider.Items); i++ {
		for _, items := range [][]ExploreItem{mal.Items, provider.Items} {
			if i >= len(items) {
				continue
			}
			item := items[i]
			keys := recommendationKeys(item.Anime)
			duplicate := false
			for _, key := range keys {
				if seen[key] {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			for _, key := range keys {
				seen[key] = true
			}
			item.Anime = cloneAnime(item.Anime)
			out.Items = append(out.Items, item)
		}
		if len(out.Items) >= 48 {
			out.Items = out.Items[:48]
			break
		}
	}
	out.Warnings = append(out.Warnings, mal.Warnings...)
	out.Warnings = append(out.Warnings, provider.Warnings...)
	if malErr != nil {
		out.Warnings = append(out.Warnings, "MyAnimeList metadata is temporarily unavailable. Showing available source recommendations.")
	}
	if providerErr != nil {
		out.Warnings = append(out.Warnings, "Streaming discovery is temporarily unavailable. Showing MyAnimeList recommendations.")
	}
	return out, nil
}

// ResolveAnime translates a catalogue entry into a verified source identity.
// A near title or another season never becomes an automatic playback match.
func (s *Service) ResolveAnime(anime Anime) (Anime, error) {
	if anime.Source != "mal" {
		return anime, nil
	}
	if err := ValidateMALAnime(anime); err != nil {
		return Anime{}, err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	details, err := s.metadata.Details(ctx, anime)
	if err == nil {
		anime = details
	}
	target := map[string]bool{}
	for _, title := range append([]string{anime.Title}, anime.AlternateTitles...) {
		if key := episodeTitleNormalize(title); key != "" {
			target[key] = true
		}
	}
	matches := []Anime{}
	var firstSearchError error
	successfulSearches := 0
	queries := append([]string{anime.Title}, anime.AlternateTitles...)
	tried := map[string]bool{}
	for _, query := range queries {
		if len(tried) >= 3 {
			break
		}
		key := episodeTitleNormalize(query)
		if key == "" || tried[key] {
			continue
		}
		tried[key] = true
		found, searchErr := s.catalog.Search(ctx, query, "hianime")
		if searchErr != nil {
			if firstSearchError == nil {
				firstSearchError = searchErr
			}
			continue
		}
		successfulSearches++
		for _, candidate := range found {
			if !target[episodeTitleNormalize(candidate.Title)] {
				continue
			}
			if anime.EpisodeCount > 0 && candidate.EpisodeCount > 0 && anime.EpisodeCount != candidate.EpisodeCount {
				continue
			}
			duplicate := false
			for _, prior := range matches {
				if prior.ID == candidate.ID {
					duplicate = true
				}
			}
			if !duplicate {
				matches = append(matches, candidate)
			}
		}
	}
	if ctx.Err() != nil {
		return Anime{}, ctx.Err()
	}
	if successfulSearches == 0 && firstSearchError != nil {
		return Anime{}, errors.New("The streaming source is temporarily unavailable. Try again or search for the series.")
	}
	if len(matches) != 1 {
		return Anime{}, errors.New("This catalogue title has no single verified streaming match. Search for the series to choose a source.")
	}
	result := cloneAnime(matches[0])
	result.MALID = anime.MALID
	result.Tags = append([]AnimeTag{}, anime.Tags...)
	result.Trailer = anime.Trailer
	result.MetadataSource = anime.MetadataSource
	result.AlternateTitles = append([]string{}, anime.AlternateTitles...)
	result.Genres = recommendationMergeGenres(result.Genres, anime.Genres)
	if strings.TrimSpace(result.Description) == "" {
		result.Description = anime.Description
	}
	if result.Score == 0 {
		result.Score = anime.Score
	}
	return cloneAnime(result), nil
}
